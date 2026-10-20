package recording

// PRD REC-09: running the pre-event buffer for a channel.
//
// The buffer is deliberately NOT an ordinary recording run:
//
//   - it writes into the tmpfs at /cache, never into a storage pool;
//   - it is never published, indexed or archived;
//   - its fragments are deleted by this process, not by ZLM.
//
// Sharing Start() would have meant threading a purpose through pool admission,
// write evidence, naming rules and the publication path — all of which exist to
// guarantee that bytes on a pool are trustworthy recordings. None of them apply
// to a three-second fragment that will be gone in half a minute.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

// BufferDefaults are the numbers this batch ships with, chosen from the live
// measurement rather than from a guess:
//
//   - FragmentSeconds 3: the camera's GOP is 3.3 s, so a smaller request cannot
//     produce smaller files. Asking for the GOP keeps fragments whole.
//   - KeepSeconds 120: long enough to hold the tail of a real event, not just the
//     lead-in plus latency.
//
// The first version kept 30 seconds, reasoning from the lead-in window alone.
// That is the wrong question: the ring is also the ONLY source for the rest of a
// long event, because a clip is assembled from fragments held in the event's own
// parts directory and those can only be copied while they are still in the ring.
// Measured on the live site: of 879 daily events, 191 (22%) ran longer than 30
// seconds and 43 longer than two minutes, and a 216-second clip ended up with 78
// seconds of its span backed by no fragment at all.
//
// 120 seconds covers the 90th percentile (68s) with room for the trail, at about
// four times the memory: measured 71 MB for five buffering channels at 30s, so
// roughly 280 MB at 120s — inside a 512 MB cache with headroom.
const (
	BufferFragmentSeconds = 3
	BufferKeepSeconds     = 120
)

// ErrBufferUnavailable reports that this deployment reserved no memory for the
// event buffer. It is an answer, not a failure: the channel still records
// events and snapshots, just without pre-event footage.
var ErrBufferUnavailable = errors.New("event_buffer_unavailable")

// bufferPath is where one channel's ring lives inside the tmpfs.
//
// The channel id rather than the channel number: a channel keeps its id across a
// camera swap (REC-01), so a renamed or re-ordered channel cannot inherit another
// channel's buffered footage.
func bufferPath(root string, channelID id.ID) string {
	return filepath.Join(root, "events", string(channelID))
}

