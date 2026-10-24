package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// running, and it must be chosen against the SHAPE of real detections.
//
// It was ten minutes, which measured against this site covers almost every
// event: the median detection lasts 47 seconds and the 95th percentile is 4.7
// minutes (max 8.6). A ten-minute gate therefore did not protect live events — it
// only guaranteed that a lost end message was repaired long after everyone had
// stopped looking, and the whole time the event's clip sat 'finalizing' (the page
// says 写入中) even though later clips had already appeared.
//
// The age gate is per-event now, not one number for every detection.
//
// The old comment justified two minutes with "the median detection lasts 47
// seconds". Measured again on this site after sensitivity was raised: the median
// is 4 seconds and the 90th percentile is 33 seconds. A single two-minute gate
// therefore spent two minutes saying "抓拍待同步 · 稍后自动核对" about events that
// had ended in one second and could never have a picture — the operator read a
// pending state that was already decided.
//
// The gate exists to avoid touching a live event, and a live event has no
// end time. So the wait is now proportional to how long the event itself lasted:
// a detection that ended after one second is worth asking about immediately, one
// that ran five minutes is worth waiting on in case its picture is still being
// written.
const (
	// reconcileMinAge is the floor for a CLOSED event: no event is examined
	// sooner than this after it ends, because the detector writes the picture
	// around the end message.
	reconcileMinAge = 30 * time.Second
	// reconcileMaxAge caps the proportional wait. An event that ran longer than
	// this is checked at this interval, not at its own duration.
	reconcileMaxAge = 2 * time.Minute
	// openEventMinAge is how long an event with NO end time must age before the
	// sweep asks the detector about it.
	//
	// This is deliberately the old two-minute gate and not the 30-second floor:
	// without an end time there is nothing to be proportional to, and an event
	// that is genuinely still running must not be interrogated. Dropping this to
	// 30 seconds closed a detection that had only been running for a minute.
	openEventMinAge = 2 * time.Minute
)

// reconcileBatch bounds one pass. It used to mean "this many HTTP calls": each
// candidate cost one GET of its own. The list route below answers the whole batch
// in one request, so the bound is now about how much work one pass does, not how
// hard it hits the detector.
const reconcileBatch = 50

// reconcileListLimit bounds the single list request. The response is compact
// (thumbnails excluded), and a batch of 50 candidates needs at most that many
// rows plus whatever else the detector recorded in the same window.
const reconcileListLimit = 2000

