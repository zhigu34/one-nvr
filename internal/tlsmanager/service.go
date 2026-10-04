package tlsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"net/url"
	"path/filepath"
	"time"
)

type Service struct {
	DB                                *database.DB
	Auth                              *auth.Service
	DataDir, InputDir, Host, Protocol string
	watcher                           *Watcher
}

func New(db *database.DB, a *auth.Service, dataDir, publicURL, inputDir string) *Service {
	u, _ := url.Parse(publicURL)
	s := &Service{DB: db, Auth: a, DataDir: dataDir, InputDir: inputDir}
	if u != nil {
		s.Host = u.Hostname()
		s.Protocol = u.Scheme
	}
	s.watcher = &Watcher{Directory: inputDir, Host: s.Host}
	return s
}
func (s *Service) source() string {
	if s.InputDir != "" {
		return "directory"
	}
	return "manual"
}

func (s *Service) Initialize(ctx context.Context) error {
	if s.Host == "" || (s.Protocol != "http" && s.Protocol != "https") || !filepath.IsAbs(s.DataDir) {
		return auth.ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO gateway_tls_state(singleton,source,check_state,check_reason) VALUES(true,$1,$2,$3) ON CONFLICT(singleton) DO UPDATE SET source=EXCLUDED.source,candidate_id=NULL,check_state=EXCLUDED.check_state,check_reason=EXCLUDED.check_reason,version=gateway_tls_state.version+1 WHERE gateway_tls_state.source<>EXCLUDED.source`, s.source(), map[bool]string{true: "manual", false: "pending"}[s.InputDir == ""], map[bool]string{true: "manual_management", false: "not_checked"}[s.InputDir == ""])
		return err
	})
}

func (s *Service) currentTx(ctx context.Context, tx pgx.Tx) (State, error) {
	state, err := scanState(tx.QueryRow(ctx, "SELECT "+stateColumns+" FROM gateway_tls_state WHERE singleton FOR UPDATE"))
	if err != nil {
		return state, err
	}
	if state.Source != s.source() {
		return State{}, fault.New(503, "tls_source_changed", "证书来源已改变，请等待部署完成")
	}
	return state, nil
}
func (s *Service) Current(ctx context.Context, p auth.Principal) (State, error) {
	var out State
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	out, err := scanState(s.DB.Pool.QueryRow(ctx, "SELECT "+stateColumns+" FROM gateway_tls_state WHERE singleton"))
	if err != nil {
		return out, err
	}
	if out.LastCheckAt != nil {
		at := out.LastCheckAt.UTC()
		out.LastCheckAt = &at
	}
	if out.LastApplyAt != nil {
		at := out.LastApplyAt.UTC()
		out.LastApplyAt = &at
	}
	out.Protocol = s.Protocol
	out.Certificates = []Version{}
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,source,metadata FROM tls_certificates WHERE id IN (SELECT id FROM tls_certificates ORDER BY created_at DESC,id DESC LIMIT 100) OR id=$1 OR id=$2 OR id=$3 OR id=$4 ORDER BY created_at DESC,id DESC`, out.ActiveID, out.PreviousID, out.DesiredID, out.CandidateID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return out, err
		}
		out.Certificates = append(out.Certificates, v)
	}
	return out, rows.Err()
}

func (s *Service) Import(ctx context.Context, p auth.Principal, chain, key []byte) (Version, error) {
	var out Version
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		state, err := s.currentTx(ctx, tx)
		if err != nil {
			return err
		}
		if state.Source != "manual" {
			return fault.New(409, "tls_directory_managed", "映射目录模式请更新目录中的证书文件")
		}
		out, err = s.importTx(ctx, tx, chain, key, "manual")
		if err != nil {
			return err
		}
		if state.CandidateID == nil || *state.CandidateID != out.ID {
			if _, err = tx.Exec(ctx, "UPDATE gateway_tls_state SET candidate_id=$1,version=version+1 WHERE singleton", out.ID); err != nil {
				return err
			}
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "tls.imported", ObjectID: out.ID})
	})
	return out, err
}

