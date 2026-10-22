package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/notify"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// EventIndexRetention is EVT-07's window: a finished ordinary event expires
// 90 days after its real end. Active events and protection hold their index.
const EventIndexRetention = 90 * 24 * time.Hour

// StaleOpenThreshold: an event the upstream never closed (Frigate restarted,
// message lost) stops being "active" after this, so it can expire normally.
const StaleOpenThreshold = 24 * time.Hour

var ErrEventNotFound = auth.ErrNotFound

// Event is the API view. raw stays private; zones are the normalized strings
// Frigate reported, and the snapshot is proxied, never a direct path.
type Event struct {
	ID               id.ID      `json:"id"`
	ChannelID        *id.ID     `json:"channel_id"`
	SourceRevisionID *id.ID     `json:"source_revision_id"`
	Source           string     `json:"source"`
	SourceID         string     `json:"source_id"`
	Category         string     `json:"category"`
	Score            float64    `json:"score"`
	Zones            []string   `json:"zones"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at"`
	SnapshotState    string     `json:"snapshot_state"`
	MediaState       string     `json:"media_state"`
	// The span actually available for playback, which is not the span the
	// window asked for: a segment boundary lands wherever the keyframe put it.
	// Null when no footage covers the event.
	MediaStartAt  *time.Time `json:"media_start_at"`
	MediaEndAt    *time.Time `json:"media_end_at"`
	MediaSegments int        `json:"media_segments"`
	// Box is the detector's last reported bounding box for this event, as
	// fractions of the frame: [x, y, w, h]. Absent for rows stored before the box
	// was captured, and for events that never reported one.
	Box       []float64  `json:"box,omitempty"`
	ExpiresAt *time.Time `json:"expires_at"`
	// Hits are the rules this event tripped (AI-06), carried on the detail view
	// so the operator sees why it was flagged without a second request. The
	// list view stays lean.
	Hits []RuleHit `json:"hits,omitempty"`
}

// Service owns ingestion, reading and expiry.
type Service struct {
	DB      *database.DB
	Auth    *auth.Service
	Frigate string // base URL of the Frigate HTTP API, empty when disabled
	siteID  id.ID
	now     func() time.Time
	// Notify raises a notification for each rule hit. It is optional by design:
	// an assembly that only records hits — and every test that exercises rule
	// matching alone — leaves it nil, and the hit is still recorded. Telling the
	// operator is an extension of recording, never a precondition for it.
	Notify interface {
		Raise(ctx context.Context, tx pgx.Tx, facts notify.HitFacts) (notify.Raised, error)
	}
	// PublicURL is the site's external address, used to make the links inside a
	// notification absolute for receivers that are not this browser.
	PublicURL string
	// Pools resolves a channel's bound storage pool so an archived snapshot can
	// be written into it. Optional: without it the archive sweep does nothing
	// and the detector's own copy keeps answering, which is the pre-archive
	// behaviour rather than a broken one.
	Pools *storage.Service
	// zone caches the site location for rule arming windows: ingestion
	// evaluates rules for every event, and the zone changes at most once in a
	// deployment's life.
	zone atomic.Value
}

func New(db *database.DB, accounts *auth.Service, frigateURL string, siteID id.ID) *Service {
	return &Service{DB: db, Auth: accounts, Frigate: strings.TrimRight(frigateURL, "/"), siteID: siteID, now: time.Now}
}

const eventColumns = `e.id,e.channel_id,e.source_revision_id,e.source,e.source_id,e.category,e.score,e.zones,e.started_at,e.ended_at,e.snapshot_state,e.media_state,e.media_start_at,e.media_end_at,e.media_segments,e.expires_at,e.raw`

func scanEvent(row pgx.Row) (Event, error) {
	var out Event
	var zones, raw []byte
	err := row.Scan(&out.ID, &out.ChannelID, &out.SourceRevisionID, &out.Source, &out.SourceID, &out.Category, &out.Score, &zones, &out.StartedAt, &out.EndedAt, &out.SnapshotState, &out.MediaState, &out.MediaStartAt, &out.MediaEndAt, &out.MediaSegments, &out.ExpiresAt, &raw)
	if err != nil {
		return out, err
	}
	out.Zones = []string{}
	_ = json.Unmarshal(zones, &out.Zones)
	// The detector's box is read back out of the stored payload: it is what tells
	// a re-identified person apart from two people in frame at once.
	var stored struct {
		Box []float64 `json:"box"`
	}
	_ = json.Unmarshal(raw, &stored)
	out.Box = stored.Box
	return out, nil
}