// EnsureBuffer starts the ring recording for a channel if it is not already
// running, and returns whether a buffer is now active.
//
// Idempotent on purpose: the scheduler calls this every tick while the channel
// qualifies, so a running buffer must be left alone rather than restarted (a
// restart empties the ring, which is exactly the footage a detection needs).
func (s *Service) EnsureBuffer(ctx context.Context, e *channel.Execution, revision id.ID) (bool, error) {
	if s.EventCacheDir == "" {
		return false, ErrBufferUnavailable
	}
	ss, err := s.currentMain(ctx, e, &revision)
	if err != nil {
		return false, err
	}
	var running bool
	if err := s.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM event_buffers WHERE channel_id=$1 AND heartbeat_at>clock_timestamp()-make_interval(secs => $2))`,
		e.ChannelID, BufferStaleAfter.Seconds()).Scan(&running); err != nil {
		return false, err
	}
	if running {
		// The keep window is deployment policy, not part of the recording: a running
		// buffer picks up a changed value without being restarted (a restart empties
		// the ring, which is exactly the footage a detection about to arrive needs).
		if _, err := s.DB.Pool.Exec(ctx, `UPDATE event_buffers SET keep_seconds=$2 WHERE channel_id=$1 AND keep_seconds<>$2`, e.ChannelID, BufferKeepSeconds); err != nil {
			return true, err
		}
		return true, nil
	}
	// A stale row means the worker that claimed it is gone and its tmpfs with it.
	// Clearing it here keeps the channel from reporting "already buffering" with
	// nothing writing.
	if _, err := s.DB.Pool.Exec(ctx, `DELETE FROM event_buffers WHERE channel_id=$1`, e.ChannelID); err != nil {
		return false, err
	}
	path := bufferPath(s.EventCacheDir, e.ChannelID)
	if err := ensureBufferDir(path); err != nil {
		return false, err
	}
	// StartRecord treats the directory as the recording root and appends
	// record/app/stream/date itself, so the promise about the ring's shape must
	// account for that nesting.
	if err := s.Media.StartRecord(ctx, ss.Key, path, BufferFragmentSeconds); err != nil {
		return false, err
	}
	runID, err := id.New()
	if err != nil {
		return false, err
	}
	if _, err := s.DB.Pool.Exec(ctx, `INSERT INTO event_buffers(channel_id,run_id,stream_session_id,source_revision_id,cache_path,keep_seconds,owner_token)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(channel_id) DO UPDATE SET run_id=EXCLUDED.run_id,stream_session_id=EXCLUDED.stream_session_id,
			source_revision_id=EXCLUDED.source_revision_id,cache_path=EXCLUDED.cache_path,
			keep_seconds=EXCLUDED.keep_seconds,owner_token=EXCLUDED.owner_token,
			started_at=clock_timestamp(),heartbeat_at=clock_timestamp()`,
		e.ChannelID, runID, ss.ID, revision, path, BufferKeepSeconds, s.bufferOwnerToken()); err != nil {
		return false, err
	}
	slog.Info("event buffer started", "channel", e.ChannelID, "keep_seconds", BufferKeepSeconds)
	return true, nil
}

// StopBuffer ends the ring recording for a channel.
//
// It is called when the channel no longer qualifies — ordinary recording started,
// the operator turned the switch off, or the channel was disabled. Leaving the
// buffer running would keep writing fragments nothing will ever read, which is
// precisely the cost REC-10 says an off switch must avoid.
func (s *Service) StopBuffer(ctx context.Context, channelID id.ID) error {
	var path string
	var revision id.ID
	err := s.DB.Pool.QueryRow(ctx, `DELETE FROM event_buffers WHERE channel_id=$1 RETURNING cache_path,source_revision_id`, channelID).Scan(&path, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// The stream key comes from the session, not from the cache path: ZLM records
	// per stream, and asking it to stop a key we reconstructed from a directory
	// name would stop nothing.
	ss, err := s.currentMain(ctx, &channel.Execution{ChannelID: channelID}, &revision)
	if err == nil {
		if err := s.Media.StopRecord(ctx, ss.Key); err != nil && !errors.Is(err, zlm.ErrProxyAbsent) {
			slog.Warn("event buffer stop failed", "channel", channelID, "error", err)
		}
	}
	if err := removeBufferDir(path); err != nil {
		slog.Warn("event buffer cleanup failed", "channel", channelID, "error", err)
	}
	slog.Info("event buffer stopped", "channel", channelID)
	return nil
}

// EventBufferActive reports whether this channel is inside a live buffer.
//
// Exported for the integration tests: a stale row and a running buffer look
// identical in the table, and the difference is the only thing that keeps a
// channel from reporting "already buffering" with nothing writing.
func (s *Service) EventBufferActive(ctx context.Context, channelID id.ID) (bool, error) {
	var active bool
	err := s.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM event_buffers WHERE channel_id=$1 AND heartbeat_at>clock_timestamp()-make_interval(secs => $2))`,
		channelID, BufferStaleAfter.Seconds()).Scan(&active)
	return active, err
}

// ensureBufferDir creates the ring directory.
//
// 0777 rather than 0755: the media server runs as a different user from the
// worker on the live deployment (root versus uid 10001), and the mount itself is
// sticky. A private directory here would make the recorder fail with EACCES on a
// path it was just told to write.
func ensureBufferDir(path string) error {
	return os.MkdirAll(path, 0o777)
}

// removeBufferDir deletes a ring after the buffer stops, so a channel that is no
// longer buffering leaves nothing behind for the next one to inherit.
func removeBufferDir(path string) error {
	if path == "" {
		return nil
	}
	return os.RemoveAll(path)
}

// BufferKeepSecondsFor exposes the keep window for the settings surface.
func BufferKeepSecondsFor() time.Duration {
	return time.Duration(BufferKeepSeconds) * time.Second
}

// bufferUnavailableCode maps a buffer failure to a stable code for the log and
// the observation trail. The operator needs to distinguish "this deployment
// reserved no memory" from "the media server refused the call": the first is a
// deployment decision, the second is a fault to investigate.
func bufferUnavailableCode(err error) string {
	if errors.Is(err, ErrBufferUnavailable) {
		return "event_buffer_not_configured"
	}
	return "event_buffer_unavailable"
}