func (s *Service) SetAutoApply(ctx context.Context, p auth.Principal, expected int64, enabled bool) error {
	if expected < 1 {
		return auth.ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		state, err := s.currentTx(ctx, tx)
		if err != nil {
			return err
		}
		if state.Version != expected {
			return auth.ErrConflict
		}
		if state.Source != "directory" {
			return fault.New(409, "tls_not_directory_managed", "手动证书模式没有目录自动应用")
		}
		if !enabled {
			rows, err := tx.Query(ctx, "SELECT state FROM jobs WHERE kind='tls.apply' AND state IN ('queued','running') AND payload->>'automatic'='true' ORDER BY id FOR UPDATE")
			if err != nil {
				return err
			}
			running := false
			queued := false
			for rows.Next() {
				var jobState string
				if err := rows.Scan(&jobState); err != nil {
					rows.Close()
					return err
				}
				if jobState == "running" {
					running = true
				}
				if jobState == "queued" {
					queued = true
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			if running {
				return fault.New(409, "tls_apply_in_progress", "证书正在应用，请完成后再暂停")
			}
			if queued {
				if _, err = tx.Exec(ctx, "UPDATE jobs SET state='failed',error_code='auto_apply_paused',updated_at=clock_timestamp() WHERE kind='tls.apply' AND state='queued' AND payload->>'automatic'='true'"); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, "UPDATE gateway_tls_state SET desired_id=NULL,state=CASE WHEN active_id IS NULL THEN 'pending' ELSE 'active' END WHERE singleton"); err != nil {
					return err
				}
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE gateway_tls_state SET auto_apply=$1,version=version+1 WHERE singleton", enabled); err != nil {
			return err
		}
		// Resuming is picked up by a new validated input check, never a stale DTO.
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "tls.auto_apply_changed"})
	})
}

func (s *Service) queueApplyTx(ctx context.Context, tx pgx.Tx, state State, target id.ID, operation string, actor id.ID) (id.ID, error) {
	return s.queueApplyTxWithAutomatic(ctx, tx, state, target, operation, actor, actor == "")
}
func (s *Service) queueApplyTxWithAutomatic(ctx context.Context, tx pgx.Tx, state State, target id.ID, operation string, actor id.ID, automatic bool) (id.ID, error) {
	if state.State == "applying" {
		return "", auth.ErrConflict
	}
	var unfinished bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM jobs WHERE kind='tls.apply' AND state IN ('queued','running'))").Scan(&unfinished); err != nil {
		return "", err
	}
	if unfinished {
		return "", auth.ErrConflict
	}
	if _, err := s.readVersion(ctx, tx, target); err != nil {
		return "", err
	}
	key, err := id.New()
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		CertificateID    id.ID  `json:"certificate_id"`
		ExpectedActiveID *id.ID `json:"expected_active_id"`
		Operation        string `json:"operation"`
		ActorID          id.ID  `json:"actor_id,omitempty"`
		Automatic        bool   `json:"automatic"`
	}{target, state.ActiveID, operation, actor, automatic})
	if err != nil {
		return "", err
	}
	jobID, err := jobs.Enqueue(ctx, tx, jobs.Input{Kind: "tls.apply", ObjectID: target, IdempotencyKey: string(key), Payload: payload})
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, "UPDATE gateway_tls_state SET desired_id=$1,state='applying',error_code=NULL,version=version+1 WHERE singleton", target)
	return jobID, err
}

func (s *Service) RequestApply(ctx context.Context, p auth.Principal, target id.ID, expected int64) (id.ID, error) {
	var jobID id.ID
	if expected < 1 {
		return "", auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		state, err := s.currentTx(ctx, tx)
		if err != nil {
			return err
		}
		if state.Version != expected {
			return auth.ErrConflict
		}
		if s.Protocol != "https" {
			return fault.New(409, "tls_https_disabled", "HTTP 模式只保存待用证书，请通过部署切换 HTTPS")
		}
		jobID, err = s.queueApplyTx(ctx, tx, state, target, "apply", p.UserID)
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "tls.apply_requested", ObjectID: target})
	})
	return jobID, err
}

