package integration

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/detection"
	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/id"
)

func newEventsFixture(t *testing.T) (publishFixture, *events.Service) {
	t.Helper()
	fixture := newPublicationFixture(t)
	service := events.New(fixture.DB, fixture.Auth, "", fixture.Site.Secrets.SiteID)
	return fixture, service
}

// frigatePayload mirrors the Frigate 0.17 event envelope.
func frigatePayload(eventType, id, camera, label string, start float64, end *float64, zones []string, hasSnapshot bool) []byte {
	body := map[string]any{
		"type": eventType,
		"after": map[string]any{
			"id": id, "camera": camera, "label": label, "score": 0.8, "top_score": 0.9,
			"zones": zones, "entered_zones": zones, "start_time": start, "has_snapshot": hasSnapshot,
		},
		"before": map[string]any{"id": id, "camera": camera},
	}
	if end != nil {
		body["after"].(map[string]any)["end_time"] = *end
	}
	raw, _ := json.Marshal(body)
	return raw
}

// frigatePayloadWithBox mirrors the envelope the detector actually publishes.
//
// Two details are deliberately faithful because getting either wrong is how the
// exclusion feature came to be untested in the only mode production uses:
//
//   - `box` is a TOP-LEVEL field of `after`. There is no `data` nesting in the
//     MQTT payload; that shape belongs to the REST API.
//   - `box` is in detect-frame PIXELS, not fractions, and it is CORNERS
//     [x1,y1,x2,y2], not [x,y,w,h]. The API normalizes and subtracts; the stream
//     does neither.
//
// The helper takes [x,y,w,h] fractions so the tests can talk about frame regions,
// and writes what the detector writes: corner pixels.
func frigatePayloadWithBox(eventType, id, camera, label string, start float64, box []float64) []byte {
	pixels := []float64{
		box[0] * detection.DetectWidth,
		box[1] * detection.DetectHeight,
		(box[0] + box[2]) * detection.DetectWidth,
		(box[1] + box[3]) * detection.DetectHeight,
	}
	body := map[string]any{
		"type": eventType,
		"after": map[string]any{
			"id": id, "camera": camera, "label": label, "score": 0.8, "top_score": 0.9,
			"zones": []string{}, "current_zones": []string{}, "entered_zones": []string{},
			"start_time": start, "has_snapshot": false,
			"box": pixels,
		},
		"before": map[string]any{"id": id, "camera": camera},
	}
	raw, _ := json.Marshal(body)
	return raw
}

