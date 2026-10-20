package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
		return NewClient(draft.BaseURL, draft.RemoteRoot, draft.Username, draft.Password)
	}
	var baseURL, remoteRoot, username, keyID string
	var nonce, ciphertext []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT base_url,remote_root,username,credential_key_id,password_nonce,password_ciphertext FROM archive_targets WHERE id=$1`, targetID).
		Scan(&baseURL, &remoteRoot, &username, &keyID, &nonce, &ciphertext)
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
	return NewClient(baseURL, remoteRoot, username, string(plain))
}

func validateTargetInput(in TargetInput) error {
	if len(in.Name) < 1 || len(in.Name) > 64 {
		return auth.ErrInvalid
	}
	if _, err := NewClient(in.BaseURL, in.RemoteRoot, in.Username, in.Password); err != nil && in.Password == "" {
		// An update may keep the stored password; the URL and account still
		// have to be structurally valid.
		if _, urlErr := NewClient(in.BaseURL, in.RemoteRoot, in.Username, "x"); urlErr != nil {
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
	var baseURL, remoteRoot, username, keyID string
	var nonce, ciphertext []byte
	var enabled bool
	if err := s.DB.Pool.QueryRow(ctx, `SELECT base_url,remote_root,username,credential_key_id,password_nonce,password_ciphertext,enabled FROM archive_targets WHERE id=$1`, targetID).
		Scan(&baseURL, &remoteRoot, &username, &keyID, &nonce, &ciphertext, &enabled); err != nil {
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
	client, err := NewClient(baseURL, remoteRoot, username, string(plain))
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

// failRetry keeps the copy uploading and makes the job retry with backoff.
//
// The error must be non-nil: returning nil would let jobs.Execute record a
// completed job while the copy still says 'uploading', and nothing would ever
// run it again — a transient network failure would park the footage in a state
// that reads as progress forever.
func (s *Service) failRetry(ctx context.Context, copyID id.ID, code string) error {
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET error_code=$2 WHERE id=$1 AND state='uploading'`, copyID, code); err != nil {
		return err
	}
	return fmt.Errorf("archive upload retryable: %s", code)
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

