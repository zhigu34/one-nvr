package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

// Cloud playback errors. A missing verified copy is indistinguishable from a
// missing recording (404); a broken remote read is a distinct, retryable 503
// that never rewrites the archive success history (ARC-06).
var (
	ErrCloudUnavailable = fault.New(503, "cloud_read_failed", "云端副本暂时无法读取")
	ErrLeaseTouched     = errors.New("archive_lease_touched")
)

// CloudSegment is one verified remote copy offered for playback when the local
// file is unavailable. It carries no credentials and no direct URL: the bytes
// are proxied through this service (ARC-04).
type CloudSegment struct {
	SegmentID, ChannelID id.ID
	Start, End           time.Time
	Size                 int64
	Modified             time.Time // copy verification time, feeds the ETag
	key                  string
	client               *Client
	service              *Service
}

// ETag mirrors the local content validator's recipe over the remote copy's own
// identity, so a client that saw the local bytes simply re-reads in full.
func (c *CloudSegment) ETag() string {
	sum := sha256.Sum256([]byte("cloud\x00" + string(c.SegmentID) + "\x00" + strconv.FormatInt(c.Size, 10) + "\x00" + strconv.FormatInt(c.Modified.UnixNano(), 10)))
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// Read streams one byte window of the remote object. start==0 && end<0 means
// the whole object.
func (c *CloudSegment) Read(ctx context.Context, start, end int64) (io.ReadCloser, int, error) {
	rangeHeader := ""
	if end >= 0 {
		rangeHeader = fmt.Sprintf("bytes=%d-%d", start, end)
	}
	body, _, status, err := c.client.GetRange(ctx, c.key, rangeHeader)
	if err != nil {
		if errors.Is(err, ErrObjectMissing) {
			// The remote object is definitively gone (expiry or external delete);
			// the index follows instead of promising a copy that is not there.
			c.service.expireCopy(ctx, c.SegmentID)
			return nil, 0, ErrCloudUnavailable
		}
		return nil, 0, ErrCloudUnavailable
	}
	return body, status, nil
}

// OpenCloudSegment authorizes playback on the segment's channel and resolves
// its verified cloud copy. Without one the answer is auth.ErrNotFound, exactly
// like a recording that never existed.
func (s *Service) OpenCloudSegment(ctx context.Context, p auth.Principal, segmentID id.ID) (*CloudSegment, error) {
	if s.Auth == nil {
		return nil, auth.ErrNotFound
	}
	var out *CloudSegment
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var channelID id.ID
		if err := tx.QueryRow(ctx, `SELECT channel_id FROM recording_segments WHERE id=$1`, segmentID).Scan(&channelID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Playback, tx); err != nil {
			if errors.Is(err, auth.ErrForbidden) {
				return auth.ErrNotFound
			}
			return err
		}
		var copyID, key, targetID string
		var size int64
		var verified time.Time
		if err := tx.QueryRow(ctx, `SELECT id,object_key,target_id,size_bytes,verified_at FROM recording_archive_copies WHERE segment_id=$1 AND state='verified'`, segmentID).
			Scan(&copyID, &key, &targetID, &size, &verified); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		var start, end time.Time
		if err := tx.QueryRow(ctx, `SELECT start_at,end_at FROM recording_segments WHERE id=$1`, segmentID).Scan(&start, &end); err != nil {
			return err
		}
		var baseURL, remoteRoot, username, keyID string
		var nonce, ciphertext []byte
		if err := tx.QueryRow(ctx, `SELECT base_url,remote_root,username,credential_key_id,password_nonce,password_ciphertext FROM archive_targets WHERE id=$1 AND enabled`, targetID).
			Scan(&baseURL, &remoteRoot, &username, &keyID, &nonce, &ciphertext); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		plain, err := s.Secrets.OpenCredential(s.siteID, id.ID(targetID), id.ID(targetID), "password", secrets.EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
		if err != nil {
			return ErrCloudUnavailable
		}
		client, err := NewClient(baseURL, remoteRoot, username, string(plain))
		if err != nil {
			return ErrCloudUnavailable
		}
		out = &CloudSegment{SegmentID: segmentID, ChannelID: channelID, Start: start, End: end, Size: size, Modified: verified, key: key, client: client, service: s}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A cloud playback holds the same cleanup lease as a local one.
	_, err = s.DB.Pool.Exec(ctx, `INSERT INTO recording_playback_leases(segment_id,lease_expires_at) VALUES($1,clock_timestamp()+interval '90 seconds')
		ON CONFLICT (segment_id) DO UPDATE SET lease_expires_at=clock_timestamp()+interval '90 seconds'`, segmentID)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AuditCloudPlayback records an authorized cloud read. Same discipline as the
// local path: no byte leaves before the row exists.
func (s *Service) AuditCloudPlayback(ctx context.Context, p auth.Principal, c *CloudSegment, start, end int64, partial bool) error {
	details, err := json.Marshal(map[string]any{"channel_id": c.ChannelID, "byte_from": start, "byte_to": end, "segment_bytes": c.Size, "partial": partial})
	if err != nil {
		return err
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "recording.cloud_streamed", ObjectID: c.SegmentID, Details: details})
	})
}

// expireCopy reconciles a definitively missing remote object. It does not
// touch the upload history: the copy row moves to expired with its own reason.
func (s *Service) expireCopy(ctx context.Context, segmentID id.ID) {
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET state='expired' WHERE segment_id=$1 AND state='verified'`, segmentID); err != nil && ctx.Err() == nil {
		slog.Warn("archive copy reconcile unavailable", "error", err)
	}
}