// frigateMessage is the subset of Frigate's event JSON one-nvr consumes. The
// upstream schema is versioned by the locked image; unknown fields are ignored
// on purpose (forward compatibility), missing required fields are rejected.
type frigateMessage struct {
	Type   string `json:"type"`
	Before struct {
		ID           string   `json:"id"`
		Camera       string   `json:"camera"`
		Label        string   `json:"label"`
		Score        float64  `json:"score"`
		TopScore     float64  `json:"top_score"`
		Zones        []string `json:"zones"`
		EnteredZones []string `json:"entered_zones"`
		StartTime    float64  `json:"start_time"`
		EndTime      *float64 `json:"end_time"`
		HasSnapshot  bool     `json:"has_snapshot"`
		Data         struct {
			Box []float64 `json:"box"`
		} `json:"data"`
	} `json:"before"`
	After struct {
		ID           string   `json:"id"`
		Camera       string   `json:"camera"`
		Label        string   `json:"label"`
		Score        float64  `json:"score"`
		TopScore     float64  `json:"top_score"`
		Zones        []string `json:"zones"`
		EnteredZones []string `json:"entered_zones"`
		StartTime    float64  `json:"start_time"`
		EndTime      *float64 `json:"end_time"`
		HasSnapshot  bool     `json:"has_snapshot"`
		// Data carries the detector's own geometry. box is [x, y, w, h] in
		// fractions of the frame, and it is the only signal that separates "the
		// same person was re-identified" (nearly the same place) from "two people
		// were in frame at once" (different places). Without it, folding
		// overlapping events would merge two real detections into one.
		Data struct {
			Box []float64 `json:"box"`
		} `json:"data"`
	} `json:"after"`
}

// Ingest normalizes one MQTT payload into the event index.
//
// The write itself lives in eventUpsert, shared with the reconciliation read of
// the detector's API: the stream is the fast path, and reconciliation repairs a
// message it lost, so both must produce the same record.
func (s *Service) Ingest(ctx context.Context, payload []byte) error {
	var message frigateMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return errMQTTProtocol
	}
	after := message.After
	if after.ID == "" || after.Camera == "" || after.Label == "" || after.StartTime <= 0 {
		return errMQTTProtocol
	}
	score := after.Score
	if after.TopScore > score {
		score = after.TopScore
	}
	zones := after.EnteredZones
	if len(zones) == 0 {
		zones = after.Zones
	}
	var endTime *time.Time
	if after.EndTime != nil && *after.EndTime > 0 {
		end := time.Unix(int64(*after.EndTime), 0).UTC()
		endTime = &end
	}
	snapshotState := "pending"
	if after.HasSnapshot {
		snapshotState = "ready"
	}
	rawFields := map[string]any{"type": message.Type, "label": after.Label, "camera": after.Camera}
	if len(after.Data.Box) == 4 {
		rawFields["box"] = after.Data.Box
	}
	raw, _ := json.Marshal(rawFields)
	err := s.eventUpsert(ctx, upsertInput{
		Camera:        after.Camera,
		SourceID:      after.ID,
		Category:      after.Label,
		Score:         score,
		Zones:         zones,
		StartedAt:     time.Unix(int64(after.StartTime), 0).UTC(),
		EndedAt:       endTime,
		SnapshotState: snapshotState,
		Raw:           raw,
	})
	// An unmapped camera is not a protocol error: the payload was fine, the
	// site just does not know that camera. The record was written by the upsert.
	if errors.Is(err, errEventUnmapped) {
		return nil
	}
	return err
}

func (s *Service) recordUnmapped(ctx context.Context, tx pgx.Tx, camera, sourceID string) error {
	details, _ := json.Marshal(map[string]any{"camera": camera, "source_id": sourceID})
	return audit.Append(ctx, tx, audit.Entry{Action: "event.unmapped_camera", Details: details})
}

// NoteStreamGap records that the subscription was down. A gap is evidence;
// silence would read as "no events happened" (EVT-04).
func (s *Service) NoteStreamGap(ctx context.Context, started time.Time, reason string) error {
	gapID, err := id.New()
	if err != nil {
		return err
	}
	_, err = s.DB.Pool.Exec(ctx, `INSERT INTO event_stream_gaps(id,started_at,reason) VALUES($1,$2,$3)`, gapID, started.UTC(), reason)
	return err
}