// RetryFailures re-queues every copy of one target that is stopped but not
// finished, and reports how many it revived.
//
// Without this a permanently failed copy is a dead end: the job that carried it
// reached a terminal state, and re-selecting the segment only re-reads the same
// dead job (the idempotency key is unchanged), so the page could report
// "queued" while nothing was ever going to run. Failures that were worth fixing
// — a target that was down, credentials that were wrong — therefore needed a
// database edit to recover.
//
// Copies still in flight are left alone: the job is queued or running, and
// resetting it would duplicate an upload that is already moving bytes.
func (s *Service) RetryFailures(ctx context.Context, p auth.Principal, targetID id.ID) (int, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return 0, err
	}
	revived := 0
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.id,c.segment_id,c.state
			FROM recording_archive_copies c
			LEFT JOIN jobs j ON j.kind='recording.archive_upload'
				AND j.idempotency_key = c.segment_id::text || ':' || c.target_id::text
			WHERE c.target_id=$1 AND (c.state='failed' OR c.state='uploading')
			AND (j.id IS NULL OR j.state NOT IN ('queued','running'))`, targetID)
		if err != nil {
			return err
		}
		type stuck struct {
			CopyID, SegmentID id.ID
			State             string
		}
		var candidates []stuck
		for rows.Next() {
			var item stuck
			if err := rows.Scan(&item.CopyID, &item.SegmentID, &item.State); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range candidates {
			// A failed copy is revived in place: its object key and size are still
			// right, and ARC-02's single row per (segment,target) is what later
			// uploads and expiry cleanup key on.
			if _, err := tx.Exec(ctx, `UPDATE recording_archive_copies SET state='uploading',error_code=NULL WHERE id=$1`, item.CopyID); err != nil {
				return err
			}
			payload, err := json.Marshal(map[string]any{"copy_id": item.CopyID})
			if err != nil {
				return err
			}
			// Attempts restart at zero: the retry budget belongs to one attempt
			// sequence, and the operator asking again is a new sequence.
			if _, err := tx.Exec(ctx, `UPDATE jobs SET state='queued',attempt=0,available_at=clock_timestamp(),
				error_code=NULL,lease_expires_at=NULL,fencing_token=NULL,updated_at=clock_timestamp()
				WHERE kind='recording.archive_upload' AND idempotency_key=$1`, string(item.SegmentID)+":"+string(targetID)); err != nil {
				return err
			}
			if _, err := jobs.Enqueue(ctx, tx, jobs.Input{
				Kind:           "recording.archive_upload",
				ObjectID:       item.CopyID,
				IdempotencyKey: string(item.SegmentID) + ":" + string(targetID),
				Payload:        payload,
			}); err != nil && !errors.Is(err, jobs.ErrIdempotencyConflict) {
				return err
			}
			revived++
		}
		if revived == 0 {
			return nil
		}
		detailsRaw, _ := json.Marshal(map[string]any{"target_id": targetID, "retried": revived})
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "archive.failures_retried", ObjectID: targetID, Details: detailsRaw})
	})
	if err != nil {
		return 0, err
	}
	return revived, nil
}

// TargetStatus is what one target is actually doing.
//
// The target list says how a target is configured; this says whether the archive
// is working. Without it an operator has to query the copies table by hand to learn
// that (for example) every upload is failing, and the page looks identical whether
// the archive is idle, healthy, or broken.
type TargetStatus struct {
	TargetID id.ID `json:"target_id"`
	// Verified, Pending and Failed count COPIES, not segments: one segment archived
	// twice appears once per target, which is what the per-target rows answer.
	Verified int `json:"verified"`
	// Uploading counts copies whose job is genuinely queued or running.
	// Stopped counts copies still marked 'uploading' whose job has given up —
	// the job's retry budget ran out. They are reported separately because
	// they look identical in the copies table but mean opposite things on the
	// page: one is progress, the other is work that will never resume without
	// an operator.
	Uploading int `json:"uploading"`
	Stopped   int `json:"stopped"`
	Failed    int `json:"failed"`
	// VerifiedBytes is the size of the copies that are confirmed present.
	VerifiedBytes int64 `json:"verified_bytes"`
	// OldestPendingAt is when the least recently queued copy was enqueued; null
	// when nothing is waiting. It answers "is the archive keeping up".
	OldestPendingAt *time.Time `json:"oldest_pending_at"`
	// LastError is the most recent failure's code, so the page can name the reason
	// instead of only counting failures.
	LastError string `json:"last_error"`
}

// TargetStatuses reports the archive state per target.
func (s *Service) TargetStatuses(ctx context.Context, p auth.Principal) ([]TargetStatus, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	// The job state, not the copy state, decides whether an upload is moving:
	// failRetry leaves the copy 'uploading' while the job backs off, and a job
	// that exhausts its attempts leaves that same row behind with nothing left
	// to run it.
	rows, err := s.DB.Pool.Query(ctx, `WITH copies AS (
			SELECT c.id,c.target_id,c.state,c.size_bytes,c.created_at,c.error_code,
				EXISTS(SELECT 1 FROM jobs j WHERE j.kind='recording.archive_upload'
					AND j.idempotency_key = c.segment_id::text || ':' || c.target_id::text
					AND j.state IN ('queued','running')) AS live
			FROM recording_archive_copies c
		)
		SELECT t.id,
			count(*) FILTER (WHERE k.state='verified'),
			count(*) FILTER (WHERE k.state='uploading' AND k.live),
			count(*) FILTER (WHERE k.state='uploading' AND NOT k.live),
			count(*) FILTER (WHERE k.state='failed'),
			coalesce(sum(k.size_bytes) FILTER (WHERE k.state='verified'),0),
			(array_agg(k.created_at ORDER BY k.created_at) FILTER (WHERE k.state='uploading' AND k.live))[1],
			coalesce((array_agg(coalesce(k.error_code,'') ORDER BY k.created_at DESC) FILTER (WHERE k.state='failed' OR (k.state='uploading' AND NOT k.live)))[1],'')
		FROM archive_targets t
		LEFT JOIN copies k ON k.target_id=t.id
		GROUP BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]TargetStatus, 0)
	for rows.Next() {
		var item TargetStatus
		if err := rows.Scan(&item.TargetID, &item.Verified, &item.Uploading, &item.Stopped, &item.Failed, &item.VerifiedBytes, &item.OldestPendingAt, &item.LastError); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ChannelArchiveStatus is one archiving channel's binding plus what has happened
// to its footage.
type ChannelArchiveStatus struct {
	ChannelID id.ID  `json:"channel_id"`
	ChannelNo int    `json:"channel_no"`
	Name      string `json:"name"`
	TargetID  id.ID  `json:"target_id"`
	Target    string `json:"target_name"`
	// TargetEnabled matters on its own: a disabled target still leaves the binding
	// in place, and the footage then falls back to the site's recording retention.
	TargetEnabled bool   `json:"target_enabled"`
	Mode          string `json:"mode"`
	// Segments and Copied count the channel's ready segments against the copies
	// that reached this target, so "is this channel actually archived" is one
	// subtraction rather than two queries.
	Segments int `json:"segments"`
	Copied   int `json:"copied"`
	Failed   int `json:"failed"`
	// LocalRetentionHours is the window after which a verified copy lets the local
	// file go. Zero means the archive window is disabled and the site's recording
	// retention governs instead.
	LocalRetentionHours int `json:"local_retention_hours"`
}

// ChannelArchiveStatuses reports every channel and its archive binding.
//
// Every channel, not just the bound ones: the page shows a row per channel so
// the table is a complete picture of what is archived and what is not. A channel
// with no policy carries empty target fields and mode "none", which the page
// renders as "未归档" rather than omitting the row.
func (s *Service) ChannelArchiveStatuses(ctx context.Context, p auth.Principal) ([]ChannelArchiveStatus, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	// LEFT JOIN, so an unbound channel keeps its row. The per-target counts are
	// correlated subqueries: they run once per channel rather than multiplying
	// rows the way a join onto the copies table would.
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.channel_no,c.channel_name,
			coalesce(ap.target_id, '00000000-0000-0000-0000-000000000000'::uuid),
			coalesce(t.name,''), coalesce(t.enabled,false),
			coalesce(ap.mode,'none'),
			coalesce((SELECT count(*) FROM recording_segments s WHERE s.channel_id=c.id AND s.state='ready'),0),
			coalesce((SELECT count(*) FROM recording_archive_copies ac JOIN recording_segments s ON s.id=ac.segment_id
				WHERE s.channel_id=c.id AND s.state='ready' AND ac.target_id=ap.target_id AND ac.state='verified'),0),
			coalesce((SELECT count(*) FROM recording_archive_copies ac JOIN recording_segments s ON s.id=ac.segment_id
				WHERE s.channel_id=c.id AND ac.target_id=ap.target_id AND ac.state='failed'),0),
			(SELECT archive_local_retention_hours FROM sites WHERE id=c.site_id)
		FROM channels c
		LEFT JOIN archive_policies ap ON ap.channel_id=c.id
		LEFT JOIN archive_targets t ON t.id=ap.target_id
		ORDER BY c.channel_no`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ChannelArchiveStatus, 0)
	for rows.Next() {
		var item ChannelArchiveStatus
		if err := rows.Scan(&item.ChannelID, &item.ChannelNo, &item.Name, &item.TargetID, &item.Target,
			&item.TargetEnabled, &item.Mode, &item.Segments, &item.Copied, &item.Failed, &item.LocalRetentionHours); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
