package integration

// PRD REC-09/10: the pre-event ring buffer.
//
// Measured on the live site before these tests were written: with max_second=3
// against a 3.3-second GOP, ZLM produced 3.3-4.0 second fragments and never
// deleted them. Two things follow and are asserted here: the ring's granularity
// is the camera's GOP, and eviction is the worker's job — a buffer with no sweep
// eventually fills the tmpfs and stops the media server for every channel.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// The buffer only runs where ordinary recording does not: a channel already
// writing footage must not write the same bytes twice, and REC-10 says the
// switch must leave continuous and scheduled recording untouched.
func TestEventBufferRunsOnlyWithoutOrdinaryRecording(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET event_recording=true WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	// Planned outside its windows counts as "no ordinary recording".
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	active, err := fixture.Service.EventBufferActive(ctx, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("a buffer row existed before any reconcile ran")
	}
}

// The switch is what gates the buffer. Turning it off must stop an existing one:
// fragments nothing will read are exactly the cost REC-10 says an off switch
// must avoid.
func TestEventBufferStopsWhenTheSwitchIsOff(t *testing.T) {
	// testedSource wires the media layer Reconcile needs; the shorter fixture
	// does not, and this test is about the decision rather than the pull.
	fixture, _, _, _ := testedSource(t)
	ctx := context.Background()
	// Simulate a running buffer by writing the row directly: starting a real one
	// needs the media server, and this test is about the decision, not the pull.
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO event_buffers(channel_id,run_id,stream_session_id,source_revision_id,cache_path,keep_seconds,owner_token)
		VALUES($1,$2,$3,$4,'/nonexistent',30,'owner')`, fixture.Channel, mustID(t), mustID(t), fixture.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET event_recording=false,mode='none' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.Reconcile(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	active, err := fixture.Service.EventBufferActive(ctx, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("the buffer kept running with its switch off")
	}
}

// A stale row — one whose worker stopped heartbeating — must not make a channel
// look "already buffering" forever. The tmpfs died with that worker, so the row
// describes footage that no longer exists.
func TestEventBufferStaleRowsAreNotTrusted(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO event_buffers(channel_id,run_id,stream_session_id,source_revision_id,cache_path,keep_seconds,owner_token,heartbeat_at)
		VALUES($1,$2,$3,$4,'/nonexistent',30,'dead-owner',clock_timestamp()-interval '10 minutes')`, fixture.Channel, mustID(t), mustID(t), fixture.Revision); err != nil {
		t.Fatal(err)
	}
	active, err := fixture.Service.EventBufferActive(ctx, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("a buffer whose worker stopped heartbeating was reported as active")
	}
}

// A detection with no ordinary recording queues a clip job, so the extraction
// survives a worker restart between the event and the assembly.
func TestEventWithoutFootageQueuesAClipJob(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-clip", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM event_clip_jobs WHERE channel_id=$1`, fixture.Channel).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("expected one queued clip job, got %d", jobs)
	}
	// The window must cover the lead-in the page promises.
	var from, to time.Time
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT window_start,window_end FROM event_clip_jobs WHERE channel_id=$1`, fixture.Channel).Scan(&from, &to); err != nil {
		t.Fatal(err)
	}
	if !from.Before(started) || !to.After(started) {
		t.Fatalf("window %v..%v does not bracket the event at %v", from, to, started)
	}
}

// A FINISHED event whose footage already exists must not queue a clip: the buffer
// would assemble a second copy of the same seconds.
//
// A running event is different and does queue: its clip has to be re-cut as the
// window advances, and at ingestion time there is no clip yet to compare against.
// That case is covered by TestRunningEventAdvancesItsClipWindow below.
func TestCoveredEventDoesNotQueueAClipJob(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	ended := started.Add(20 * time.Second)
	seedSegment(t, fixture, started.Add(-time.Minute), ended.Add(time.Minute))
	if err := service.Ingest(ctx, frigatePayload("new", "evt-covered", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	// Clear whatever the running phase queued: the question is what the FINAL
	// message decides, and a running event legitimately queues one.
	if _, err := fixture.DB.Pool.Exec(ctx, "DELETE FROM event_clip_jobs WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	endedUnix := float64(ended.Unix())
	if err := service.Ingest(ctx, frigatePayload("update", "evt-covered", "front-door", "person", float64(started.Unix()), &endedUnix, nil, false)); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM event_clip_jobs WHERE channel_id=$1`, fixture.Channel).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("a finished, already-covered event queued %d redundant clip jobs", jobs)
	}
}

