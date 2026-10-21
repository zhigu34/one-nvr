package archive

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

// ConfirmRemoteAvailable is the ARC-05 gate the local cleanup uses: the verified
// copy must answer right now. A definitively missing object reconciles the copy
// row to expired and reports false; anything else returns an error so the
// cleanup round stops instead of guessing.
func (s *Service) ConfirmRemoteAvailable(ctx context.Context, segmentID id.ID) (bool, error) {
	var key, baseURL, remoteRoot, username, keyID string
	var targetID id.ID
	var nonce, ciphertext []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT c.object_key,c.target_id,t.base_url,t.remote_root,t.username,t.credential_key_id,t.password_nonce,t.password_ciphertext
		FROM recording_archive_copies c JOIN archive_targets t ON t.id=c.target_id AND t.enabled
		WHERE c.segment_id=$1 AND c.state='verified'`, segmentID).
		Scan(&key, &targetID, &baseURL, &remoteRoot, &username, &keyID, &nonce, &ciphertext)
	if err != nil {
		return false, err
	}
	plain, err := s.Secrets.OpenCredential(s.siteID, targetID, targetID, "password", secrets.EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		return false, err
	}
	client, err := NewClient(baseURL, remoteRoot, username, string(plain))
	if err != nil {
		return false, err
	}
	body, _, status, err := client.GetRange(ctx, key, "bytes=0-0")
	if err != nil {
		return false, err
	}
	body.Close()
	if status == 404 {
		s.expireCopy(ctx, segmentID)
		return false, nil
	}
	return status/100 == 2, nil
}

// SweepCloudExpiry removes remote objects past their cloud deadline (ARC-09).
// Protection and an active playback lease both keep the copy; the deadline
// derives from the segment's real end, never from the upload moment.
func (s *Service) SweepCloudExpiry(ctx context.Context) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.segment_id,c.target_id,c.object_key FROM recording_archive_copies c
		JOIN recording_segments s ON s.id=c.segment_id
		JOIN archive_targets t ON t.id=c.target_id AND t.enabled
		WHERE c.state='verified' AND c.cloud_expires_at<clock_timestamp()
		AND NOT s.protected
		AND NOT EXISTS(SELECT 1 FROM recording_playback_leases l WHERE l.segment_id=c.segment_id AND l.lease_expires_at>clock_timestamp())
		LIMIT 100`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("cloud expiry scan unavailable")
		}
		return
	}
	type dueCopy struct {
		ID, SegmentID, TargetID id.ID
		Key                     string
	}
	var batch []dueCopy
	for rows.Next() {
		var item dueCopy
		if err := rows.Scan(&item.ID, &item.SegmentID, &item.TargetID, &item.Key); err != nil {
			rows.Close()
			return
		}
		batch = append(batch, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(batch) == 0 {
		return
	}
	for _, item := range batch {
		if ctx.Err() != nil {
			return
		}
		var baseURL, remoteRoot, username, keyID string
		var nonce, ciphertext []byte
		if err := s.DB.Pool.QueryRow(ctx, `SELECT base_url,remote_root,username,credential_key_id,password_nonce,password_ciphertext FROM archive_targets WHERE id=$1 AND enabled`, item.TargetID).
			Scan(&baseURL, &remoteRoot, &username, &keyID, &nonce, &ciphertext); err != nil {
			slog.Warn("cloud expiry target unavailable", "error", err)
			return
		}
		targetRef := id.ID(item.TargetID)
		plain, err := s.Secrets.OpenCredential(s.siteID, targetRef, targetRef, "password", secrets.EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
		if err != nil {
			slog.Warn("cloud expiry credentials unavailable", "error", err)
			return
		}
		client, err := NewClient(baseURL, remoteRoot, username, string(plain))
		if err != nil {
			slog.Warn("cloud expiry client unavailable", "error", err)
			return
		}
		// A missing object is already the desired state; only a proven delete
		// lets the index row follow. An unreachable target keeps the object
		// flagged by its deadline and the next hourly pass retries.
		if _, err := client.Delete(ctx, item.Key); err != nil {
			slog.Warn("cloud expiry delete failed", "error", err)
			return
		}
		if err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE recording_archive_copies SET state='expired',error_code='cloud_expired' WHERE id=$1 AND state='verified'`, item.ID); err != nil {
				return err
			}
			details, _ := json.Marshal(map[string]any{"object_key": item.Key})
			return audit.Append(ctx, tx, audit.Entry{Action: "archive.copy_expired", ObjectID: item.SegmentID, Details: details})
		}); err != nil {
			slog.Warn("cloud expiry reconcile failed", "error", err)
			return
		}
	}
	if len(batch) == 100 {
		// A full batch means more is waiting; the next hourly pass drains it.
		return
	}
	_ = time.Now
}

// ErrNoCloudCopy is returned when a recording has no verified remote object to
// delete. It is a 404 rather than a 409: the operator asked to remove a copy that
// is not there, and the desired state already holds.
var ErrNoCloudCopy = fault.New(404, "cloud_copy_absent", "这段录像没有云端副本")

// DeleteCloudCopy removes one recording's verified remote object.
//
// This is the manual counterpart to SweepCloudExpiry: the sweep deletes what has
// reached its deadline, and an operator may need to delete one sooner — a
// mistaken upload, a mis-aimed target, or footage that must not stay on a cloud
// drive any longer. Without it the only ways to remove a remote object were to
// wait for a deadline that (until migration 0051) was never written, or to purge
// the entire site.
//
// Protection is deliberately NOT consulted. Protection says "retention and
// deletion must not remove this recording's local copy" (FILE-03); the cloud copy
// of the same recording is a second location, and an operator asking to remove it
// is asking about that location. Refusing would make protected footage
// permanently undeletable from the cloud, which is the opposite of what
// protection is for. The audit row records who did it.
//
// The row is only marked expired after the remote delete is proven. An
// unreachable target leaves the row verified, so the copy stays playable and the
// operator can retry — marking it first would claim the bytes are gone while they
// are still there.
func (s *Service) DeleteCloudCopy(ctx context.Context, p auth.Principal, segmentID id.ID) error {
	var key string
	var targetID, channelID id.ID
	var state string
	err := s.DB.Pool.QueryRow(ctx, `SELECT c.object_key,c.target_id,c.state,s.channel_id
		FROM recording_archive_copies c JOIN recording_segments s ON s.id=c.segment_id
		WHERE c.segment_id=$1
		ORDER BY (c.state='verified') DESC, c.created_at DESC LIMIT 1`, segmentID).Scan(&key, &targetID, &state, &channelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoCloudCopy
	}
	if err != nil {
		return err
	}
	if state != "verified" {
		return ErrNoCloudCopy
	}
	// Same authority as archiving: this is a channel-scoped operation on footage,
	// not site infrastructure. A viewer may watch the copy; only someone who may
	// archive this channel may destroy it.
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Archive); err != nil {
		return err
	}
	if _, err := s.RemoveObject(ctx, targetID, key); err != nil {
		return fault.New(503, "cloud_delete_failed", "云端副本删除失败，稍后再试")
	}
	// A missing object is already the desired state, so the boolean needs no
	// branch: either way the row must stop claiming a copy exists.
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE recording_archive_copies SET state='expired',error_code='cloud_deleted_manual'
			WHERE segment_id=$1 AND target_id=$2 AND state='verified'`, segmentID, targetID); err != nil {
			return err
		}
		details, marshalErr := json.Marshal(map[string]any{"object_key": key})
		if marshalErr != nil {
			return marshalErr
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "archive.copy_deleted", ObjectID: segmentID, Details: details})
	})
}
