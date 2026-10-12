package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// EventIndexRetention is EVT-07's window: a finished ordinary event expires
// 90 days after its real end. Active events and protection hold their index.
const EventIndexRetention = 90 * 24 * time.Hour

// StaleOpenThreshold: an event the upstream never closed (Frigate restarted,
// message lost) stops being "active" after this, so it can expire normally.
const StaleOpenThreshold = 24 * time.Hour

var (
	ErrEventNotFound = auth.ErrNotFound
	ErrEventConflict = fault.New(409, "event_version_conflict", "事件已被他人处理，请刷新后重试")
)

// Event is the API view. raw stays private; zones are the normalized strings
// Frigate reported, and the snapshot is proxied, never a direct path.
type Event struct {
	ID               id.ID          `json:"id"`
	ChannelID        *id.ID         `json:"channel_id"`
	SourceRevisionID *id.ID         `json:"source_revision_id"`
	Source           string         `json:"source"`
	SourceID         string         `json:"source_id"`
	Category         string         `json:"category"`
	Score            float64        `json:"score"`
	Zones            []string       `json:"zones"`
	StartedAt        time.Time      `json:"started_at"`
	EndedAt          *time.Time     `json:"ended_at"`
	SnapshotState    string         `json:"snapshot_state"`
	MediaState       string         `json:"media_state"`
	HandleState      string         `json:"handle_state"`
	HandleNote       string         `json:"handle_note"`
	Version          int64          `json:"version"`
	ExpiresAt        *time.Time     `json:"expires_at"`
	Handling         []HandleRecord `json:"handling"`
}