// An exclusion suppresses the event without deleting it.
//
// The row keeps `excluded_reason` and the rule id, so an operator can see how
// much a rule is eating — a rule that silently deleted rows would be
// indistinguishable from a working one, and a rule set too wide would look
// exactly like a site with nothing happening.
func TestExclusionSuppressesWithoutDeleting(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	// A region covering the left half of the frame, quiet around the clock so the
	// window cannot be what makes this pass.
	view, err := service.SaveExclusion(ctx, fixture.Admin, fixture.Channel, events.ExclusionInput{
		Name:        "门前停车位",
		Coordinates: [][2]float64{{0, 0}, {0.5, 0}, {0.5, 1}, {0, 1}},
		Categories:  []string{"person"},
		Reason:      "夜间误判电动车",
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Suppressed24h != 0 {
		t.Fatalf("a fresh rule has suppressed nothing yet, got %d", view.Suppressed24h)
	}

	started := float64(time.Now().UTC().Add(-time.Minute).Unix())
	// Inside the region: excluded.
	if err := service.Ingest(ctx, frigatePayloadWithBox("new", "evt-inside", "front-door", "person", started, []float64{0.1, 0.1, 0.2, 0.2})); err != nil {
		t.Fatal(err)
	}
	// Outside the region: reported.
	if err := service.Ingest(ctx, frigatePayloadWithBox("new", "evt-outside", "front-door", "person", started, []float64{0.7, 0.1, 0.1, 0.2})); err != nil {
		t.Fatal(err)
	}
	// Inside the region but a category the rule does not name: reported.
	if err := service.Ingest(ctx, frigatePayloadWithBox("new", "evt-car", "front-door", "car", started, []float64{0.1, 0.1, 0.2, 0.2})); err != nil {
		t.Fatal(err)
	}

	// The excluded row still exists, with its reason.
	var reason string
	var ruleID *id.ID
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT COALESCE(excluded_reason,''),excluded_rule_id FROM events WHERE source_id='evt-inside'`).Scan(&reason, &ruleID); err != nil {
		t.Fatal(err)
	}
	if reason == "" || ruleID == nil {
		t.Fatalf("the excluded event must keep its reason and rule, got %q %v", reason, ruleID)
	}
	// The stored box is [x,y,w,h] in frame fractions, whatever the wire used. A
	// pixel box or an unconverted corner box sits outside every 0..1 polygon and
	// quietly excludes nothing — the failure modes this test now pins.
	var rawJSON []byte
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT raw FROM events WHERE source_id='evt-inside'`).Scan(&rawJSON); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Box []float64 `json:"box"`
	}
	if err := json.Unmarshal(rawJSON, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Box) != 4 {
		t.Fatalf("the detector's box was not stored: %s", rawJSON)
	}
	wantBox := []float64{0.1, 0.1, 0.2, 0.2}
	for i := range wantBox {
		if math.Abs(stored.Box[i]-wantBox[i]) > 1e-6 {
			t.Fatalf("stored box %v, want [x,y,w,h] fractions %v", stored.Box, wantBox)
		}
	}
	// The others were not excluded.
	var visible int
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE source_id IN ('evt-outside','evt-car') AND excluded_reason IS NULL`).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if visible != 2 {
		t.Fatalf("outside the region or outside the category must still be recorded, got %d", visible)
	}
	// And the list does not show the excluded one.
	page, err := service.List(ctx, fixture.Admin, events.ListQuery{
		Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour), Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.SourceID == "evt-inside" {
			t.Fatal("an excluded event must not appear in the list")
		}
	}
	if len(page.Items) != 2 {
		t.Fatalf("the list holds the two reportable events, got %d", len(page.Items))
	}
	// The rule reports what it suppressed.
	list, err := service.ListExclusions(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Suppressed24h != 1 {
		t.Fatalf("the rule must count its suppressions, got %+v", list)
	}
}

// An excluded event must not trip a rule either: an exclusion that still pages
// someone at 3 a.m. has not excluded anything.
func TestExcludedEventTripsNoRule(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpsertRule(ctx, fixture.Admin, events.Rule{
		ChannelID:  fixture.Channel,
		Name:       "任何出现",
		Kind:       "appear",
		Categories: []string{"person"},
		Severity:   "high",
		Enabled:    true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveExclusion(ctx, fixture.Admin, fixture.Channel, events.ExclusionInput{
		Name:        "整帧",
		Coordinates: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}},
		Categories:  []string{"person"},
		Enabled:     true,
	}); err != nil {
		t.Fatal(err)
	}
	started := float64(time.Now().UTC().Add(-time.Minute).Unix())
	if err := service.Ingest(ctx, frigatePayloadWithBox("new", "evt-silent", "front-door", "person", started, []float64{0.4, 0.4, 0.1, 0.1})); err != nil {
		t.Fatal(err)
	}
	var hits int
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT count(*) FROM event_rule_hits h JOIN events e ON e.id=h.event_id WHERE e.source_id='evt-silent'`).Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("an excluded event must not record rule hits, got %d", hits)
	}
}

