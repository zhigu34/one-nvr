package events

// PRD REC-09 / REC-10: what does this event have to watch?
//
// Two different things share the word "event recording", and keeping them apart
// is what makes the feature honest:
//
//   - When the channel has ordinary recording (continuous, or a schedule window,
//     or a manual window), the footage already exists. The event's job is to
//     *link* to it — record which segments cover the event, so the page can jump
//     to the right second instead of guessing at "started_at minus ten seconds".
//   - When the channel has no ordinary recording, the event needs a buffer to
//     produce footage at all. That is the other half of the batch and it needs
//     the rolling ZLM cache; this file only answers the first question, and it
//     answers "no footage" truthfully until that cache exists.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
)

// DefaultEventMediaWindow is how far before an event starts and after it ends
// the footage should reach, when the site has no override.
//
// These used to be the ONLY definition, duplicated as recording.PreEventLead /
// PostEventTrail with identical values and no shared source. The real values now
// live on the site row (event_clip_lead_seconds / event_clip_trail_seconds,
// migration 0050) and are read by eventMediaWindow; this pair is the fallback for
// a service without a site (rendering-only construction).
const (
	PreEventWindow  = 5 * time.Second
	PostEventWindow = 10 * time.Second
)

// eventMediaWindow reads the site's lead and trail for this channel.
//
// Both places that ask "how far around an event does footage reach" must read the
// same numbers: queueClipJob decides what to record, and linkEventMedia decides
// whether what exists counts as covering the event. Leaving the second on the old
// constants meant shortening the trail made every clip read as "only covers part
// of the event" — the recording shrank exactly as asked and the verdict called it
// a shortfall.
func eventMediaWindow(ctx context.Context, tx pgx.Tx, channelID id.ID) (lead, trail time.Duration) {
	var leadSeconds, trailSeconds int
	err := tx.QueryRow(ctx, `SELECT s.event_clip_lead_seconds, s.event_clip_trail_seconds
		FROM channels c JOIN sites s ON s.id=c.site_id WHERE c.id=$1`, channelID).Scan(&leadSeconds, &trailSeconds)
	if err != nil {
		// A channel without a site row cannot exist, but a caller in a test fixture
		// might not have created one; fall back to the defaults rather than losing
		// the recording or the coverage verdict.
		if ctx.Err() == nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("event clip timing unavailable, using defaults", "error", err)
		}
		return PreEventWindow, PostEventWindow
	}
	return time.Duration(leadSeconds) * time.Second, time.Duration(trailSeconds) * time.Second
}

// mediaLink is the outcome of looking for footage around an event.
type mediaLink struct {
	State string
	// Start and End are the *available* span, not the requested one: a segment
	// boundary rarely lands exactly where the window asks, so the page must
	// know what it can actually play rather than assuming the whole window.
	Start *time.Time
	End   *time.Time
	// Segments counts the distinct segments covering the window, for the UI's
	// "3 段录像" hint and for noticing a window split by a source switch.
	Segments int
}

// linkEventMedia finds the ordinary recordings covering an event's window.
//
// The three states are deliberately distinct:
//
//   - linked  — footage covers the whole requested window;
//   - partial — footage covers part of it. Reporting this as linked would put a
//     play button on a range that ends early; reporting it as unavailable would
//     hide footage that does exist. REC-09 forbids both.
//   - unavailable — nothing covers the window.
func (s *Service) linkEventMedia(ctx context.Context, tx pgx.Tx, channelID id.ID, startedAt time.Time, endedAt *time.Time) (mediaLink, error) {
	lead, trail := eventMediaWindow(ctx, tx, channelID)
	from := startedAt.Add(-lead)
	// An event still running has no end yet. Ask for the window it would have
	// if it stopped now, and let a later update re-evaluate: a link computed
	// against a moving end would go stale the moment the event grew.
	to := startedAt.Add(trail)
	if endedAt != nil {
		to = endedAt.Add(trail)
	}
	// A growing event clip is 'finalizing' until the event ends, and it is still
	// footage that exists: the page plays it while it grows. Counting only 'ready'
	// made every MQTT update (~1/sec) overwrite the verdict with 'unavailable'
	// while the assembler wrote 'linked' every five seconds — the card flickered
	// between 本机无录像 and 录像生成中 for the whole life of a running event,
	// which is exactly the case event recording exists for.
	rows, err := tx.Query(ctx, `SELECT start_at,end_at FROM recording_segments
		WHERE channel_id=$1 AND state IN ('ready','finalizing') AND start_at<$3 AND end_at>$2
		ORDER BY start_at`, channelID, from.UTC(), to.UTC())
	if err != nil {
		return mediaLink{}, err
	}
	defer rows.Close()
	var (
		covered int64
		count   int
		first   time.Time
		last    time.Time
	)
	for rows.Next() {
		var start, end time.Time
		if err := rows.Scan(&start, &end); err != nil {
			return mediaLink{}, err
		}
		if count == 0 {
			first = start
		}
		last = end
		count++
		// Segments can touch or overlap at a source switch; counting each
		// segment's full length would claim more coverage than exists.
		overlapStart := start
		if overlapStart.Before(from) {
			overlapStart = from
		}
		overlapEnd := end
		if overlapEnd.After(to) {
			overlapEnd = to
		}
		if overlapEnd.After(overlapStart) {
			covered += int64(overlapEnd.Sub(overlapStart))
		}
	}
	if err := rows.Err(); err != nil {
		return mediaLink{}, err
	}
	if count == 0 {
		return mediaLink{State: "unavailable"}, nil
	}
	link := mediaLink{State: "partial", Start: &first, End: &last, Segments: count}
	// Compare against the requested span, not the event's own duration: the
	// point of the window is the lead-in and lead-out, and a clip that starts
	// at the event and ends at the event is precisely what "no lead-in" means.
	if covered >= int64(to.Sub(from)) {
		link.State = "linked"
	}
	return link, nil
}

