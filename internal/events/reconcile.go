package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// Reconciliation closes the gap MQTT leaves behind.
//
// The event stream is the fast path, and it is the only path this service had:
// when the broker drops an event's closing message — a detector restart, a
// broker blip, a subscriber reconnect — nothing ever fills in its end time. The
// event then stays open forever, its snapshot stays pending, and the archive
// sweep skips it, because a picture is only copied once the detector says it
// exists. What the operator sees is a card with a black tile that never
// resolves.
//
// The detector's own HTTP API still knows the truth, so the worker asks it
// directly for events the stream left open. This is the backstop the product
// needs for "MQTT is best-effort": the stream stays the fast path, and this
// makes the record correct even when it fails.

// ReconcileInterval is how often the worker looks for events the stream left
// open. Long enough that a healthy site does almost nothing; short enough that
// a lost message is corrected while the operator is still looking at the page.
const ReconcileInterval = 5 * time.Minute

// reconcileMinAge keeps the sweep away from events that are legitimately still
// running. A detection lasts seconds; waiting ten minutes means a live event is
// never mistaken for an abandoned one.
const reconcileMinAge = 10 * time.Minute

// reconcileBatch bounds one pass. Each candidate costs one HTTP call to the
// detector, and an unbounded sweep on first enable would hammer it.
const reconcileBatch = 50

// ReconcileOpenEvents asks the detector about the events this index cannot
// finish on its own, and fills in what it reports. Two conditions qualify, and
// they are different failures with the same symptom:
//
//   - the stream never closed the event (a lost end message);
//   - the event closed but its snapshot never arrived.
//
// The second is easy to miss because the event *looks* complete: it has an end
// time and a duration, so nothing downstream waits for it. The detector sends
// `has_snapshot` as it stands when the event ends, then stops reporting on that
// event entirely — so a picture written just after the end is never announced,
// and the row sits at pending forever with no repair path looking at it.
func (s *Service) ReconcileOpenEvents(ctx context.Context) {
	if s.Frigate == "" || s.DB == nil {
		return
	}
	open, err := s.openEvents(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("event reconciliation scan unavailable", "error", err)
		}
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, candidate := range open {
		if ctx.Err() != nil {
			return
		}
		report, err := s.fetchEvent(ctx, client, candidate.SourceID)
		if err != nil {
			// A detector that cannot answer is not evidence that the event is
			// over; leave it alone and try again next pass.
			if !errors.Is(err, errEventGone) {
				slog.Warn("event reconciliation fetch failed", "source_id", candidate.SourceID, "error", err)
			} else {
				s.settleAbandoned(ctx, candidate)
			}
			continue
		}
		if err := s.applyReport(ctx, candidate, report); err != nil && ctx.Err() == nil {
			slog.Warn("event reconciliation write failed", "source_id", candidate.SourceID, "error", err)
		}
		// A closed event whose flag says "no picture" is where the detector's
		// own bookkeeping is known to lag: it stopped reporting on this event at
		// the end, so a picture written just after is never announced. Trust the
		// bytes over the flag — ask for the image itself before concluding
		// anything, because "the flag is stale" and "the picture exists" look
		// identical from here.
		if candidate.Closed && !report.HasSnapshot {
			switch s.probeSnapshot(ctx, client, candidate.SourceID) {
			case snapshotPresent:
				s.markSnapshotReady(ctx, candidate)
			case snapshotAbsent:
				s.settleWithoutSnapshot(ctx, candidate)
			case snapshotUnknown:
				// The detector could not answer about the picture; leave the
				// event pending and ask again next pass rather than declaring a
				// picture missing on no evidence.
			}
		}
	}
}

// openEvent is one event this index cannot finish, with what reconciliation
// needs to ask about it.
type openEvent struct {
	ID       id.ID
	SourceID string
	Started  time.Time
	// Ended is set when the event closed but its snapshot never arrived: the
	// two cases need different reports, because an unclosed event may still be
	// running while a closed one definitely is not.
	Ended  *time.Time
	Closed bool
}

