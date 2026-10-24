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

// A segment published after the event closed must upgrade the verdict.
//
// This is the normal order on a real site: the detection ends, the "end" message
// is written, and only then does the recorder publish the segment covering its
// tail. The verdict written at ingestion is therefore "unavailable", and it must
// not be treated as final. The old reconcile only re-read rows in 'pending' — a
// state the upsert never writes — so a covered event stayed "本机无录像" for good.
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
	// The segment arrives after the event was stored. No state is doctored here:
	// the row is exactly what the ingest path writes.
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

// Footage that will never arrive must not stay "pending" forever. Before this,
// only a positive answer was written, so an event from a channel that was not
// recording sat at "录像待归档" indefinitely — the page told the operator to
// wait for something that was never coming.
//
// The cutoff is two hours: an event older than that with no segments is final.
func TestEventMediaReconcileSettlesOldPendingEvents(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	// An event from five hours ago, stored as pending as if footage were coming.
	old := time.Now().UTC().Add(-5 * time.Hour)
	ended := old.Add(10 * time.Second)
	endedUnix := float64(ended.Unix())
	if err := service.Ingest(ctx, frigatePayload("new", "evt-stale", "front-door", "person", float64(old.Unix()), &endedUnix, nil, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE events SET media_state='pending' WHERE source_id='evt-stale'"); err != nil {
		t.Fatal(err)
	}
	service.ReconcileEventMedia(ctx)
	state, _ := eventMediaState(t, fixture, "evt-stale")
	if state != "unavailable" {
		t.Fatalf("an old event with no footage must be settled as unavailable, got %q", state)
	}
}

// A recent event with no footage yet must stay pending: the segment may still
// be publishing, and "no footage" would be a false negative.
func TestEventMediaReconcileKeepsRecentPendingEventsPending(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().UTC().Add(-2 * time.Minute)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-waiting", "front-door", "person", float64(recent.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE events SET media_state='pending' WHERE source_id='evt-waiting'"); err != nil {
		t.Fatal(err)
	}
	service.ReconcileEventMedia(ctx)
	state, _ := eventMediaState(t, fixture, "evt-waiting")
	if state != "pending" {
		t.Fatalf("a recent event must stay pending while its segment may still publish, got %q", state)
	}
}

// An event whose clip is still growing must stay 'linked', not flip back to
// 'unavailable'.
//
// A running event's clip is 'finalizing' until the event ends — it is footage that
// exists and the page plays it while it grows. Counting only 'ready' segments made
// every MQTT update (about one per second) overwrite the verdict with
// 'unavailable', while the assembler wrote 'linked' every five seconds. The card
// flickered between 本机无录像 and 录像生成中 for the whole life of a running
// event — which is exactly the case event recording exists for, since it is only
// offered on channels with no ordinary recording.
func TestGrowingClipKeepsTheEventLinked(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(context.Background(), fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-growing-media", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}

	// A clip assembled from the buffer while the event runs: 'finalizing'.
	runID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	var sessionID id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT stream_session_id FROM recording_runs WHERE id=$1`, fixture.Run).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state)
		VALUES($1,$2,$3,$4,$5,$6,$7,'event','stopped')`,
		runID, fixture.Site.Secrets.SiteID, fixture.Channel, fixture.Revision, sessionID, fixture.Pool.ID, ".work/event/"+string(runID)); err != nil {
		t.Fatal(err)
	}
	segment, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	path := "recordings/" + string(segment) + ".mp4"
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
		original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,state)
		VALUES($1,$2,$3,$4,$5,$6,$6,$7,'UTC',0,$7,clock_timestamp(),1024,'{}','finalizing')`,
		segment, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, path, started.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}

	// A later MQTT update re-evaluates the link. The growing clip must count.
	if err := service.Ingest(ctx, frigatePayload("update", "evt-growing-media", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	state, _ := eventMediaState(t, fixture, "evt-growing-media")
	if state != "linked" && state != "partial" {
		t.Fatalf("media_state = %q while the clip was still growing; the card would say 本机无录像", state)
	}
}