func (s *Service) RequestRollback(ctx context.Context, p auth.Principal, expected int64) (id.ID, error) {
	var jobID id.ID
	if expected < 1 {
		return "", auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		state, err := s.currentTx(ctx, tx)
		if err != nil {
			return err
		}
		if state.Version != expected {
			return auth.ErrConflict
		}
		if s.Protocol != "https" {
			return fault.New(409, "tls_https_disabled", "HTTP 模式没有 TLS 回滚操作")
		}
		if state.PreviousID == nil {
			return fault.New(409, "tls_no_previous_version", "没有可回滚的上一有效证书")
		}
		jobID, err = s.queueApplyTx(ctx, tx, state, *state.PreviousID, "rollback", p.UserID)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE gateway_tls_state SET auto_apply=false WHERE singleton"); err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "tls.rollback_requested", ObjectID: *state.PreviousID})
	})
	return jobID, err
}

func (s *Service) RequestDirectoryCheck(ctx context.Context, p auth.Principal) (id.ID, error) {
	var jobID id.ID
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		state, err := s.currentTx(ctx, tx)
		if err != nil {
			return err
		}
		if state.Source != "directory" {
			return fault.New(409, "tls_not_directory_managed", "当前不是目录证书模式")
		}
		err = tx.QueryRow(ctx, "SELECT id FROM jobs WHERE kind='tls.check' AND state IN ('queued','running') ORDER BY created_at LIMIT 1").Scan(&jobID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		key, err := id.New()
		if err != nil {
			return err
		}
		jobID, err = jobs.Enqueue(ctx, tx, jobs.Input{Kind: "tls.check", IdempotencyKey: string(key)})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "tls.check_requested"})
	})
	return jobID, err
}

type CheckResult struct {
	State         string `json:"state"`
	Reason        string `json:"reason"`
	CertificateID *id.ID `json:"certificate_id"`
	JobID         *id.ID `json:"job_id"`
}

func (s *Service) CheckDirectory(ctx context.Context) (CheckResult, error) {
	var result CheckResult
	// Serialize DB intent and input sampling. A failed DB transaction cannot
	// apply a certificate; gateway only consumes the subsequently committed job.
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		state, err := s.currentTx(ctx, tx)
		if err != nil {
			return err
		}
		if state.Source != "directory" {
			return auth.ErrInvalid
		}
		sample, sampleErr := s.watcher.Sample(time.Now())
		result.State = sample.State
		result.Reason = sample.Reason
		if sampleErr != nil {
			var failure *fault.Error
			if errors.As(sampleErr, &failure) {
				result.Reason = failure.Code
			}
			_, err = tx.Exec(ctx, `UPDATE gateway_tls_state SET last_check_at=clock_timestamp(),check_state='unavailable',check_reason=$1,consecutive_errors=CASE WHEN check_state='unavailable' AND check_reason=$1 THEN LEAST(consecutive_errors::bigint+1,2147483647)::integer ELSE 1 END WHERE singleton`, result.Reason)
			return err
		}
		if sample.State != "stable" {
			_, err = tx.Exec(ctx, "UPDATE gateway_tls_state SET last_check_at=clock_timestamp(),check_state='stabilizing',check_reason='stable_pair_required',consecutive_errors=0 WHERE singleton")
			return err
		}
		version, err := s.importTx(ctx, tx, sample.chain, sample.key, "directory")
		if err != nil {
			return err
		}
		result.State = "valid"
		result.Reason = "pair_validated"
		result.CertificateID = &version.ID
		if _, err = tx.Exec(ctx, `UPDATE gateway_tls_state SET candidate_id=$1,version=version+CASE WHEN candidate_id IS DISTINCT FROM $1 THEN 1 ELSE 0 END,last_check_at=clock_timestamp(),check_state='valid',check_reason='pair_validated',consecutive_errors=0 WHERE singleton`, version.ID); err != nil {
			return err
		}
		if state.AutoApply && s.Protocol == "https" && state.State != "applying" && (state.ActiveID == nil || *state.ActiveID != version.ID) {
			jobID, err := s.queueApplyTx(ctx, tx, state, version.ID, "apply", "")
			if err != nil {
				return err
			}
			result.JobID = &jobID
			return audit.Append(ctx, tx, audit.Entry{Action: "tls.directory_apply_requested", ObjectID: version.ID})
		}
		return nil
	})
	return result, err
}