func (s *Service) openEvents(ctx context.Context) ([]openEvent, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,source_id,started_at,ended_at,snapshot_state FROM events
		WHERE source='frigate' AND (
			-- The stream never closed it.
			(ended_at IS NULL AND started_at < clock_timestamp() - $1::interval)
			-- It closed but the picture never arrived. The age test uses the
			-- end, because that is when the detector stopped reporting on it.
			OR (ended_at IS NOT NULL AND ended_at < clock_timestamp() - $1::interval AND snapshot_state='pending')
		)
		ORDER BY started_at DESC LIMIT $2`,
		reconcileMinAge.String(), reconcileBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []openEvent{}
	for rows.Next() {
		var item openEvent
		var snapshotState string
		if err := rows.Scan(&item.ID, &item.SourceID, &item.Started, &item.Ended, &snapshotState); err != nil {
			return nil, err
		}
		item.Closed = item.Ended != nil
		out = append(out, item)
	}
	return out, rows.Err()
}

// eventReport is the detector's answer for one event. Only the fields this
// service stores are read; the detector's response carries more.
//
// The field names match the detector's own API, which uses the same vocabulary
// as its MQTT payload — so both paths feed the same upsert and cannot drift.
type eventReport struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Camera      string   `json:"camera"`
	StartTime   float64  `json:"start_time"`
	EndTime     *float64 `json:"end_time"`
	HasSnapshot bool     `json:"has_snapshot"`
	Zones       []string `json:"zones"`
	Data        struct {
		Score    *float64 `json:"score"`
		TopScore *float64 `json:"top_score"`
		Zones    []string `json:"zones"`
	} `json:"data"`
}

var errEventGone = errors.New("event no longer exists upstream")

func (s *Service) fetchEvent(ctx context.Context, client *http.Client, sourceID string) (eventReport, error) {
	var out eventReport
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Frigate+"/api/events/"+sourceID, nil)
	if err != nil {
		return out, err
	}
	response, err := client.Do(request)
	if err != nil {
		return out, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return out, errEventGone
	}
	if response.StatusCode != http.StatusOK {
		return out, errors.New("detector answered " + response.Status)
	}
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

// applyReport writes what the detector reported. It reuses the same monotonic
// upsert as the stream, so this path cannot open a closed event, move an end
// time backwards, or downgrade a ready snapshot.
func (s *Service) applyReport(ctx context.Context, candidate openEvent, report eventReport) error {
	// A detector that answered with a different id would be answering about
	// something else; using its facts would attach them to the wrong event.
	if report.ID != "" && report.ID != candidate.SourceID {
		return errors.New("detector answered about a different event")
	}
	if report.StartTime <= 0 {
		return errors.New("detector reported no start time")
	}
	if report.Label == "" || report.Camera == "" {
		return errors.New("detector reported an incomplete event")
	}
	started := time.Unix(int64(report.StartTime), 0).UTC()
	// Prefer the id the event already has: reconciliation reports facts about an
	// event, it never re-derives which event that is.
	started = candidate.Started

	zones := report.Data.Zones
	if len(zones) == 0 {
		zones = report.Zones
	}
	if zones == nil {
		zones = []string{}
	}
	score := 0.0
	if report.Data.Score != nil {
		score = *report.Data.Score
	}
	if report.Data.TopScore != nil && *report.Data.TopScore > score {
		score = *report.Data.TopScore
	}
	var endTime *time.Time
	if report.EndTime != nil && *report.EndTime > 0 {
		end := time.Unix(int64(*report.EndTime), 0).UTC()
		endTime = &end
	}
	snapshot := "pending"
	if report.HasSnapshot {
		snapshot = "ready"
	}
	raw, _ := json.Marshal(map[string]any{"type": "reconcile", "label": report.Label, "camera": report.Camera})
	err := s.eventUpsert(ctx, upsertInput{
		Camera:        report.Camera,
		SourceID:      candidate.SourceID,
		Category:      report.Label,
		Score:         score,
		Zones:         zones,
		StartedAt:     started,
		EndedAt:       endTime,
		SnapshotState: snapshot,
		Raw:           raw,
	})
	// A camera the site no longer maps is a recorded outcome, not a failure.
	if errors.Is(err, errEventUnmapped) {
		return nil
	}
	return err
}

// settleAbandoned handles an event the detector no longer knows about at all.
//
// The detector's own retention can drop an event before its closing message
// arrived, or before its snapshot reached us. That is not our record's fault,
// and the truthful outcome is a closed event with no picture: leaving it open
// would claim something is still happening, and leaving it pending would
// promise a picture nobody can produce. An unclosed event is closed at its
// start time, the last moment the detector is known to have seen it.
func (s *Service) settleAbandoned(ctx context.Context, candidate openEvent) {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE events SET
			ended_at=COALESCE(ended_at,started_at),
			expires_at=COALESCE(expires_at,COALESCE(ended_at,started_at) + $2::interval),
			snapshot_state=CASE WHEN snapshot_state='pending' THEN 'gone' ELSE snapshot_state END,
			updated_at=clock_timestamp()
		WHERE id=$1 AND (ended_at IS NULL OR snapshot_state='pending')`,
		candidate.ID, EventIndexRetention.String())
	if err != nil && ctx.Err() == nil {
		slog.Warn("abandoned event not settled", "event", candidate.ID, "error", err)
	}
}