func TestEventIngestLifecycle(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	// The mapping table is what attributes an upstream camera to a channel.
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	viewerUser, err := fixture.Auth.CreateUser(ctx, fixture.Admin, auth.CreateUserInput{Username: "event_viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := fixture.Auth.Login(ctx, "event_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	started := float64(time.Now().UTC().Add(-time.Minute).Unix())
	// A "new" message creates the event; the snapshot is still pending.
	if err := service.Ingest(ctx, frigatePayload("new", "evt-1", "front-door", "person", started, nil, []string{"门口"}, false)); err != nil {
		t.Fatal(err)
	}
	// An unmapped camera is recorded as dropped, not merged into a channel.
	if err := service.Ingest(ctx, frigatePayload("new", "evt-2", "unknown-cam", "person", started, nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	// The update completes the same logical event with end time and snapshot.
	ended := started + 12
	if err := service.Ingest(ctx, frigatePayload("update", "evt-1", "front-door", "person", started, &ended, []string{"门口"}, true)); err != nil {
		t.Fatal(err)
	}
	// A late "new" for the closed event must not reopen it or reset its end.
	if err := service.Ingest(ctx, frigatePayload("new", "evt-1", "front-door", "person", started, nil, []string{"门口"}, false)); err != nil {
		t.Fatal(err)
	}
	window := events.ListQuery{ChannelID: fixture.Channel, Start: time.Now().Add(-time.Hour), End: time.Now().Add(time.Hour), Limit: 50}
	// A viewer without the playback grant sees nothing, not a refusal.
	if page, err := service.List(ctx, login.Principal, window); err != nil || len(page.Items) != 0 {
		t.Fatal("without the grant the list must be empty", page, err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO channel_grants(user_id,channel_id,playback) VALUES($1,$2,true) ON CONFLICT (user_id,channel_id) DO UPDATE SET playback=true`, viewerUser.ID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	page, err := service.List(ctx, login.Principal, window)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("exactly one logical event must exist (dedupe by upstream id)", page, err)
	}
	item := page.Items[0]
	if item.SourceID != "evt-1" || item.Category != "person" || item.EndedAt == nil || item.SnapshotState != "ready" || item.Score < 0.8 {
		t.Fatal("the update must fill end time, snapshot and top score", item)
	}
	if len(item.Zones) != 1 || item.Zones[0] != "门口" {
		t.Fatal("zones must survive normalization", item.Zones)
	}
	if item.ExpiresAt == nil {
		t.Fatal("a finished event carries its 90-day index deadline", item)
	}
}

func TestEventExpirySweep(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	ended := float64(time.Now().UTC().Add(-time.Hour).Unix())
	started := ended - 10
	if err := service.Ingest(ctx, frigatePayload("update", "old-1", "front-door", "person", started, &ended, nil, false)); err != nil {
		t.Fatal(err)
	}
	// Force the deadline past: the sweep removes the index but the handling
	// trail cascades with it (reference behaviour), recordings are untouched.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE events SET expires_at=clock_timestamp()-interval '1 day', ended_at=clock_timestamp()-interval '91 days'`); err != nil {
		t.Fatal(err)
	}
	service.SweepExpiry(ctx)
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&count); err != nil || count != 0 {
		t.Fatal("a stale finished event expires", count, err)
	}
	// An event overlapping a protected segment survives (protection exception).
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	segmentStart := fixture.Completion.StartTime
	overlap := float64(segmentStart.Add(time.Second).Unix())
	if err := service.Ingest(ctx, frigatePayload("update", "kept-1", "front-door", "person", overlap, &overlap, nil, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE events SET expires_at=clock_timestamp()-interval '1 day', ended_at=clock_timestamp()-interval '91 days' WHERE source_id='kept-1'`); err != nil {
		t.Fatal(err)
	}
	var keptID id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id FROM events WHERE source_id='kept-1'`).Scan(&keptID); err != nil {
		t.Fatal(err)
	}
	var segmentID id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id FROM recording_segments`).Scan(&segmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Service.SetProtection(ctx, fixture.Admin, segmentID, true); err != nil {
		t.Fatal(err)
	}
	// The overlap window is relative to the (forced) ended_at, so protect at
	// the segment's real time instead: re-ingest with the original times.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE events SET started_at=$2,ended_at=$3,expires_at=clock_timestamp()-interval '1 day' WHERE id=$1`, keptID, segmentStart.UTC(), segmentStart.Add(time.Second).UTC()); err != nil {
		t.Fatal(err)
	}
	service.SweepExpiry(ctx)
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT source_id FROM events WHERE id=$1`, keptID).Scan(&state); err != nil {
		t.Fatal("an event overlapping protected evidence keeps its index", err)
	}
}
