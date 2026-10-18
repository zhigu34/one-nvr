package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
)

// errEventUnmapped marks a payload about a camera this site does not map to a
// channel. It is a recorded outcome, not a failure: the caller treats it as
// handled.
var errEventUnmapped = errors.New("event camera is not mapped to a channel")

// upsertInput is one event's facts, from either source: the MQTT stream (the
// fast path) or a reconciliation read of the detector's API (the backstop that
// repairs a message the stream lost). Both go through the same write so the two
// paths cannot disagree about what an event looks like.
type upsertInput struct {
	// Camera is the detector's camera name, resolved to a channel here.
	Camera        string
	SourceID      string
	Category      string
	Score         float64
	Zones         []string
	StartedAt     time.Time
	EndedAt       *time.Time
	SnapshotState string
	Raw           []byte
}

// eventUpsert writes one event and evaluates the rules that match it.
//
// It is idempotent on the upstream id and monotonic: a later message may fill
// in an end time, a higher score or a snapshot, but never reopens a closed
// event, moves an end time backwards, or downgrades a ready snapshot (AI-05).
func (s *Service) eventUpsert(ctx context.Context, in upsertInput) error {
	if in.Zones == nil {
		// nil marshals to JSON null, and the upsert's jsonb_array_length()
		// rejects a scalar — so an event outside every zone would fail to store
		// instead of being recorded as "no zones".
		in.Zones = []string{}
	}
	if in.Raw == nil {
		in.Raw = []byte("{}")
	}
	zonesJSON, err := json.Marshal(in.Zones)
	if err != nil {
		return err
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		channelID, revision, err := s.channelForCamera(ctx, tx, in.Camera, in.SourceID)
		if err != nil {
			return err
		}
		eventID, err := id.New()
		if err != nil {
			return err
		}
		// Only a finished event has a retention deadline. An open one is held
		// for as long as it is open (EVT-07: activity defers cleanup), and the
		// reconciliation sweep is what eventually closes an event whose closing
		// message never arrived — after which this deadline applies normally.
		expires := any(nil)
		if in.EndedAt != nil {
			expires = in.EndedAt.Add(EventIndexRetention)
		}
		// The insert is the "new" path; the update path only ever moves the
		// event forward (ended_at monotonically, snapshot pending→ready).
		_, err = tx.Exec(ctx, `INSERT INTO events(id,channel_id,source_revision_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,expires_at,raw)
			VALUES($1,$2,$3,'frigate',$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (source,source_id) DO UPDATE SET
				score=GREATEST(events.score,EXCLUDED.score),
				zones=CASE WHEN jsonb_array_length(EXCLUDED.zones)>0 THEN EXCLUDED.zones ELSE events.zones END,
				ended_at=COALESCE(EXCLUDED.ended_at,events.ended_at),
				snapshot_state=CASE WHEN EXCLUDED.snapshot_state='ready' THEN 'ready' ELSE events.snapshot_state END,
				expires_at=COALESCE(EXCLUDED.expires_at,events.expires_at),
				updated_at=clock_timestamp()
			WHERE events.ended_at IS NULL OR EXCLUDED.ended_at IS NULL OR EXCLUDED.ended_at>=events.ended_at`,
			eventID, channelID, revision, in.SourceID, in.Category, in.Score, zonesJSON, in.StartedAt, in.EndedAt, in.SnapshotState, expires, in.Raw)
		if err != nil {
			return err
		}
		// Rules are evaluated in the same transaction: an event and the fact
		// that it tripped a rule commit together, so a crash cannot leave a
		// flagged event without its hit or the reverse. Running this on a
		// repaired event is safe — the hit fence on (event_id, rule_id) and the
		// notification fence on hit_id make a second pass a no-op — and it means
		// one write path rather than two that can drift.
		stored, err := scanEvent(tx.QueryRow(ctx, `SELECT `+eventColumns+` FROM events e WHERE e.source=$1 AND e.source_id=$2`, "frigate", in.SourceID))
		if err != nil {
			return err
		}
		// Link the footage in the same transaction as the event itself. The
		// segment query is a single indexed range read, and doing it here means
		// an event is never briefly stored as "no footage" when the footage was
		// already on disk — a window that the page would render as a failure.
		//
		// A channel with no ordinary recording simply finds nothing and stays
		// unavailable; the buffer half of REC-09 is what will fill that gap, and
		// until it exists "unavailable" is the honest answer.
		if stored.ChannelID != nil {
			link, linkErr := s.linkEventMedia(ctx, tx, *stored.ChannelID, stored.StartedAt, stored.EndedAt)
			if linkErr != nil {
				return linkErr
			}
			if err := s.writeMediaLink(ctx, tx, stored.ID, link); err != nil {
				return err
			}
			stored.MediaState = link.State
			stored.MediaStartAt = link.Start
			stored.MediaEndAt = link.End
			stored.MediaSegments = link.Segments
		}
		return s.evaluateRules(ctx, tx, stored)
	})
}

// channelForCamera resolves the detector's camera name to a channel.
//
// When the mapping is gone — the operator removed the camera from Frigate after
// the event was stored — the event's own channel is used instead. Falling back
// to the stored value keeps an existing event updatable: without it, a
// reconciliation write would be dropped for a reason that has nothing to do
// with the event, leaving it open forever.
func (s *Service) channelForCamera(ctx context.Context, tx pgx.Tx, camera, sourceID string) (id.ID, *id.ID, error) {
	var channelID id.ID
	var revision *id.ID
	err := tx.QueryRow(ctx, `SELECT c.id,c.current_revision_id FROM frigate_cameras f JOIN channels c ON c.id=f.channel_id WHERE f.frigate_name=$1`, camera).Scan(&channelID, &revision)
	if err == nil {
		return channelID, revision, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", nil, err
	}
	var storedChannel, storedRevision *id.ID
	if qErr := tx.QueryRow(ctx, `SELECT channel_id,source_revision_id FROM events WHERE source='frigate' AND source_id=$1`, sourceID).Scan(&storedChannel, &storedRevision); qErr == nil && storedChannel != nil {
		return *storedChannel, storedRevision, nil
	}
	// An unmapped camera with no stored event is dropped with a record, not
	// silently merged into another channel (CH-07/CH-08). It is also logged:
	// without a line here, "events arrive but are dropped" is indistinguishable
	// from "no events arrive" when reading the worker log.
	slog.Warn("event dropped: camera has no channel mapping", "camera", camera, "source_id", sourceID)
	if err := s.recordUnmapped(ctx, tx, camera, sourceID); err != nil {
		return "", nil, err
	}
	return "", nil, errEventUnmapped
}
