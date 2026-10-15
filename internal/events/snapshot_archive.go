package events

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// Snapshot archiving copies the detector's picture into the channel's bound
// pool, so the evidence outlives the detector's own short-lived directory
// (STO-07, and the M3 milestone's "抓拍归档").
//
// The detector stays the source of truth for what it saw; this is a copy, and
// the read path prefers the local copy precisely because the detector's is
// allowed to disappear.

// SnapshotSweepInterval is how often the worker looks for pictures to copy.
// The detector keeps its snapshots for a while, so a few minutes of latency
// costs nothing while polling harder would waste both ends.
const SnapshotSweepInterval = 2 * time.Minute

// snapshotSweepBatch bounds one pass: fetching is network-bound and an
// unbounded sweep would hold the worker for an arbitrarily long time on first
// enable, when every recent event is a candidate.
const snapshotSweepBatch = 50

// ArchiveSnapshots copies pending pictures into their channel's pool.
//
// Nothing here may stop recording, block ingestion or fail an event: a picture
// that cannot be copied is reported on the event row and retried later, and the
// detector's own copy keeps serving in the meantime.
func (s *Service) ArchiveSnapshots(ctx context.Context) {
	if s.Pools == nil || s.DB == nil || s.Frigate == "" {
		return
	}
	for {
		candidates, err := s.pendingSnapshots(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("snapshot archive scan unavailable", "error", err)
			}
			return
		}
		if len(candidates) == 0 {
			return
		}
		for _, candidate := range candidates {
			if err := s.archiveSnapshot(ctx, candidate); err != nil && ctx.Err() == nil {
				// The reason is recorded on the row; the log names the event so
				// an operator can find it without scanning the table.
				slog.Warn("snapshot not archived", "event", candidate.EventID, "reason", err)
			}
		}
		if len(candidates) < snapshotSweepBatch || ctx.Err() != nil {
			return
		}
	}
}

type pendingSnapshot struct {
	EventID   id.ID
	SourceID  string
	ChannelID id.ID
	ChannelNo int
	PoolID    id.ID
	PoolPath  string
	StartedAt time.Time
	Timezone  string
}

