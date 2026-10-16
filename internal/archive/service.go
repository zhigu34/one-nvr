package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// Task failure codes, shared with the jobs table and the drawer.
const (
	errTargetGone     = "target_unavailable"
	errTargetDenied   = "target_unauthorized"
	errSourceGone     = "source_unavailable"
	errUploadFailed   = "upload_failed"
	errVerifyFailed   = "verify_failed"
	errJobExhausted   = "job_exhausted"
	uploadConcurrency = 2
	maxManualBatch    = 100
)

var (
	ErrTargetNotFound     = auth.ErrNotFound
	ErrNoEnabledTarget    = fault.New(422, "archive_target_required", "请选择启用的归档目标")
	ErrSegmentNotArchived = auth.ErrNotFound
)

// Target is the operator-facing view: the password never leaves the service.
type Target struct {
	ID                 id.ID           `json:"id"`
	Name               string          `json:"name"`
	BaseURL            string          `json:"base_url"`
	RemoteRoot         string          `json:"remote_root"`
	Username           string          `json:"username"`
	CloudRetentionDays int             `json:"cloud_retention_days"`
	Enabled            bool            `json:"enabled"`
	LastCheck          json.RawMessage `json:"last_check"`
	CreatedAt          time.Time       `json:"created_at"`
}

type TargetInput struct {
	Name               string
	BaseURL            string
	RemoteRoot         string
	Username           string
	Password           string
	CloudRetentionDays int
	Enabled            bool
}

const targetColumns = `t.id,t.name,t.base_url,t.remote_root,t.username,t.cloud_retention_days,t.enabled,t.last_check,t.created_at`

func scanTarget(row pgx.Row) (Target, error) {
	var out Target
	err := row.Scan(&out.ID, &out.Name, &out.BaseURL, &out.RemoteRoot, &out.Username, &out.CloudRetentionDays, &out.Enabled, &out.LastCheck, &out.CreatedAt)
	return out, err
}

// Service owns targets and uploads. Secrets carries the credential cipher; the
// storage service opens the local source files.
type Service struct {
	DB      *database.DB
	Auth    *auth.Service
	Secrets secrets.State
	Pools   *storage.Service
	siteID  id.ID
}

func New(db *database.DB, accounts *auth.Service, secret secrets.State, pools *storage.Service) *Service {
	return &Service{DB: db, Auth: accounts, Secrets: secret, Pools: pools, siteID: secret.SiteID}
}

func validRoot(root string) bool {
	return len(root) > 1 && strings.HasPrefix(root, "/") && !strings.HasPrefix(root, "//") && root != "/"
}

// ListTargets returns every configured target, newest first. Admin only: the
// credentials may be encrypted, the endpoints and account names are still
// site infrastructure.
func (s *Service) ListTargets(ctx context.Context, p auth.Principal) ([]Target, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT `+targetColumns+` FROM archive_targets t ORDER BY t.created_at DESC,t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Target, 0)
	for rows.Next() {
		item, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// RegisterTarget stores one target with its sealed password.
func (s *Service) RegisterTarget(ctx context.Context, p auth.Principal, in TargetInput) (Target, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return Target{}, err
	}
	if err := validateTargetInput(in); err != nil {
		return Target{}, err
	}
	// The credential AAD binds the target id, so the id is drawn first.
	targetID, err := id.New()
	if err != nil {
		return Target{}, err
	}
	sealed, err := s.Secrets.SealCredential(s.siteID, targetID, targetID, "password", []byte(in.Password))
	if err != nil {
		return Target{}, err
	}
	var out Target
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO archive_targets(id,site_id,name,base_url,remote_root,username,credential_key_id,password_nonce,password_ciphertext,cloud_retention_days,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			targetID, s.siteID, in.Name, in.BaseURL, in.RemoteRoot, in.Username, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, in.CloudRetentionDays, in.Enabled); err != nil {
			return err
		}
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "archive.target_registered", ObjectID: targetID, Details: details(in.Name)}); err != nil {
			return err
		}
		out, err = scanTarget(tx.QueryRow(ctx, `SELECT `+targetColumns+` FROM archive_targets t WHERE t.id=$1`, targetID))
		return err
	})
	return out, err
}