func (s *Service) CloseStreamGap(ctx context.Context, gapID id.ID) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE event_stream_gaps SET ended_at=clock_timestamp() WHERE id=$1 AND ended_at IS NULL`, gapID)
	return err
}

// ListQuery mirrors EVT-01's filters. Each zero value means "no restriction".
type ListQuery struct {
	ChannelID        id.ID
	SourceRevisionID id.ID
	Category         string
	Start, End       time.Time
	Cursor           id.ID
	Limit            int
}

type EventPage struct {
	Items      []Event `json:"items"`
	NextCursor *id.ID  `json:"next_cursor"`
}

// List pages the event index newest-first. Visibility follows the channel's
// playback grant: events are recordings-adjacent evidence.
func (s *Service) List(ctx context.Context, p auth.Principal, q ListQuery) (EventPage, error) {
	out := EventPage{Items: []Event{}}
	if s.Auth == nil || q.Limit < 1 || q.Limit > 200 || q.Start.IsZero() || q.End.IsZero() || !q.End.After(q.Start) {
		return out, auth.ErrInvalid
	}
	var cursorTime time.Time
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		cursor := time.Time{}
		if q.Cursor != "" {
			if err := tx.QueryRow(ctx, `SELECT started_at FROM events WHERE id=$1`, q.Cursor).Scan(&cursor); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return auth.ErrNotFound
				}
				return err
			}
		}
		cursorTime = cursor
		// Evidence-free slivers are not shown.
		//
		// A detection that lasted a couple of seconds, produced no picture and has
		// no footage anywhere has nothing to look at: the card is a blank tile
		// whose only content is the fact that something was briefly detected.
		// Measured on this site: 24 hours held 868 events, 407 of them three
		// seconds or shorter, and 103 of those had neither a snapshot nor footage
		// and no other event within thirty seconds — a fifth of the list was
		// unpickable.
		//
		// The rule has two halves, both in SQL so the page and the count agree:
		//
		//   - a short, evidence-free event is hidden when nothing else happened on
		//     that channel around it. It is a detection artifact, not a visit;
		//   - when something else DID happen around it, only the rows with evidence
		//     remain: the fragment is part of that activity, and its picture is on
		//     its neighbour's card.
		//
		// A short event that DOES have a picture or footage is always shown — the
		// rule is about having nothing to show, not about being brief.
		rows, err := tx.Query(ctx, `SELECT `+eventColumns+` FROM events e
			WHERE e.started_at < $1 AND e.started_at >= $2
			AND NOT (
				-- A running event is never a sliver: it has not finished, so its
				-- duration is unknown, and hiding something that is happening right
				-- now would be the worst possible answer.
				e.ended_at IS NOT NULL
				AND COALESCE(EXTRACT(EPOCH FROM (e.ended_at - e.started_at)), 0) <= $9
				AND e.snapshot_relative_path IS NULL
				AND NOT EXISTS (SELECT 1 FROM recording_segments rs
				                WHERE rs.channel_id=e.channel_id AND rs.state IN ('ready','finalizing')
				                  AND rs.start_at < COALESCE(e.ended_at,e.started_at) + interval '5 seconds'
				                  AND rs.end_at > e.started_at - interval '5 seconds')
			)
			AND ($3::uuid IS NULL OR (e.started_at,e.id)<($4,$3::uuid))
			AND ($5::uuid IS NULL OR e.channel_id=$5)
			AND ($6::uuid IS NULL OR e.source_revision_id=$6)
			AND ($7::text IS NULL OR e.category=$7)
			ORDER BY e.started_at DESC,e.id DESC LIMIT $8`, q.End.UTC(), q.Start.UTC(), nullableNull(q.Cursor), cursorTime, nullableID(q.ChannelID), nullableID(q.SourceRevisionID), nullableText(q.Category), q.Limit+1, EvidenceFreeSeconds)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanEvent(rows)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	// The grant model cannot be expressed in that query, so visibility is
	// filtered per row afterwards: a forbidden row must look absent, and an
	// event without a channel (dropped/unmapped) is never visible to anyone.
	visible := out.Items[:0]
	for _, item := range out.Items {
		if item.ChannelID == nil {
			continue
		}
		if err := s.Auth.RequireChannel(ctx, p, *item.ChannelID, auth.Playback); err == nil {
			visible = append(visible, item)
		}
	}
	// Every detection is its own row. The list answers "what did the detector
	// see", and folding neighbouring detections into one row hid that: an operator
	// counting what happened read a smaller number than the index held, and the
	// "合并" badge made an event look like something the product had decided to
	// suppress. Merging belongs to the RECORDING (one clip, several anchors,
	// clip_windows) — not to the event list, where each anchor is the thing an
	// operator navigates by.
	out.Items = visible
	if len(out.Items) > q.Limit {
		out.Items = out.Items[:q.Limit]
		cursor := out.Items[len(out.Items)-1].ID
		out.NextCursor = &cursor
	}
	return out, nil
}

// Get returns one event with its handling trail, grant-checked.
func (s *Service) Get(ctx context.Context, p auth.Principal, eventID id.ID) (Event, error) {
	var out Event
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		item, err := scanEvent(tx.QueryRow(ctx, `SELECT `+eventColumns+` FROM events e WHERE e.id=$1`, eventID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if item.ChannelID != nil {
			if err := s.Auth.RequireChannelTx(ctx, p, *item.ChannelID, auth.Playback, tx); err != nil {
				if errors.Is(err, auth.ErrForbidden) {
					return auth.ErrNotFound
				}
				return err
			}
		}
		out = item
		// Rule hits ride with the detail view: they are the reason the event is
		// interesting, so one request answers "what happened and why".
		hitRows, err := tx.Query(ctx, `SELECT h.id,h.event_id,h.rule_id,r.name,r.severity,h.zone_name,h.reason,h.notified_at,h.false_positive,h.false_positive_note,h.created_at
			FROM event_rule_hits h JOIN detection_rules r ON r.id=h.rule_id WHERE h.event_id=$1 ORDER BY h.created_at`, eventID)
		if err != nil {
			return err
		}
		defer hitRows.Close()
		for hitRows.Next() {
			var hit RuleHit
			if err := hitRows.Scan(&hit.ID, &hit.EventID, &hit.RuleID, &hit.RuleName, &hit.Severity, &hit.ZoneName, &hit.Reason, &hit.NotifiedAt, &hit.FalsePositive, &hit.Note, &hit.CreatedAt); err != nil {
				return err
			}
			out.Hits = append(out.Hits, hit)
		}
		return hitRows.Err()
	})
	return out, err
}

// SweepExpiry drops the index of ordinary finished events past 90 days.
// Active events, protected evidence references and pending work keep theirs
// (EVT-07) — the sweep never touches recordings, cloud objects or audits.
func (s *Service) SweepExpiry(ctx context.Context) {
	// EVT-07: the business "unhandled" state does not extend the window of an
	// ordinary finished event. The only exceptions are evidence protection
	// (time overlap with a protected segment, ±the replay window) and events
	// that never ended.
	_, err := s.DB.Pool.Exec(ctx, `DELETE FROM events WHERE expires_at IS NOT NULL AND expires_at<clock_timestamp()
		AND ended_at IS NOT NULL AND ended_at<clock_timestamp()-interval '90 days'
		AND NOT EXISTS(SELECT 1 FROM recording_segments s WHERE s.channel_id=events.channel_id AND s.protected
			AND s.start_at<=events.ended_at+interval '30 seconds' AND s.end_at>=events.started_at-interval '10 seconds')`)
	if err != nil && ctx.Err() == nil {
		slog.Warn("event expiry sweep unavailable", "error", err)
	}
}

// Snapshot proxies the upstream snapshot image. Frigate serves it over HTTP;
// the bytes never touch one-nvr storage, and a pending snapshot is reported as
// such instead of a broken image (EVT-02).
//
// The archived copy is served first when one exists, so a picture survives the
// detector cleaning up its own short-lived directory (STO-07). The detector
// stays the fallback rather than the source of truth.
func (s *Service) Snapshot(ctx context.Context, p auth.Principal, eventID id.ID) ([]byte, string, error) {
	event, err := s.Get(ctx, p, eventID)
	if err != nil {
		return nil, "", err
	}
	if local, err := s.localSnapshot(ctx, eventID); err == nil && local != nil {
		defer local.Close()
		data, readErr := io.ReadAll(io.LimitReader(local, 8<<20))
		if readErr == nil && len(data) > 0 {
			return data, "image/jpeg", nil
		}
		// A local copy that cannot be read falls through to the detector: the
		// operator asked for the picture, and a failure here is not a reason to
		// refuse it while another copy may answer.
	}
	if s.Frigate == "" {
		return nil, "", ErrEventNotFound
	}
	if event.SnapshotState == "pending" {
		return nil, "", fault.New(409, "snapshot_pending", "抓拍尚未生成")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Frigate+"/api/events/"+event.SourceID+"/snapshot.jpg", nil)
	if err != nil {
		return nil, "", fault.New(503, "snapshot_unavailable", "抓拍暂时不可读")
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request = request.WithContext(bounded)
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", fault.New(503, "snapshot_unavailable", "抓拍暂时不可读")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		// 'gone' is a verdict about the DETECTOR's current answer, not a permanent
		// one. Frigate writes snapshots asynchronously, so a 404 minutes after the
		// event can still be followed by the file — and the archive sweep keeps
		// asking for two hours. Recording 'gone' only where it is still gone at the
		// end of that window keeps the two paths from disagreeing: the sweep may
		// archive a picture this endpoint already called missing.
		if _, updateErr := s.DB.Pool.Exec(ctx, `UPDATE events SET snapshot_state='gone'
			WHERE id=$1 AND snapshot_state<>'gone'
			  AND started_at < clock_timestamp() - $2::interval`, eventID, SnapshotRetryWindow.String()); updateErr != nil && ctx.Err() == nil {
			slog.Warn("event snapshot reconcile unavailable", "error", updateErr)
		}
		return nil, "", ErrEventNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", fault.New(503, "snapshot_unavailable", "抓拍暂时不可读")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, "", fault.New(503, "snapshot_unavailable", "抓拍暂时不可读")
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return data, contentType, nil
}

// localSnapshot opens the archived copy when the event has one. A missing or
// unreadable copy is not an error the caller reports: it means "ask the
// detector", which is the state every event was in before archiving existed.
func (s *Service) localSnapshot(ctx context.Context, eventID id.ID) (*os.File, error) {
	if s.Pools == nil {
		return nil, nil
	}
	ref, err := s.snapshotRefFor(ctx, eventID)
	if err != nil || ref.Local == "" {
		return nil, err
	}
	return s.OpenLocalSnapshot(ctx, ref)
}

// SetMappings replaces the camera mapping table (admin). One Frigate camera
// name maps to exactly one channel; unmapped cameras are reported, never
// guessed (CH-07).
func (s *Service) SetMappings(ctx context.Context, p auth.Principal, mappings map[id.ID]string) error {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return err
	}
	if len(mappings) > 64 {
		return auth.ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM frigate_cameras`); err != nil {
			return err
		}
		for channelID, name := range mappings {
			if name == "" || len(name) > 64 {
				return auth.ErrInvalid
			}
			if _, err := tx.Exec(ctx, `INSERT INTO frigate_cameras(channel_id,frigate_name) VALUES($1,$2)`, channelID, name); err != nil {
				return err
			}
		}
		details, _ := json.Marshal(map[string]any{"mappings": len(mappings)})
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "event.mappings_updated", Details: details})
	})
}

// Mappings lists the camera mapping table for the settings view.
func (s *Service) Mappings(ctx context.Context, p auth.Principal) (map[string]string, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT frigate_name,channel_id FROM frigate_cameras`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name string
		var channelID id.ID
		if err := rows.Scan(&name, &channelID); err != nil {
			return nil, err
		}
		out[name] = string(channelID)
	}
	return out, rows.Err()
}

func nullableID(value id.ID) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableNull(value id.ID) any {
	if value == "" {
		return nil
	}
	return value
}

// EvidenceFreeSeconds is how short a detection must be for the list to hide it
// when it has nothing to show.
//
// Three seconds is the measured knee, not a guess: on this site the <=3s bucket
// averages 0.707 confidence against 0.886 for events over ten seconds, and
// Frigate writes a snapshot for barely half of them (138 of 407) — it writes
// pictures while an event runs, and a two-second detection can end before the
// first one lands. A detection that short with no picture and no footage is a
// track fragment, not something an operator can act on.
const EvidenceFreeSeconds = 3
