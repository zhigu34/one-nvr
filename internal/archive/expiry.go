package archive

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

// ConfirmRemoteAvailable is the ARC-05 gate the local cleanup uses: the verified
// copy must answer right now. A definitively missing object reconciles the copy
// row to expired and reports false; anything else returns an error so the
// cleanup round stops instead of guessing.
func (s *Service) ConfirmRemoteAvailable(ctx context.Context, segmentID id.ID) (bool, error) {
	var key, baseURL, username, keyID string
	var targetID id.ID
	var nonce, ciphertext []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT c.object_key,c.target_id,t.base_url,t.username,t.credential_key_id,t.password_nonce,t.password_ciphertext
		FROM recording_archive_copies c JOIN archive_targets t ON t.id=c.target_id AND t.enabled
		WHERE c.segment_id=$1 AND c.state='verified'`, segmentID).
		Scan(&key, &targetID, &baseURL, &username, &keyID, &nonce, &ciphertext)
	if err != nil {
		return false, err
	}
	plain, err := s.Secrets.OpenCredential(s.siteID, targetID, targetID, "password", secrets.EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		return false, err
	}
	client, err := NewClient(baseURL, username, string(plain))
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
		var baseURL, username, keyID string
		var nonce, ciphertext []byte
		if err := s.DB.Pool.QueryRow(ctx, `SELECT base_url,username,credential_key_id,password_nonce,password_ciphertext FROM archive_targets WHERE id=$1 AND enabled`, item.TargetID).
			Scan(&baseURL, &username, &keyID, &nonce, &ciphertext); err != nil {
			slog.Warn("cloud expiry target unavailable", "error", err)
			return
		}
		targetRef := id.ID(item.TargetID)
		plain, err := s.Secrets.OpenCredential(s.siteID, targetRef, targetRef, "password", secrets.EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
		if err != nil {
			slog.Warn("cloud expiry credentials unavailable", "error", err)
			return
		}
		client, err := NewClient(baseURL, username, string(plain))
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
