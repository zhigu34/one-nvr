package tlsmanager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/safejson"
	"io"
)

type ApplyIntent struct {
	CertificateID    id.ID          `json:"certificate_id"`
	ExpectedActiveID *id.ID         `json:"expected_active_id"`
	Operation        string         `json:"operation"`
	ActorID          id.ID          `json:"actor_id,omitempty"`
	Automatic        bool           `json:"automatic"`
	Metadata         Metadata       `json:"-"`
	CommittedResult  *GatewayResult `json:"-"`
}
type GatewayResult struct {
	JobID       id.ID  `json:"job_id"`
	State       string `json:"state"`
	ActiveID    *id.ID `json:"active_id"`
	LeafSHA256  string `json:"leaf_sha256"`
	ChainSHA256 string `json:"chain_sha256"`
	ErrorCode   string `json:"error_code"`
}

func decodeIntent(data []byte) (ApplyIntent, error) {
	var out ApplyIntent
	if len(data) > 4096 {
		return out, auth.ErrInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil {
		return out, auth.ErrInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return out, auth.ErrInvalid
	}
	if _, err := id.Parse(string(out.CertificateID)); err != nil {
		return out, auth.ErrInvalid
	}
	if out.ExpectedActiveID != nil {
		if _, err := id.Parse(string(*out.ExpectedActiveID)); err != nil {
			return out, auth.ErrInvalid
		}
	}
	if out.ActorID != "" {
		if _, err := id.Parse(string(out.ActorID)); err != nil {
			return out, auth.ErrInvalid
		}
	}
	if out.Operation != "apply" && out.Operation != "rollback" {
		return out, auth.ErrInvalid
	}
	return out, nil
}
func fencedJob(ctx context.Context, tx pgx.Tx, lease jobs.Lease) ([]byte, error) {
	var payload []byte
	err := tx.QueryRow(ctx, "SELECT payload FROM jobs WHERE id=$1 AND kind='tls.apply' AND state='running' AND attempt=$2 AND fencing_token=$3 AND lease_expires_at>clock_timestamp() FOR UPDATE", lease.ID, lease.Attempt, lease.FencingToken).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, jobs.ErrLeaseLost
	}
	return payload, err
}
func sameOptionalID(a, b *id.ID) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func (s *Service) PrepareApply(ctx context.Context, lease jobs.Lease) (ApplyIntent, error) {
	var out ApplyIntent
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		payload, err := fencedJob(ctx, tx, lease)
		if err != nil {
			return err
		}
		out, err = decodeIntent(payload)
		if err != nil {
			return err
		}
		var completed []byte
		err = tx.QueryRow(ctx, "SELECT result FROM gateway_apply_results WHERE job_id=$1", lease.ID).Scan(&completed)
		if err == nil {
			var result GatewayResult
			if err = json.Unmarshal(completed, &result); err != nil {
				return err
			}
			out.CommittedResult = &result
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		state, err := s.currentTx(ctx, tx)
		if err != nil {
			return err
		}
		if s.Protocol != "https" || state.DesiredID == nil || *state.DesiredID != out.CertificateID || !sameOptionalID(state.ActiveID, out.ExpectedActiveID) || (out.Automatic && !state.AutoApply) {
			return auth.ErrConflict
		}
		version, err := s.readVersion(ctx, tx, out.CertificateID)
		if err != nil {
			return err
		}
		out.Metadata = version.Metadata
		return nil
	})
	if err != nil {
		var failure *fault.Error
		if errors.As(err, &failure) && failure.Status >= 400 && failure.Status < 500 {
			code := "candidate_invalid"
			if errors.Is(err, auth.ErrConflict) {
				code = "version_conflict"
			}
			if s.Protocol != "https" {
				code = "https_disabled"
			}
			result := GatewayResult{JobID: lease.ID, State: "failed", ErrorCode: code}
			committed := s.CommitGatewayResult(ctx, lease, result)
			if committed == nil {
				out.CommittedResult = &result
				return out, nil
			}
			// A superseded job must not alter the newer intent. Its own lease can
			// terminate safely; temporary database errors still retry normally.
			if errors.Is(committed, auth.ErrConflict) {
				return out, &jobs.PermanentFailure{Code: "version_conflict"}
			}
			return out, committed
		}
	}
	return out, err
}

var gatewayFailureCodes = map[string]bool{"candidate_invalid": true, "version_conflict": true, "verification_failed": true, "rollback_unverified": true, "interrupted_application": true, "https_disabled": true, "request_invalid": true}

func (s *Service) CommitGatewayResult(ctx context.Context, lease jobs.Lease, result GatewayResult) error {
	if result.JobID != lease.ID || (result.State != "applied" && result.State != "failed") || (result.State == "failed" && !gatewayFailureCodes[result.ErrorCode]) {
		return auth.ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := lockTLS(ctx, tx); err != nil {
			return err
		}
		payload, err := fencedJob(ctx, tx, lease)
		if err != nil {
			return err
		}
		in, err := decodeIntent(payload)
		if err != nil {
			return err
		}
		var prior []byte
		err = tx.QueryRow(ctx, "SELECT result FROM gateway_apply_results WHERE job_id=$1", lease.ID).Scan(&prior)
		if err == nil {
			var stored GatewayResult
			if err = json.Unmarshal(prior, &stored); err != nil {
				return err
			}
			if stored.JobID != result.JobID || stored.State != result.State || !sameOptionalID(stored.ActiveID, result.ActiveID) || stored.LeafSHA256 != result.LeafSHA256 || stored.ChainSHA256 != result.ChainSHA256 || stored.ErrorCode != result.ErrorCode {
				return auth.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		state, err := s.currentTx(ctx, tx)
		if err != nil {
			return err
		}
		if state.DesiredID == nil || *state.DesiredID != in.CertificateID || !sameOptionalID(state.ActiveID, in.ExpectedActiveID) {
			return auth.ErrConflict
		}
		if result.State == "applied" {
			version, err := s.readVersion(ctx, tx, in.CertificateID)
			if err != nil {
				return err
			}
			if result.ActiveID == nil || *result.ActiveID != in.CertificateID || result.LeafSHA256 != version.LeafSHA256 || result.ChainSHA256 != version.ChainSHA256 || result.ErrorCode != "" {
				return auth.ErrInvalid
			}
			_, err = tx.Exec(ctx, "UPDATE gateway_tls_state SET previous_id=CASE WHEN active_id IS DISTINCT FROM $1 THEN active_id ELSE previous_id END,active_id=$1,desired_id=NULL,state='active',error_code=NULL,last_apply_at=clock_timestamp(),version=version+1 WHERE singleton", in.CertificateID)
			if err != nil {
				return err
			}
		} else {
			if result.ActiveID != nil && !sameOptionalID(result.ActiveID, in.ExpectedActiveID) {
				return auth.ErrInvalid
			}
			if _, err = tx.Exec(ctx, "UPDATE gateway_tls_state SET desired_id=NULL,state='unavailable',error_code=$1,version=version+1 WHERE singleton", result.ErrorCode); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		encoded, err = safejson.Canonical(encoded)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO gateway_apply_results(job_id,result) VALUES($1,$2)", lease.ID, encoded); err != nil {
			return err
		}
		action := "tls.applied"
		if result.State == "failed" {
			action = "tls.apply_failed"
		}
		details, err := json.Marshal(struct {
			JobID     id.ID  `json:"job_id"`
			Operation string `json:"operation"`
		}{lease.ID, in.Operation})
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: in.ActorID, Action: action, ObjectID: in.CertificateID, Details: details})
	})
}