// snapshotVerdict is what asking the detector for the image itself established.
type snapshotVerdict int

const (
	snapshotUnknown snapshotVerdict = iota
	snapshotPresent
	snapshotAbsent
)

// probeSnapshot asks for the picture instead of the flag. The flag is a claim
// the detector makes when it finishes an event; the bytes are the fact, and the
// two disagree exactly in the case that produced this code.
func (s *Service) probeSnapshot(ctx context.Context, client *http.Client, sourceID string) snapshotVerdict {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Frigate+"/api/events/"+sourceID+"/snapshot.jpg", nil)
	if err != nil {
		return snapshotUnknown
	}
	response, err := client.Do(request)
	if err != nil {
		return snapshotUnknown
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusOK:
		return snapshotPresent
	case response.StatusCode == http.StatusNotFound:
		return snapshotAbsent
	default:
		// A struggling detector must not be read as "no picture".
		return snapshotUnknown
	}
}

// markSnapshotReady records a picture the detector does have. The event is
// already closed, so only the snapshot dimension changes; the archive sweep
// picks it up from here on its own tick.
func (s *Service) markSnapshotReady(ctx context.Context, candidate openEvent) {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE events SET snapshot_state='ready',updated_at=clock_timestamp()
		WHERE id=$1 AND snapshot_state='pending'`, candidate.ID)
	if err != nil && ctx.Err() == nil {
		slog.Warn("recovered snapshot not recorded", "event", candidate.ID, "error", err)
	}
}

// settleWithoutSnapshot records that a closed event has no picture and is not
// going to get one. The event closed, the detector has been asked for the image
// itself well after the fact, and it answered that there is none: waiting longer
// would only keep the operator watching a placeholder.
func (s *Service) settleWithoutSnapshot(ctx context.Context, candidate openEvent) {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE events SET snapshot_state='gone',updated_at=clock_timestamp()
		WHERE id=$1 AND snapshot_state='pending'`, candidate.ID)
	if err != nil && ctx.Err() == nil {
		slog.Warn("closed event without a snapshot not settled", "event", candidate.ID, "error", err)
	}
}

// ReconcileStaleOpen closes events that are old enough that nobody is coming
// back for them, even when the detector is unreachable entirely.
//
// Without this, a site whose detector is gone would accumulate open events
// forever: reconciliation cannot ask, and nothing else would ever close them.
// The threshold is deliberately far beyond any real detection, so this can only
// ever touch events the upstream has already stopped talking about.
func (s *Service) ReconcileStaleOpen(ctx context.Context) {
	if s.DB == nil {
		return
	}
	_, err := s.DB.Pool.Exec(ctx, `UPDATE events SET
			ended_at=COALESCE(ended_at,started_at),
			expires_at=COALESCE(expires_at,COALESCE(ended_at,started_at) + $2::interval),
			snapshot_state=CASE WHEN snapshot_state='pending' THEN 'gone' ELSE snapshot_state END,
			updated_at=clock_timestamp()
		WHERE source='frigate' AND (
			(ended_at IS NULL AND started_at < clock_timestamp() - $1::interval)
			OR (ended_at IS NOT NULL AND snapshot_state='pending' AND ended_at < clock_timestamp() - $1::interval)
		)`,
		StaleOpenThreshold.String(), EventIndexRetention.String())
	if err != nil && ctx.Err() == nil {
		slog.Warn("stale open events not closed", "error", err)
	}
}