type HandleRecord struct {
	ID        id.ID     `json:"id"`
	ActorName string    `json:"actor_name"`
	State     string    `json:"state"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// Service owns ingestion, reading, handling and expiry.
type Service struct {
	DB      *database.DB
	Auth    *auth.Service
	Frigate string // base URL of the Frigate HTTP API, empty when disabled
	siteID  id.ID
	now     func() time.Time
}

func New(db *database.DB, accounts *auth.Service, frigateURL string, siteID id.ID) *Service {
	return &Service{DB: db, Auth: accounts, Frigate: strings.TrimRight(frigateURL, "/"), siteID: siteID, now: time.Now}
}

const eventColumns = `e.id,e.channel_id,e.source_revision_id,e.source,e.source_id,e.category,e.score,e.zones,e.started_at,e.ended_at,e.snapshot_state,e.media_state,e.handle_state,e.handle_note,e.version,e.expires_at`

func scanEvent(row pgx.Row) (Event, error) {
	var out Event
	var zones []byte
	err := row.Scan(&out.ID, &out.ChannelID, &out.SourceRevisionID, &out.Source, &out.SourceID, &out.Category, &out.Score, &zones, &out.StartedAt, &out.EndedAt, &out.SnapshotState, &out.MediaState, &out.HandleState, &out.HandleNote, &out.Version, &out.ExpiresAt)
	if err != nil {
		return out, err
	}
	out.Zones = []string{}
	_ = json.Unmarshal(zones, &out.Zones)
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
	} `json:"after"`
}

// Ingest normalizes one MQTT payload into the event index. It is idempotent on
// the upstream id and monotonic: an update may fill in end time, score and
// snapshot, but a late "new" for a closed event never reopens it (AI-05).
func (s *Service) Ingest(ctx context.Context, payload []byte) error {
	var message frigateMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return errMQTTProtocol
	}
	after := message.After
	if after.ID == "" || after.Camera == "" || after.Label == "" || after.StartTime <= 0 {
		return errMQTTProtocol
	}
	started := time.Unix(int64(after.StartTime), 0).UTC()
	score := after.Score
	if after.TopScore > score {
		score = after.TopScore
	}
	zones := after.EnteredZones
	if len(zones) == 0 {
		zones = after.Zones
	}
	raw, _ := json.Marshal(map[string]any{"type": message.Type, "label": after.Label, "camera": after.Camera})
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var channelID id.ID
		var revision *id.ID
		err := tx.QueryRow(ctx, `SELECT c.id,c.current_revision_id FROM frigate_cameras f JOIN channels c ON c.id=f.channel_id WHERE f.frigate_name=$1`, after.Camera).Scan(&channelID, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			// An unmapped camera is dropped with a record, not silently merged
			// into another channel (CH-07/CH-08).
			return s.recordUnmapped(ctx, tx, after.Camera, after.ID)
		}
		if err != nil {
			return err
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
		eventID, err := id.New()
		if err != nil {
			return err
		}
		expires := any(nil)
		if endTime != nil {
			expires = endTime.Add(EventIndexRetention)
		}
		zonesJSON, err := json.Marshal(zones)
		if err != nil {
			return err
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
			eventID, channelID, revision, after.ID, after.Label, score, zonesJSON, started, endTime, snapshotState, expires, raw)
		return err
	})
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
	HandleState      string
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
		rows, err := tx.Query(ctx, `SELECT `+eventColumns+` FROM events e
			WHERE e.started_at < $1 AND e.started_at >= $2
			AND ($3::uuid IS NULL OR (e.started_at,e.id)<($4,$3::uuid))
			AND ($5::uuid IS NULL OR e.channel_id=$5)
			AND ($6::uuid IS NULL OR e.source_revision_id=$6)
			AND ($7::text IS NULL OR e.category=$7)
			AND ($8::text IS NULL OR e.handle_state=$8)
			ORDER BY e.started_at DESC,e.id DESC LIMIT $9`, q.End.UTC(), q.Start.UTC(), nullableNull(q.Cursor), cursorTime, nullableID(q.ChannelID), nullableID(q.SourceRevisionID), nullableText(q.Category), nullableText(q.HandleState), q.Limit+1)
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
		rows, err := tx.Query(ctx, `SELECT id,actor_name,state,note,created_at FROM event_handling WHERE event_id=$1 ORDER BY created_at,id`, eventID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var record HandleRecord
			if err := rows.Scan(&record.ID, &record.ActorName, &record.State, &record.Note, &record.CreatedAt); err != nil {
				return err
			}
			out.Handling = append(out.Handling, record)
		}
		return rows.Err()
	})
	return out, err
}

// Handle records one handling transition with the operator's note, guarded by
// the version (EVT-03: concurrent updates must not overwrite each other).
func (s *Service) Handle(ctx context.Context, p auth.Principal, eventID id.ID, state, note string, expected int64) (Event, error) {
	if state != "open" && state != "acknowledged" && state != "closed" && state != "false_positive" {
		return Event{}, auth.ErrInvalid
	}
	if len(note) > 500 {
		return Event{}, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		item, err := scanEvent(tx.QueryRow(ctx, `SELECT `+eventColumns+` FROM events e WHERE e.id=$1 FOR UPDATE`, eventID))
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
		if expected > 0 && item.Version != expected {
			return ErrEventConflict
		}
		var actor string
		if err := tx.QueryRow(ctx, `SELECT username FROM users WHERE id=$1`, p.UserID).Scan(&actor); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE events SET handle_state=$2,handle_note=$3,version=version+1,updated_at=clock_timestamp() WHERE id=$1`, eventID, state, note); err != nil {
			return err
		}
		recordID, err := id.New()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO event_handling(id,event_id,actor_id,actor_name,state,note) VALUES($1,$2,$3,$4,$5,$6)`, recordID, eventID, p.UserID, actor, state, note); err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"state": state})
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "event.handled", ObjectID: eventID, Details: details})
	})
	if err != nil {
		return Event{}, err
	}
	return s.Get(ctx, p, eventID)
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
func (s *Service) Snapshot(ctx context.Context, p auth.Principal, eventID id.ID) ([]byte, string, error) {
	event, err := s.Get(ctx, p, eventID)
	if err != nil {
		return nil, "", err
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
		if _, updateErr := s.DB.Pool.Exec(ctx, `UPDATE events SET snapshot_state='gone' WHERE id=$1 AND snapshot_state<>'gone'`, eventID); updateErr != nil && ctx.Err() == nil {
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