// UpdateTarget rewrites the connection settings. An empty password keeps the
// stored one, so editing a name never forces re-entering credentials.
func (s *Service) UpdateTarget(ctx context.Context, p auth.Principal, targetID id.ID, in TargetInput) (Target, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return Target{}, err
	}
	if err := validateTargetInput(in); err != nil {
		return Target{}, err
	}
	var out Target
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM archive_targets WHERE id=$1)`, targetID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return auth.ErrNotFound
		}
		if in.Password == "" {
			if _, err := tx.Exec(ctx, `UPDATE archive_targets SET name=$2,base_url=$3,remote_root=$4,username=$5,cloud_retention_days=$6,enabled=$7 WHERE id=$1`,
				targetID, in.Name, in.BaseURL, in.RemoteRoot, in.Username, in.CloudRetentionDays, in.Enabled); err != nil {
				return err
			}
		} else {
			sealed, err := s.Secrets.SealCredential(s.siteID, targetID, targetID, "password", []byte(in.Password))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE archive_targets SET name=$2,base_url=$3,remote_root=$4,username=$5,credential_key_id=$6,password_nonce=$7,password_ciphertext=$8,cloud_retention_days=$9,enabled=$10 WHERE id=$1`,
				targetID, in.Name, in.BaseURL, in.RemoteRoot, in.Username, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, in.CloudRetentionDays, in.Enabled); err != nil {
				return err
			}
		}
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "archive.target_updated", ObjectID: targetID, Details: details(in.Name)}); err != nil {
			return err
		}
		var scanErr error
		out, scanErr = scanTarget(tx.QueryRow(ctx, `SELECT `+targetColumns+` FROM archive_targets t WHERE t.id=$1`, targetID))
		return scanErr
	})
	return out, err
}

// DeleteTarget removes a target only when nothing references it; a target with
// copies keeps its identity (ARC-06), so its remote objects stay addressable.
func (s *Service) DeleteTarget(ctx context.Context, p auth.Principal, targetID id.ID) error {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return err
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var references int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies WHERE target_id=$1`, targetID).Scan(&references); err != nil {
			return err
		}
		if references > 0 {
			return fault.New(409, "archive_target_referenced", "该目标已有归档副本，只能停用不能删除")
		}
		tag, err := tx.Exec(ctx, `DELETE FROM archive_targets WHERE id=$1`, targetID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrNotFound
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "archive.target_deleted", ObjectID: targetID})
	})
}

// TestTarget probes the stored (or, on create, the submitted) credentials and
// records the verified capabilities.
func (s *Service) TestTarget(ctx context.Context, p auth.Principal, targetID id.ID, draft *TargetInput) (Capabilities, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return Capabilities{}, err
	}
	client, err := s.clientFor(ctx, targetID, draft)
	if err != nil {
		return Capabilities{}, err
	}
	caps, err := client.Check(ctx)
	// A draft probe has no row to record against; only stored targets persist.
	if targetID != "" {
		state := "healthy"
		if err != nil {
			state = "unavailable"
		}
		if _, err := s.DB.Pool.Exec(ctx, `UPDATE archive_targets SET last_check=$2 WHERE id=$1`, targetID, checkJSON(state, caps)); err != nil {
			return Capabilities{}, err
		}
	}
	return caps, err
}

func checkJSON(state string, caps Capabilities) []byte {
	raw, _ := json.Marshal(map[string]any{"state": state, "capabilities": caps, "observed_at": time.Now().UTC()})
	return raw
}

// clientFor builds a client from a stored target or, when a draft is given,
// from the submitted settings (so a target can be tested before registering).
func (s *Service) clientFor(ctx context.Context, targetID id.ID, draft *TargetInput) (*Client, error) {
	if draft != nil {
		return NewClient(draft.BaseURL, draft.Username, draft.Password)
	}
	var baseURL, username, keyID string
	var nonce, ciphertext []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT base_url,username,credential_key_id,password_nonce,password_ciphertext FROM archive_targets WHERE id=$1`, targetID).
		Scan(&baseURL, &username, &keyID, &nonce, &ciphertext)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auth.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.Secrets.OpenCredential(s.siteID, targetID, targetID, "password", secrets.EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		return nil, ErrTargetUnavailable
	}
	return NewClient(baseURL, username, string(plain))
}

