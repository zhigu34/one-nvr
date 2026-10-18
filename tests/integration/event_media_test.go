package integration

// PRD REC-09: an event must say what footage it has, and seek to the span that
// actually exists rather than to a fixed offset that may land in a gap.

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// seedSegment inserts a ready segment covering [start,end) for the channel.
func seedSegment(t *testing.T, fixture publishFixture, start, end time.Time) id.ID {
	t.Helper()
	segment, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	// The publication identity is immutable and the pool path unique, so each
	// segment needs its own path derived from its own id.
	path := "recordings/" + string(segment) + ".mp4"
	_, err = fixture.DB.Pool.Exec(context.Background(), `INSERT INTO recording_segments(
		id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
		original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,state,ready_at)
		VALUES($1,$2,$3,$4,$5,$6,$6,$7,'UTC',0,$7,$8,1024,'ready',clock_timestamp())`,
		segment, fixture.Channel, fixture.Revision, fixture.Run, fixture.Pool.ID, path, start.UTC(), end.UTC())
	if err != nil {
		t.Fatal(err)
	}
	return segment
}

func eventMediaState(t *testing.T, fixture publishFixture, sourceID string) (string, *time.Time) {
	t.Helper()
	var state string
	var start *time.Time
	if err := fixture.DB.Pool.QueryRow(context.Background(),
		`SELECT media_state,media_start_at FROM events WHERE source='frigate' AND source_id=$1`, sourceID).Scan(&state, &start); err != nil {
		t.Fatal(err)
	}
	return state, start
}

// Footage that covers the whole window (5s before the event, 5s after it) is
// linked, and the span stored is the recorded one — not the requested one.
func TestEventLinksToCoveringRecording(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	// One segment much wider than the event so the whole window is covered.
	end := time.Now().UTC().Add(-30 * time.Second)
	started := end.Add(-20 * time.Second)
	seedSegment(t, fixture, started.Add(-time.Minute), end.Add(time.Minute))
	if err := service.Ingest(ctx, frigatePayload("new", "evt-linked", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	ended := float64(end.Unix())
	if err := service.Ingest(ctx, frigatePayload("update", "evt-linked", "front-door", "person", float64(started.Unix()), &ended, nil, true)); err != nil {
		t.Fatal(err)
	}
	state, mediaStart := eventMediaState(t, fixture, "evt-linked")
	if state != "linked" {
		t.Fatalf("footage covering the window must link, got %q", state)
	}
	if mediaStart == nil {
		t.Fatal("a linked event must record the span that actually exists")
	}
}

// A single short segment near the event is real footage but not the whole
// window. Reporting it as linked puts a play button on a range that ends early;
// reporting unavailable hides footage that exists.
func TestEventReportsPartialCoverage(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	// The event starts 10 minutes ago; the only segment is the 10 seconds right
	// at it, so neither the lead-in nor the lead-out is covered.
	started := time.Now().UTC().Add(-10 * time.Minute)
	seedSegment(t, fixture, started, started.Add(10*time.Second))
	if err := service.Ingest(ctx, frigatePayload("new", "evt-partial", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	ended := float64(started.Add(10 * time.Second).Unix())
	if err := service.Ingest(ctx, frigatePayload("update", "evt-partial", "front-door", "person", float64(started.Unix()), &ended, nil, false)); err != nil {
		t.Fatal(err)
	}
	state, mediaStart := eventMediaState(t, fixture, "evt-partial")
	if state != "partial" {
		t.Fatalf("footage that covers part of the window must be partial, got %q", state)
	}
	if mediaStart == nil {
		t.Fatal("a partial event must still record what exists so playback can seek there")
	}
}

// A channel that was not recording says so. The event still exists with its
// time and channel — REC-10 requires the timestamp to survive for manual lookup
// on another NVR.
func TestEventWithoutFootageReportsUnavailable(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-bare", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	state, mediaStart := eventMediaState(t, fixture, "evt-bare")
	if state != "unavailable" {
		t.Fatalf("no segments means unavailable, got %q", state)
	}
	if mediaStart != nil {
		t.Fatal("an unavailable event must not claim a span")
	}
}

// The segment containing the event is published after the event is stored, so
// the first verdict is "pending". The reconcile pass is what turns it into
// linked without waiting for the event to change again.
func TestEventMediaReconcileLinksLateFootage(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := now.Add(-30 * time.Second)
	ended := now.Add(-20 * time.Second)
	endedUnix := float64Of(ended)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-late", "front-door", "person", float64(started.Unix()), &endedUnix, nil, false)); err != nil {
		t.Fatal(err)
	}
	if state, _ := eventMediaState(t, fixture, "evt-late"); state != "unavailable" {
		t.Fatalf("before any segment exists the verdict is unavailable, got %q", state)
	}
	// Force the row back to pending the way a mid-segment event would be, then
	// publish footage and reconcile.
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE events SET media_state='pending' WHERE source_id='evt-late'"); err != nil {
		t.Fatal(err)
	}
	seedSegment(t, fixture, started.Add(-time.Minute), ended.Add(time.Minute))
	service.ReconcileEventMedia(ctx)
	state, mediaStart := eventMediaState(t, fixture, "evt-late")
	if state != "linked" {
		t.Fatalf("reconcile must link footage published after the event, got %q", state)
	}
	if mediaStart == nil {
		t.Fatal("reconcile must record the available span")
	}
}

func float64Of(t time.Time) float64 { return float64(t.Unix()) }