// writeMediaLink stores the verdict on the event row.
//
// It is a separate update from the upsert because the verdict depends on
// segments that may be published *after* the event was first stored: an event
// arriving mid-segment is stored before its footage is published, and the
// reconcile pass links it once the segment lands. Keeping this idempotent means
// either path can call it at any time.
func (s *Service) writeMediaLink(ctx context.Context, tx pgx.Tx, eventID id.ID, link mediaLink) error {
	_, err := tx.Exec(ctx, `UPDATE events SET media_state=$2,media_start_at=$3,media_end_at=$4,media_segments=$5,updated_at=clock_timestamp() WHERE id=$1`,
		eventID, link.State, link.Start, link.End, link.Segments)
	return err
}

// ReconcileEventMedia re-evaluates events whose footage had not been published
// when the event was stored.
//
// The event arrives while the segment containing it is still being written, so
// the upsert finds nothing and records "unavailable". A few minutes later the
// segment is published and the answer changes. Without this pass the event keeps
// the verdict it happened to get at ingestion, and the page tells the operator
// there is no footage while it sits on disk.
//
// It is bounded: an event whose footage never appears (the channel was not
// recording) would otherwise be re-examined forever. After the cutoff the
// verdict stands and the page shows the honest "本机无录像".
func (s *Service) ReconcileEventMedia(ctx context.Context) {
	if s.DB == nil {
		return
	}
	// Two kinds of pending share this column and need different treatment:
	//
	//   - recent: the segment covering the event has not been published yet.
	//     Leave it pending and look again next pass.
	//   - old: footage will never appear (the channel was not recording, or it
	//     was deleted by retention). Only a positive answer used to be written,
	//     so these rows stayed pending forever and the page said "录像待归档"
	//     about events from a week ago.
	//
	// The old ones are settled below rather than filtered out here, so one query
	// keeps both cases in view.
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,channel_id,started_at,ended_at,
			started_at < clock_timestamp() - interval '2 hours' AS settled
		FROM events
		WHERE media_state='pending' AND channel_id IS NOT NULL
		ORDER BY (started_at < clock_timestamp() - interval '2 hours') DESC, started_at DESC
		LIMIT 200`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("event media reconciliation unavailable", "error", err)
		}
		return
	}
	type candidate struct {
		id        id.ID
		channelID id.ID
		startedAt time.Time
		endedAt   *time.Time
		settled   bool
	}
	var pending []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.channelID, &item.startedAt, &item.endedAt, &item.settled); err != nil {
			rows.Close()
			return
		}
		pending = append(pending, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	for _, item := range pending {
		if ctx.Err() != nil {
			return
		}
		err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
			link, err := s.linkEventMedia(ctx, tx, item.channelID, item.startedAt, item.endedAt)
			if err != nil {
				return err
			}
			// Nothing yet is not a verdict for a recent event: leave it pending
			// so the next pass looks again. For an event older than the cutoff
			// the answer is final — no segment is coming — and leaving it
			// pending would tell the operator to wait for footage that will
			// never arrive.
			if link.State == "unavailable" {
				if !item.settled {
					return nil
				}
				return s.writeMediaLink(ctx, tx, item.id, link)
			}
			return s.writeMediaLink(ctx, tx, item.id, link)
		})
		if err != nil && ctx.Err() == nil {
			slog.Warn("event media link failed", "event", item.id, "error", err)
		}
	}
}

// channelBuffering reports whether a worker is currently keeping a ring for this
// channel, using the same staleness rule the scheduler does.
//
// The buffer heartbeat is what makes this reliable: a row whose owner stopped
// beating describes a tmpfs that died with that worker, so trusting it would queue
// work against footage that no longer exists.
func (s *Service) channelBuffering(ctx context.Context, tx pgx.Tx, channelID id.ID) bool {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM event_buffers WHERE channel_id=$1 AND heartbeat_at>clock_timestamp()-interval '2 minutes')`, channelID).Scan(&active)
	if err != nil {
		// A read failure must not silently drop a clip: assuming a buffer exists
		// costs one row the assembler will resolve; assuming it does not lose the
		// footage entirely.
		return true
	}
	return active
}