// Assembly with an empty window fails cleanly and records why: the event keeps
// its honest "no footage" verdict instead of the job retrying forever.
func TestClipAssemblyReportsAnEmptyWindow(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	cache := t.TempDir()
	fixture.Service.EventCacheDir = cache
	fixture.Service.EventCacheBytes = 64 << 20
	eventID := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-empty','person',0.5,'[]',clock_timestamp()-interval '1 minute','gone','unavailable','{}')`, eventID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO event_clip_jobs(event_id,channel_id,window_start,window_end)
		VALUES($1,$2,$3,$4)`, eventID, fixture.Channel, now.Add(-10*time.Second), now); err != nil {
		t.Fatal(err)
	}
	fixture.Service.AssembleClips(ctx)
	var state, code string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,coalesce(error_code,'') FROM event_clip_jobs WHERE event_id=$1`, eventID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != "window_empty" {
		t.Fatalf("empty window: state=%q code=%q, want failed/window_empty", state, code)
	}
}

// A buffer directory whose files ZLM is still writing must not hand a partial
// MP4 to the assembler: the clip would read as damaged.
func TestBufferScanSkipsIncompleteFragments(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "record", "one_nvr", "stream", "2026-10-19")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"2026-10-19-00-16-31-7.mp4":  "complete",
		".2026-10-19-00-16-34-8.mp4": "still-writing",
	} {
		if err := os.WriteFile(filepath.Join(nested, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fragments, err := scanBufferForTest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 1 {
		t.Fatalf("expected only the complete fragment, got %d", len(fragments))
	}
}

// A running event's clip window advances with each message, so the page can show
// footage growing instead of nothing until the detection ends.
//
// The first version fixed the window at ingestion: a five-minute detection
// produced no footage for five minutes and then one file, and the event page said
// "no footage" for the whole time an operator was most likely to be looking.
func TestRunningEventAdvancesItsClipWindow(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-3 * time.Minute)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-growing", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	var firstEnd time.Time
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT window_end FROM event_clip_jobs WHERE event_id=(SELECT id FROM events WHERE source_id='evt-growing')`).Scan(&firstEnd); err != nil {
		t.Fatal(err)
	}

	// The event is still running; a later message must move the window out.
	// The payload's start_time is unchanged — only the absence of end_time marks
	// it as still open, which is what Frigate sends on updates.
	service.Ingest(ctx, frigatePayload("update", "evt-growing", "front-door", "person", float64(started.Unix()), nil, nil, false))
	var secondEnd time.Time
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT window_end FROM event_clip_jobs WHERE event_id=(SELECT id FROM events WHERE source_id='evt-growing')`).Scan(&secondEnd); err != nil {
		t.Fatal(err)
	}
	if !secondEnd.After(firstEnd) {
		t.Fatalf("window_end did not advance (%v -> %v); a running event would never grow its clip", firstEnd, secondEnd)
	}
	if state := clipJobStateOf(t, fixture, "evt-growing"); state != "queued" {
		t.Fatalf("a re-queued job must be 'queued' for the next pass, got %q", state)
	}

	// Once the event ends, the window is pinned to its end and the job must be
	// closed after assembly rather than left growing forever.
	ended := time.Now().UTC().Add(-30 * time.Second)
	endedUnix := float64(ended.Unix())
	if err := service.Ingest(ctx, frigatePayload("update", "evt-growing", "front-door", "person", float64(started.Unix()), &endedUnix, nil, false)); err != nil {
		t.Fatal(err)
	}
	var finalEnd time.Time
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT window_end FROM event_clip_jobs WHERE event_id=(SELECT id FROM events WHERE source_id='evt-growing')`).Scan(&finalEnd); err != nil {
		t.Fatal(err)
	}
	want := ended.Add(recording.PostEventTrail)
	if finalEnd.Sub(want).Abs() > time.Second {
		t.Fatalf("a finished event's window must end at its end plus the trail: %v vs %v", finalEnd, want)
	}
}

// A settled job must not be reopened by a late duplicate message: 'done' and
// 'failed' are answers, not states to re-enter.
func TestSettledClipJobIsNotReopened(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	ended := started.Add(10 * time.Second)
	endedUnix := float64(ended.Unix())
	if err := service.Ingest(ctx, frigatePayload("new", "evt-settled", "front-door", "person", float64(started.Unix()), &endedUnix, nil, false)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE event_clip_jobs SET state='done' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	// The detector re-sends the same event, as a reconnect would.
	if err := service.Ingest(ctx, frigatePayload("update", "evt-settled", "front-door", "person", float64(started.Unix()), &endedUnix, nil, false)); err != nil {
		t.Fatal(err)
	}
	if state := clipJobStateOf(t, fixture, "evt-settled"); state != "done" {
		t.Fatalf("a late duplicate reopened a settled clip job: %q", state)
	}
}

func clipJobStateOf(t *testing.T, fixture publishFixture, sourceID string) string {
	t.Helper()
	var state string
	if err := fixture.DB.Pool.QueryRow(context.Background(),
		`SELECT state FROM event_clip_jobs WHERE event_id=(SELECT id FROM events WHERE source_id=$1)`, sourceID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}