// reconcileLookupMargin widens the list window on the early side. The detector's
// clock is not this process's clock, and an event that started a few seconds
// before the oldest candidate must still come back in the same response.
const reconcileLookupMargin = 5 * time.Minute

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
//
// The batch is answered by ONE list request rather than one request per event.
// That is an efficiency change with a deliberate asymmetry: the list is used as
// POSITIVE evidence only. A row returned by it supplies the facts; a row missing
// from it proves nothing, because the response is bounded by limit and by the
// window asked for. Absence therefore still costs one per-event request to
// confirm a 404 — that is the rare case, and it is the one where a wrong answer
// (declaring a picture gone that the detector still has) is unrecoverable.
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
	if len(open) == 0 {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	// One pass, one list. `open` is newest-first, so the last row is the oldest
	// candidate and its start bounds the window.
	byID, listErr := s.listEventsSince(ctx, client, open[len(open)-1].Started.Add(-reconcileLookupMargin))
	if listErr != nil {
		// Fall back to per-event reads for this pass: the batch is small and a
		// missing list must not turn into "nothing to do".
		if ctx.Err() == nil {
			slog.Warn("event reconciliation list unavailable", "error", listErr)
		}
	}
	for _, candidate := range open {
		if ctx.Err() != nil {
			return
		}
		report, found := byID[candidate.SourceID]
		if !found {
			var err error
			report, err = s.fetchEvent(ctx, client, candidate.SourceID)
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

// listEventsSince fetches the detector's event index from `after` to now, keyed
// by upstream id. Thumbnails are excluded: this call wants facts, and the image
// is fetched separately through the archive path.
func (s *Service) listEventsSince(ctx context.Context, client *http.Client, after time.Time) (map[string]eventReport, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/events?after=%d&limit=%d&include_thumbnails=0",
			s.Frigate, after.Unix(), reconcileListLimit), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("detector answered " + response.Status)
	}
	var rows []eventReport
	if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make(map[string]eventReport, len(rows))
	for _, row := range rows {
		if row.ID == "" {
			continue
		}
		out[row.ID] = row
	}
	return out, nil
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
			(ended_at IS NULL AND started_at < clock_timestamp() - $4::interval)
			-- It closed but the picture never arrived. The wait is proportional to
			-- the event's own length: a one-second detection is asked about after
			-- $1, a five-minute one after $3.
			--
			-- 'gone' is included inside the retry window. A 'gone' verdict was once
			-- reached from a single 404 and then became permanent — invisible to the
			-- archive sweep, which only looks at 'ready', and to this scan, which
			-- only looked at 'pending'. Frigate writes pictures asynchronously, so a
			-- 404 seconds after an event can precede the file by a minute; a row
			-- marked gone in that gap could never recover the picture even though
			-- the detector had it. Outside the window the verdict stands.
			OR (ended_at IS NOT NULL AND (
					snapshot_state='pending'
					OR (snapshot_state='gone' AND started_at >= clock_timestamp() - $5::interval)
				)
			    AND ended_at < clock_timestamp() - LEAST(GREATEST(ended_at - started_at, $1::interval), $3::interval))
		)
		ORDER BY started_at DESC LIMIT $2`,
		reconcileMinAge.String(), reconcileBatch, reconcileMaxAge.String(), openEventMinAge.String(), SnapshotRetryWindow.String())
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
		// Box is [x, y, w, h] in fractions of the frame. The REST API reports it
		// normalized, while the MQTT envelope reports pixels; Ingest converts at
		// its boundary so both paths store the same unit.
		Box []float64 `json:"box"`
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
	rawFields := map[string]any{"type": "reconcile", "label": report.Label, "camera": report.Camera}
	if len(report.Data.Box) == 4 {
		rawFields["box"] = report.Data.Box
	}
	raw, _ := json.Marshal(rawFields)
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
	if err != nil {
		return err
	}
	if endTime != nil {
		s.publishReconciledClip(ctx, candidate.ID)
	}
	return nil
}

// publishReconciledClip publishes the clip of an event this pass just closed.
//
// The assembly loop publishes a clip on the pass that follows the event's end
// message, and a retention-ticker safety net catches clips whose job already
// settled. Neither runs here: this path learns about the end from the DETECTOR,
// minutes later, so without this call the clip stays 'finalizing' (the page says
// 写入中) until the next safety-net tick — long after later clips have already
// been published, which is exactly the inconsistency an operator notices first.
//
// Written as SQL rather than a call into the recording package because the
// dependency runs recording → events; going the other way would be a cycle.
func (s *Service) publishReconciledClip(ctx context.Context, eventID id.ID) {
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_segments s SET state='ready',error_code=NULL,ready_at=clock_timestamp()
		FROM event_clip_jobs j
		WHERE j.event_id=$1 AND j.segment_id=s.id AND s.state='finalizing'`, eventID); err != nil {
		if ctx.Err() == nil {
			slog.Warn("reconciled clip not published", "event", eventID, "error", err)
		}
		return
	}
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_locations l SET state='ready',verified_at=clock_timestamp()
		FROM event_clip_jobs j
		WHERE j.event_id=$1 AND j.segment_id=l.segment_id AND l.state='finalizing'`, eventID); err != nil && ctx.Err() == nil {
		slog.Warn("reconciled clip location not published", "event", eventID, "error", err)
	}
}

// settleAbandoned handles an event the detector no longer knows about at all.
//
// The detector's own retention can drop an event before its closing message
// arrived, or before its snapshot reached us. That is not our record's fault,
// and the truthful outcome is a closed event with no picture: leaving it open
// would claim something is still happening, and leaving it pending would
// promise a picture nobody can produce. An unclosed event is closed at its
// start time, the last moment the detector is known to have seen it.
//
// The end time is settled immediately — the detector not knowing the event means
// nobody is going to report one. The snapshot verdict keeps the usual wait: a
// 404 for the event can race a picture that is still being written, and 'gone'
// is invisible to the archive sweep, so declaring it early forecloses the only
// path that could still recover the image.
func (s *Service) settleAbandoned(ctx context.Context, candidate openEvent) {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE events SET
			ended_at=COALESCE(ended_at,started_at),
			expires_at=COALESCE(expires_at,COALESCE(ended_at,started_at) + $2::interval),
			snapshot_state=CASE WHEN snapshot_state='pending'
				AND started_at < clock_timestamp() - $3::interval THEN 'gone' ELSE snapshot_state END,
			updated_at=clock_timestamp()
		WHERE id=$1 AND (ended_at IS NULL OR snapshot_state='pending')`,
		candidate.ID, EventIndexRetention.String(), SnapshotRetryWindow.String())
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("abandoned event not settled", "event", candidate.ID, "error", err)
		}
		return
	}
	// The event is now closed, so its clip is finished media. See
	// publishReconciledClip: this path also closes events the end message never
	// reached, and without this the clip would wait for the next safety-net tick.
	s.publishReconciledClip(ctx, candidate.ID)
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
//
// 'gone' is included because a gone verdict inside the retry window is a
// provisional one: this pass asks the detector for the bytes, and if it answers
// with an image the earlier 404 is simply out of date.
func (s *Service) markSnapshotReady(ctx context.Context, candidate openEvent) {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE events SET snapshot_state='ready',snapshot_archive_error=NULL,updated_at=clock_timestamp()
		WHERE id=$1 AND snapshot_state IN ('pending','gone')`, candidate.ID)
	if err != nil && ctx.Err() == nil {
		slog.Warn("recovered snapshot not recorded", "event", candidate.ID, "error", err)
	}
}

// settleWithoutSnapshot records that a closed event has no picture and is not
// going to get one.
//
// One 404 is not that answer. Frigate writes snapshots asynchronously and this
// service has already been wrong about it once: a single "snapshot.jpg 404" was
// treated as final, so the row became 'gone', and 'gone' is invisible to the
// archive sweep — which only picks up 'ready'. A picture that appeared a minute
// later then had no path back into the index at all.
//
// The wait is the same window the archive sweep uses (SnapshotRetryWindow): it
// is the length of time this product already decided Frigate can still produce a
// picture for an event. Before that window ends, a 404 leaves the event pending
// and the next pass asks again; the sweep, which asks for the image itself, is
// what can still rescue it.
func (s *Service) settleWithoutSnapshot(ctx context.Context, candidate openEvent) {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE events SET snapshot_state='gone',updated_at=clock_timestamp()
		WHERE id=$1 AND snapshot_state='pending'
		  AND started_at < clock_timestamp() - $2::interval`, candidate.ID, SnapshotRetryWindow.String())
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
