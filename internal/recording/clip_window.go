package recording

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
)

// ClipTiming is how long a recording runs before an event and after its end.
type ClipTiming struct {
	Lead  time.Duration
	Trail time.Duration
}

// DefaultClipTiming reproduces the values that used to be duplicated as Go
// constants (events.PreEventWindow/PostEventWindow and
// recording.PreEventLead/PostEventTrail). It is used when the site row cannot be
// read — a rendering-only service has no site, and a missing setting must not
// stop a recording.
var DefaultClipTiming = ClipTiming{Lead: 5 * time.Second, Trail: 10 * time.Second}

// LoadClipTiming reads the site's lead and trail. The values are read on every
// window creation and extension, so a change applies from the next event; a
// window already open keeps the close it was given.
func (s *Service) LoadClipTiming(ctx context.Context, channelID id.ID) ClipTiming {
	if s.DB == nil || s.DB.Pool == nil {
		return DefaultClipTiming
	}
	var lead, trail int
	err := s.DB.Pool.QueryRow(ctx, `SELECT s.event_clip_lead_seconds, s.event_clip_trail_seconds
		FROM channels c JOIN sites s ON s.id=c.site_id WHERE c.id=$1`, channelID).Scan(&lead, &trail)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("clip timing unavailable, using defaults", "error", err)
		}
		return DefaultClipTiming
	}
	return ClipTiming{Lead: time.Duration(lead) * time.Second, Trail: time.Duration(trail) * time.Second}
}

// AttachEvent decides which recording window an event belongs to, and returns
// that window's id plus whether an existing window was extended.
//
// This is the whole of REC-09's grouping rule, and it is deliberately one
// question: a recording closes at `event end + trail`; an event arriving BEFORE
// that close moves the close out to its own `end + trail`, and so on. The
// recording is the subject — events are anchors on it — because the detector
// ends an event while the subject is still walking out and starts another when
// it sees them again. One visit to a driveway is one recording with several
// anchors, not several recordings with unwatched time between them.
//
// The buffer records continuously, so extending a window adds real footage. That
// is why this is safe: nothing here claims a gap was covered.
func (s *Service) AttachEvent(ctx context.Context, channelID, eventID id.ID, startedAt time.Time, endedAt *time.Time) (id.ID, bool, error) {
	timing := s.LoadClipTiming(ctx, channelID)
	// The instant the close would land if this event were the last one. A running
	// event has no end yet, so its close is measured from now: waiting for the
	// detector to close it would let the window stall at the first fragment.
	closeAt := startedAt.Add(timing.Trail)
	if endedAt != nil {
		closeAt = endedAt.Add(timing.Trail)
	} else {
		now := time.Now().UTC()
		if now.Add(timing.Trail).After(closeAt) {
			closeAt = now.Add(timing.Trail)
		}
	}
	openAt := startedAt.Add(-timing.Lead)

	var windowID id.ID
	extended := false
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		// Serialise attach per channel. `FOR UPDATE` below cannot do it: it locks
		// rows that exist, and the whole point of this query is that on a quiet
		// channel there is no row yet. Two detections arriving together therefore
		// both found nothing and both INSERTed — measured on the live site as two
		// windows with the same window_start, one carrying the anchors and one
		// carrying none, so the same second of footage was cut into two files.
		if err := lockChannelClipWindow(ctx, tx, channelID); err != nil {
			return err
		}
		// The newest window that has not closed yet. Locked so a concurrent
		// extension of the same row waits rather than reading a stale close.
		var currentEnd time.Time
		err := tx.QueryRow(ctx, `SELECT id, window_end FROM clip_windows
			WHERE channel_id=$1 AND state IN ('open','finalizing')
			ORDER BY window_end DESC, id LIMIT 1 FOR UPDATE`, channelID).Scan(&windowID, &currentEnd)
		switch {
		case err == nil && currentEnd.After(time.Now().UTC()):
			// Still inside its countdown: extend it. window_start never moves —
			// the head of this recording is whatever was already recorded.
			if _, err := tx.Exec(ctx, `UPDATE clip_windows
				SET window_end=GREATEST(window_end,$2), state='open', updated_at=clock_timestamp()
				WHERE id=$1`, windowID, closeAt.UTC()); err != nil {
				return err
			}
			extended = true
		case errors.Is(err, pgx.ErrNoRows), err == nil:
			// No open window, or the newest one has already passed its close: a
			// new recording starts here. The old window is left for the assembler
			// to finish and publish.
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			windowID = ""
			newID, err := id.New()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO clip_windows(id,channel_id,window_start,window_end,state)
				VALUES($1,$2,$3,$4,'open')`, newID, channelID, openAt.UTC(), closeAt.UTC()); err != nil {
				return err
			}
			windowID = newID
		default:
			return err
		}
		// One row per event keeps the detector's repeated messages idempotent. The
		// job's own window bounds are kept in step for the audit trail; the
		// assembler reads clip_windows.
		_, err = tx.Exec(ctx, `INSERT INTO event_clip_jobs(event_id,channel_id,window_start,window_end,state,window_id)
			VALUES($1,$2,$3,$4,'queued',$5)
			ON CONFLICT(event_id) DO UPDATE SET window_id=EXCLUDED.window_id,
				window_start=EXCLUDED.window_start, window_end=EXCLUDED.window_end,
				updated_at=clock_timestamp()`,
			eventID, channelID, openAt.UTC(), closeAt.UTC(), windowID)
		return err
	})
	if err != nil {
		return "", false, err
	}
	return windowID, extended, nil
}

// QueueClipWindow is AttachEvent for callers that already know the timing
// question they are asking (tests, and the reconciliation path which replays a
// stored event). It returns the window the event landed in.
func (s *Service) QueueClipWindow(ctx context.Context, eventID, channelID id.ID, startedAt time.Time, endedAt *time.Time) error {
	_, _, err := s.AttachEvent(ctx, channelID, eventID, startedAt, endedAt)
	return err
}

// lockChannelClipWindow takes the per-channel advisory lock that makes
// "create the window if none is open" safe under concurrency.
//
// A plain row lock cannot express this: on a channel whose last window has
// closed there is no row to lock, so two detections arriving within the same
// instant both see nothing and both create a window. That happened on the live
// site on the first day — two windows sharing a window_start, one with five
// anchors and one with none, the same seconds of footage cut into two files.
//
// The key is derived from the channel id so every path that creates a window for
// this channel serialises on the same value.
func lockChannelClipWindow(ctx context.Context, tx pgx.Tx, channelID id.ID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('clip_window:' || $1::text, 170022))`, channelID)
	return err
}
