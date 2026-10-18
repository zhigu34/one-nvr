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
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
)

// EventMediaWindow is how far before the event starts and after it ends the
// footage should reach. Five seconds each side: enough to show what walked into
// frame and what happened as it left, without dragging in unrelated minutes.
const (
	PreEventWindow  = 5 * time.Second
	PostEventWindow = 5 * time.Second
)

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
	from := startedAt.Add(-PreEventWindow)
	// An event still running has no end yet. Ask for the window it would have
	// if it stopped now, and let a later update re-evaluate: a link computed
	// against a moving end would go stale the moment the event grew.
	to := startedAt.Add(PostEventWindow)
	if endedAt != nil {
		to = endedAt.Add(PostEventWindow)
	}
	rows, err := tx.Query(ctx, `SELECT start_at,end_at FROM recording_segments
		WHERE channel_id=$1 AND state='ready' AND start_at<$3 AND end_at>$2
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