// queueClipJob attaches this event to a recording window. REC-09.
//
// The rule, and the whole of it: a recording is planned to close at
// `event end + trail`; an event arriving BEFORE that close moves the close out to
// its own `end + trail`, repeatedly. The recording is the subject and each event
// is an anchor on it, because the detector ends an event while the subject is
// still walking out and starts another when it sees them again — one visit is one
// recording, not several files with unwatched time between them.
//
// The buffer records continuously, so extending a window adds real footage; this
// never claims a gap was covered.
//
// Written in SQL here rather than by calling recording.AttachEvent: events must
// not import recording (the dependency runs recording → events, and the reverse
// would create a cycle the moment the buffer needs to read an event). The two
// implementations answer the same question; the integration test
// TestOneActivityStretchProducesOneClip exercises the recording side, and
// TestTwoDetectionsOnOneVisitShareARecording exercises this one.
func (s *Service) queueClipJob(ctx context.Context, tx pgx.Tx, eventID, channelID id.ID, startedAt time.Time, endedAt *time.Time) error {
	lead, trail := eventMediaWindow(ctx, tx, channelID)

	openAt := startedAt.Add(-lead)
	closeAt := startedAt.Add(trail)
	if endedAt != nil {
		closeAt = endedAt.Add(trail)
	} else {
		// A running event's close is measured from now: waiting for the detector to
		// close it would leave the window stalled at the first fragments.
		now := time.Now().UTC()
		if now.Add(trail).After(closeAt) {
			closeAt = now.Add(trail)
		}
	}

	// Serialise attach per channel before looking for an open window. A row lock
	// cannot do this job: on a channel whose last window has closed there is no row
	// to lock, so two detections arriving within the same instant both find nothing
	// and both INSERT. Measured on the live site on the first day of this model:
	// two windows sharing a window_start, one carrying five anchors and one
	// carrying none — the same seconds of footage cut into two files.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('clip_window:' || $1::text, 170022))`, channelID); err != nil {
		return err
	}
	// The newest window on this channel that has not closed yet.
	var windowID id.ID
	var currentEnd time.Time
	err := tx.QueryRow(ctx, `SELECT id, window_end FROM clip_windows
		WHERE channel_id=$1 AND state IN ('open','finalizing')
		ORDER BY window_end DESC, id LIMIT 1 FOR UPDATE`, channelID).Scan(&windowID, &currentEnd)
	switch {
	case err == nil && currentEnd.After(time.Now().UTC()):
		// Still inside its countdown: extend. window_start never moves — the head
		// of this recording is what was already recorded.
		if _, err := tx.Exec(ctx, `UPDATE clip_windows
			SET window_end=GREATEST(window_end,$2), state='open', updated_at=clock_timestamp()
			WHERE id=$1`, windowID, closeAt.UTC()); err != nil {
			return err
		}
	case err == nil || errors.Is(err, pgx.ErrNoRows):
		// No open window, or the newest one has already passed its close: a new
		// recording starts here, and the old one is left for the assembler.
		newID, idErr := id.New()
		if idErr != nil {
			return idErr
		}
		if _, err := tx.Exec(ctx, `INSERT INTO clip_windows(id,channel_id,window_start,window_end,state)
			VALUES($1,$2,$3,$4,'open')`, newID, channelID, openAt.UTC(), closeAt.UTC()); err != nil {
			return err
		}
		windowID = newID
	default:
		return err
	}

	// One row per event keeps the detector's repeated messages idempotent. The job
	// row is the anchor: which recording does this detection belong to. The
	// assembly columns are kept in step for the audit trail; the assembler reads
	// clip_windows.
	_, err = tx.Exec(ctx, `INSERT INTO event_clip_jobs(event_id,channel_id,window_start,window_end,state,window_id)
		VALUES($1,$2,$3,$4,'queued',$5)
		ON CONFLICT(event_id) DO UPDATE SET window_id=EXCLUDED.window_id,
			window_start=EXCLUDED.window_start, window_end=EXCLUDED.window_end,
			updated_at=clock_timestamp()`,
		eventID, channelID, openAt.UTC(), closeAt.UTC(), windowID)
	return err
}