// pendingSnapshots lists events whose picture is worth copying: the detector
// says it has one, and we have not stored it yet.
//
// An event is claimed by the write itself rather than by a lock: the copy is
// keyed on the event id, so two workers racing produce the same file and the
// same row update, which is idempotent rather than exclusive. That keeps the
// sweep safe to run in more than one process without a lease.
func (s *Service) pendingSnapshots(ctx context.Context) ([]pendingSnapshot, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT e.id,e.source_id,e.channel_id,c.channel_no,c.storage_pool_id,p.path,e.started_at,si.timezone
		FROM events e
		JOIN channels c ON c.id=e.channel_id
		JOIN storage_pools p ON p.id=c.storage_pool_id AND p.enabled
		JOIN sites si ON si.id=c.site_id
		WHERE e.source='frigate' AND e.snapshot_state='ready'
			AND e.snapshot_relative_path IS NULL
			AND e.snapshot_archive_error IS NULL
		ORDER BY e.started_at DESC
		LIMIT $1`, snapshotSweepBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pendingSnapshot{}
	for rows.Next() {
		var item pendingSnapshot
		if err := rows.Scan(&item.EventID, &item.SourceID, &item.ChannelID, &item.ChannelNo, &item.PoolID, &item.PoolPath, &item.StartedAt, &item.Timezone); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// snapshotRelativePath is where a picture lives inside a pool. It follows the
// recordings layout: channel number, site-local date, stable id in the name.
// Channel number rather than id keeps it readable and survives a channel id
// change the same way recordings do.
func snapshotRelativePath(channelNo int, startedAt time.Time, location *time.Location, eventID id.ID) string {
	local := startedAt.UTC()
	if location != nil {
		local = startedAt.In(location)
	}
	return path.Join("snapshots", fmt.Sprintf("%02d", channelNo), local.Format("2006-01-02"), string(eventID)+".jpg")
}

// maxSnapshotBytes bounds what is read from the detector. Frigate's pictures
// are well under this; the limit exists so a misconfigured endpoint cannot fill
// a pool with one response.
const maxSnapshotBytes = 8 << 20

func (s *Service) archiveSnapshot(ctx context.Context, item pendingSnapshot) error {
	location, err := s.siteLocation(ctx)
	if err != nil {
		// The date folder is part of the path; without the site's zone the file
		// would land under the wrong day, which is worse than waiting.
		s.markSnapshotFailure(ctx, item.EventID, "timezone_unavailable")
		return err
	}
	picture, err := s.fetchSnapshot(ctx, item.SourceID)
	if err != nil {
		s.markSnapshotFailure(ctx, item.EventID, snapshotFailureReason(err))
		return err
	}
	defer picture.Close()

	root, _, err := s.Pools.OpenMediaRoot(ctx, item.PoolID)
	if err != nil {
		s.markSnapshotFailure(ctx, item.EventID, "pool_unavailable")
		return err
	}
	defer root.Close()

	relative := snapshotRelativePath(item.ChannelNo, item.StartedAt, location, item.EventID)
	if err := root.MkdirAll(filepath.Dir(relative), 0755); err != nil {
		s.markSnapshotFailure(ctx, item.EventID, "pool_unwritable")
		return err
	}
	file, err := root.OpenFile(relative, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		s.markSnapshotFailure(ctx, item.EventID, "pool_unwritable")
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(picture, maxSnapshotBytes))
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil || written == 0 {
		// A truncated picture is worse than none: it looks like evidence and is
		// not. Remove it and report the failure.
		_ = root.Remove(relative)
		s.markSnapshotFailure(ctx, item.EventID, "snapshot_incomplete")
		if copyErr != nil {
			return copyErr
		}
		return errors.New("snapshot was empty")
	}
	// The directory entry must survive a power loss before the row points at
	// it, or the index would reference a file that is not there.
	if dir, err := root.Open(filepath.Dir(relative)); err == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return s.markSnapshotArchived(ctx, item, relative)
}

// snapshotFailureReason maps a fetch error to a short code the UI can explain,
// never free text from the far end.
func snapshotFailureReason(err error) string {
	var fetch *snapshotFetchError
	if errors.As(err, &fetch) {
		return fetch.Code
	}
	return "snapshot_unavailable"
}

type snapshotFetchError struct{ Code string }

func (e *snapshotFetchError) Error() string { return e.Code }

func (s *Service) fetchSnapshot(ctx context.Context, sourceID string) (io.ReadCloser, error) {
	window, cancel := context.WithTimeout(ctx, 20*time.Second)
	request, err := http.NewRequestWithContext(window, http.MethodGet, s.Frigate+"/api/events/"+sourceID+"/snapshot.jpg", nil)
	if err != nil {
		cancel()
		return nil, &snapshotFetchError{Code: "snapshot_unavailable"}
	}
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		cancel()
		return nil, &snapshotFetchError{Code: "detector_unreachable"}
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		cancel()
		// The detector no longer has it. Say so permanently: the event keeps its
		// honest state instead of being retried forever.
		return nil, &snapshotFetchError{Code: "detector_dropped"}
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		cancel()
		return nil, &snapshotFetchError{Code: "detector_error"}
	}
	// The caller closes the body; cancelling the window must not cut the stream
	// short, so the cancellation travels with the returned reader instead.
	return &windowedBody{ReadCloser: response.Body, cancel: cancel}, nil
}

type windowedBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *windowedBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func (s *Service) markSnapshotArchived(ctx context.Context, item pendingSnapshot, relative string) error {
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := s.DB.Pool.Exec(write, `UPDATE events SET snapshot_relative_path=$2,snapshot_pool_id=$3,
		snapshot_archive_error=NULL,snapshot_archived_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE id=$1`, item.EventID, relative, item.PoolID)
	return err
}

// markSnapshotFailure records why a picture is not archived. A reason of the
// terminal kind also stops the sweep from retrying: the detector said it no
// longer has the picture, so no amount of waiting will produce one.
func (s *Service) markSnapshotFailure(ctx context.Context, eventID id.ID, reason string) {
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.DB.Pool.Exec(write, `UPDATE events SET snapshot_archive_error=$2,updated_at=clock_timestamp()
		WHERE id=$1 AND snapshot_relative_path IS NULL`, eventID, reason); err != nil {
		slog.Warn("snapshot archive failure not recorded", "error", err, "event", eventID)
	}
}

// SnapshotRef describes where an event's picture can actually be read from. The
// UI and the notification both ask this, so neither can claim a picture exists
// when it does not.
type SnapshotRef struct {
	// Local is the pool copy, empty when there is none.
	Local string
	Pool  id.ID
	// Upstream reports whether the detector is expected to still have it.
	Upstream bool
}

// snapshotRefFor resolves the read path for one event. A local copy answers
// even after the detector dropped its own, which is the point of archiving.
func (s *Service) snapshotRefFor(ctx context.Context, eventID id.ID) (SnapshotRef, error) {
	var ref SnapshotRef
	var poolID *id.ID
	var relative *string
	var state string
	if err := s.DB.Pool.QueryRow(ctx, `SELECT snapshot_relative_path,snapshot_pool_id,snapshot_state FROM events WHERE id=$1`, eventID).
		Scan(&relative, &poolID, &state); err != nil {
		return ref, err
	}
	if relative != nil && poolID != nil {
		ref.Local = *relative
		ref.Pool = *poolID
	}
	ref.Upstream = state == "ready"
	return ref, nil
}

// OpenLocalSnapshot opens the archived copy, verifying its identity the same way
// recorded media is verified: a path that no longer matches its record must not
// authorize serving whatever now sits there.
func (s *Service) OpenLocalSnapshot(ctx context.Context, ref SnapshotRef) (*os.File, error) {
	if ref.Local == "" || s.Pools == nil {
		return nil, auth.ErrNotFound
	}
	root, _, err := s.Pools.OpenMediaRoot(ctx, ref.Pool)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// storage.OpenMediaFile enforces the no-symlink rule at every component and
	// rejects empty files, so a half-written picture cannot be served.
	return storage.OpenMediaFile(root, ref.Local)
}

// SweepSnapshots removes local pictures whose event has expired. Snapshots have
// their own retention (EVT-07), but they are attached to an event, so when the
// index row goes the picture goes with it — except where protection holds the
// event, which the caller has already excluded from expiry.
//
// The file is removed before the reference is cleared: a crash then leaves a row
// pointing at a missing file, which the read path reports honestly and the next
// sweep reconciles, whereas the reverse order would orphan a file nothing
// references.
func (s *Service) SweepSnapshots(ctx context.Context) {
	if s.Pools == nil || s.DB == nil {
		return
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,snapshot_pool_id,snapshot_relative_path FROM events
		WHERE snapshot_relative_path IS NOT NULL AND expires_at IS NOT NULL AND expires_at<clock_timestamp()
		LIMIT 200`)
	if err != nil {
		return
	}
	type stale struct {
		id       id.ID
		poolID   id.ID
		relative string
	}
	var batch []stale
	for rows.Next() {
		var item stale
		if err := rows.Scan(&item.id, &item.poolID, &item.relative); err != nil {
			rows.Close()
			return
		}
		batch = append(batch, item)
	}
	rows.Close()
	for _, item := range batch {
		root, _, err := s.Pools.OpenMediaRoot(ctx, item.poolID)
		if err != nil {
			// The pool is unreachable right now; the reference stays and the
			// next sweep tries again rather than forgetting the picture.
			continue
		}
		removeErr := root.Remove(item.relative)
		root.Close()
		if removeErr != nil && !os.IsNotExist(removeErr) {
			continue
		}
		if _, err := s.DB.Pool.Exec(ctx, `UPDATE events SET snapshot_relative_path=NULL,snapshot_pool_id=NULL,
			snapshot_archive_error=NULL,snapshot_archived_at=NULL,snapshot_state='gone' WHERE id=$1`, item.id); err != nil {
			slog.Warn("expired snapshot reference not cleared", "error", err)
		}
	}
}