func validateTargetInput(in TargetInput) error {
	if len(in.Name) < 1 || len(in.Name) > 64 {
		return auth.ErrInvalid
	}
	if _, err := NewClient(in.BaseURL, in.Username, in.Password); err != nil && in.Password == "" {
		// An update may keep the stored password; the URL and account still
		// have to be structurally valid.
		if _, urlErr := NewClient(in.BaseURL, in.Username, "x"); urlErr != nil {
			return auth.ErrInvalid
		}
	}
	if !validRoot(in.RemoteRoot) {
		return auth.ErrInvalid
	}
	if len(in.Username) < 1 || len(in.Username) > 128 || in.CloudRetentionDays < 1 {
		return auth.ErrInvalid
	}
	return nil
}

func details(name string) []byte {
	// The audit sanitizer forbids URI-shaped values, so the endpoint lives only
	// in the targets table; the audit names the target and nothing else.
	raw, _ := json.Marshal(map[string]any{"name": name})
	return raw
}

// CreateUploads pins one upload task per ready segment. The archive grant is
// checked per row exactly like the batch policy endpoint: a rejected row never
// blocks an accepted one.
func (s *Service) CreateUploads(ctx context.Context, p auth.Principal, targetID id.ID, segmentIDs []id.ID) ([]id.ID, error) {
	if len(segmentIDs) == 0 || len(segmentIDs) > maxManualBatch {
		return nil, auth.ErrInvalid
	}
	// PRD ARC-08: the upload permission is granted per channel, not by role
	// alone, so every row passes the channel gate below.
	created := make([]id.ID, 0, len(segmentIDs))
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM archive_targets WHERE id=$1`, targetID).Scan(&enabled); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if !enabled {
			return ErrNoEnabledTarget
		}
		created = created[:0]
		for _, segmentID := range segmentIDs {
			var channelID, poolID, target, state string
			var size int64
			var revision id.ID
			if err := tx.QueryRow(ctx, `SELECT s.channel_id,s.source_revision_id,s.pool_id,s.target_relative_path,s.state,s.size_bytes FROM recording_segments s WHERE s.id=$1`, segmentID).
				Scan(&channelID, &revision, &poolID, &target, &state, &size); err != nil {
				_ = poolID
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return err
			}
			if err := s.Auth.RequireChannelTx(ctx, p, id.ID(channelID), auth.Archive, tx); err != nil {
				if errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrNotFound) {
					continue
				}
				return err
			}
			if state != "ready" {
				continue
			}
			var channelNo int
			if err := tx.QueryRow(ctx, `SELECT channel_no FROM channels WHERE id=$1`, channelID).Scan(&channelNo); err != nil {
				return err
			}
			enqueued, err := s.enqueueUpload(ctx, tx, targetID, segmentID, uploadSource{
				ChannelID: id.ID(channelID), ChannelNo: channelNo, Target: target, State: state, Size: size,
			})
			if err != nil {
				return err
			}
			if enqueued {
				created = append(created, segmentID)
			}
		}
		detailsRaw, _ := json.Marshal(map[string]any{"target_id": targetID, "queued": len(created)})
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "archive.uploads_requested", ObjectID: targetID, Details: detailsRaw})
	})
	return created, err
}

// HandleUpload executes one upload job: stream the local file to the target,
// hash it on the way out, then verify the remote object's length. A crash
// re-runs cleanly — the PUT is idempotent and the copy row is single.
func (s *Service) HandleUpload(ctx context.Context, lease jobs.Lease) (jobs.Result, error) {
	var payload struct {
		CopyID id.ID `json:"copy_id"`
	}
	if err := json.Unmarshal(lease.Payload, &payload); err != nil || payload.CopyID == "" {
		return jobs.Result{}, &jobs.PermanentFailure{Code: "invalid_payload"}
	}
	var segmentID, targetID, objectKey, state, digest string
	var copyID id.ID
	var size int64
	err := s.DB.Pool.QueryRow(ctx, `SELECT id,segment_id,target_id,object_key,state,sha256,size_bytes FROM recording_archive_copies WHERE id=$1`, payload.CopyID).
		Scan(&copyID, &segmentID, &targetID, &objectKey, &state, &digest, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobs.Result{}, &jobs.PermanentFailure{Code: "copy_missing"}
	}
	if err != nil {
		return jobs.Result{}, err
	}
	if state != "uploading" {
		return jobs.Result{Payload: lease.Payload}, nil
	}
	var baseURL, username, keyID string
	var nonce, ciphertext []byte
	var enabled bool
	if err := s.DB.Pool.QueryRow(ctx, `SELECT base_url,username,credential_key_id,password_nonce,password_ciphertext,enabled FROM archive_targets WHERE id=$1`, targetID).
		Scan(&baseURL, &username, &keyID, &nonce, &ciphertext, &enabled); err != nil {
		return jobs.Result{}, err
	}
	if !enabled {
		return jobs.Result{}, s.failCopy(ctx, copyID, errTargetGone)
	}
	targetRef := id.ID(targetID)
	plain, err := s.Secrets.OpenCredential(s.siteID, targetRef, targetRef, "password", secrets.EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		return jobs.Result{}, s.failCopy(ctx, copyID, errTargetDenied)
	}
	client, err := NewClient(baseURL, username, string(plain))
	if err != nil {
		return jobs.Result{}, s.failCopy(ctx, copyID, errTargetDenied)
	}
	var poolID id.ID
	var target string
	var stored int64
	var segmentProtected bool
	var cloudExpires *time.Time
	if err := s.DB.Pool.QueryRow(ctx, `SELECT s.pool_id,s.target_relative_path,s.size_bytes,s.protected,(SELECT c.cloud_expires_at FROM recording_archive_copies c WHERE c.id=$2) FROM recording_segments s WHERE s.id=$1`, segmentID, copyID).
		Scan(&poolID, &target, &stored, &segmentProtected, &cloudExpires); err != nil {
		return jobs.Result{}, err
	}
	// ARC-09: a source whose cloud deadline passed while it sat queued is
	// skipped, never uploaded late — unless it is protected evidence.
	if cloudExpires != nil && time.Now().After(*cloudExpires) && !segmentProtected {
		if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET state='expired',error_code='archive_expired_skip' WHERE id=$1 AND state='uploading'`, copyID); err != nil {
			return jobs.Result{}, err
		}
		return jobs.Result{}, &jobs.PermanentFailure{Code: "archive_expired_skip"}
	}
	root, _, err := s.Pools.OpenMediaRoot(ctx, poolID)
	if err != nil {
		return jobs.Result{}, err
	}
	defer root.Close()
	file, err := storage.OpenMediaFile(root, target)
	if err != nil {
		return jobs.Result{}, s.failCopy(ctx, copyID, errSourceGone)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != stored {
		return jobs.Result{}, s.failCopy(ctx, copyID, errSourceGone)
	}
	hasher := sha256.New()
	if err := client.Put(ctx, objectKey, io.TeeReader(file, hasher), stored); err != nil {
		if errors.Is(err, ErrTargetDenied) {
			return jobs.Result{}, s.failCopy(ctx, copyID, errTargetDenied)
		}
		return jobs.Result{}, s.failRetry(ctx, copyID, errUploadFailed)
	}
	// ARC-02 verification floor before anything counts as archived: the object
	// exists and its length matches what was streamed.
	body, header, status, err := client.GetRange(ctx, objectKey, "bytes=0-0")
	if err != nil {
		return jobs.Result{}, s.failRetry(ctx, copyID, errVerifyFailed)
	}
	body.Close()
	total := int64(-1)
	if status == http.StatusPartialContent {
		if parsed, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(header.Get("Content-Range"), "bytes 0-0/"), ""), 10, 64); err == nil {
			total = parsed
		}
	} else if status == http.StatusOK {
		if parsed, err := strconv.ParseInt(header.Get("Content-Length"), 10, 64); err == nil {
			total = parsed
		}
	}
	if total != stored {
		return jobs.Result{}, s.failRetry(ctx, copyID, errVerifyFailed)
	}
	final := hex.EncodeToString(hasher.Sum(nil))
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET state='verified',sha256=$2,uploaded_at=clock_timestamp(),verified_at=clock_timestamp(),error_code=NULL WHERE id=$1 AND state='uploading'`, copyID, final); err != nil {
		return jobs.Result{}, err
	}
	return jobs.Result{Payload: lease.Payload}, nil
}

func (s *Service) failCopy(ctx context.Context, copyID id.ID, code string) error {
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET state='failed',error_code=$2 WHERE id=$1 AND state='uploading'`, copyID, code); err != nil {
		return err
	}
	return &jobs.PermanentFailure{Code: code}
}

