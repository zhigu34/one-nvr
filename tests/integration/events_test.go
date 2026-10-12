package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/fault"
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
	// Handling: version-guarded, trail recorded, reopen possible.
	if _, err := service.Handle(ctx, fixture.Admin, item.ID, "acknowledged", "已确认", item.Version); err != nil {
		t.Fatal(err)
	}
	_, err = service.Handle(ctx, fixture.Admin, item.ID, "closed", "并发操作", item.Version)
	var conflict *fault.Error
	if !errors.As(err, &conflict) || conflict.Code != "event_version_conflict" {
		t.Fatal("a stale version must be refused", err)
	}
	updated, err := service.Get(ctx, fixture.Admin, item.ID)
	if err != nil || updated.HandleState != "acknowledged" || updated.Version != item.Version+1 || len(updated.Handling) != 1 {
		t.Fatal("the handling trail must record actor, state and time", updated, err)
	}
	if _, err := service.Handle(ctx, fixture.Admin, item.ID, "open", "重开", updated.Version); err != nil {
		t.Fatal(err)
	}
	reopened, err := service.Get(ctx, fixture.Admin, item.ID)
	if err != nil || reopened.HandleState != "open" || len(reopened.Handling) != 2 {
		t.Fatal("an event must be reopenable", reopened, err)
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