// failRetry keeps the copy uploading (the job will retry with backoff) and
// records why, so the drawer can show a live reason without lying about state.
func (s *Service) failRetry(ctx context.Context, copyID id.ID, code string) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET error_code=$2 WHERE id=$1 AND state='uploading'`, copyID, code)
	return err
}

// uploadSource is the slice of segment state the upload kernel needs.
type uploadSource struct {
	ChannelID id.ID
	ChannelNo int
	Target    string
	State     string
	Size      int64
}

// enqueueUpload creates the copy row and the job for one segment against one
// target. Manual requests, the immediate policy hook and the scheduled sweep
// all funnel through here, so the object key and the idempotency key have
// exactly one definition (ARC-02/03).
func (s *Service) enqueueUpload(ctx context.Context, tx pgx.Tx, targetID, segmentID id.ID, src uploadSource) (bool, error) {
	if src.State != "ready" {
		return false, nil
	}
	objectKey := path.Join(string(s.siteID), src.Target)
	copyID, err := id.New()
	if err != nil {
		return false, err
	}
	// The hash is computed during upload; zeros keep the NOT NULL/CHECK honest
	// until then, and the row's presence is what guards deletion mid-flight.
	if _, err := tx.Exec(ctx, `INSERT INTO recording_archive_copies(id,segment_id,target_id,object_key,state,size_bytes,sha256) VALUES($1,$2,$3,$4,'uploading',$5,$6) ON CONFLICT (segment_id,target_id) DO NOTHING`,
		copyID, segmentID, targetID, objectKey, src.Size, strings.Repeat("0", 64)); err != nil {
		return false, err
	}
	var actual id.ID
	if err := tx.QueryRow(ctx, `SELECT id FROM recording_archive_copies WHERE segment_id=$1 AND target_id=$2`, segmentID, targetID).Scan(&actual); err != nil {
		return false, err
	}
	payload, err := json.Marshal(map[string]any{"copy_id": actual})
	if err != nil {
		return false, err
	}
	if _, err := jobs.Enqueue(ctx, tx, jobs.Input{
		Kind:           "recording.archive_upload",
		ObjectID:       actual,
		IdempotencyKey: string(segmentID) + ":" + string(targetID),
		Payload:        payload,
	}); err != nil {
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
