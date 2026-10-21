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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
)

// markBuffering makes the channel look like it has a live ring buffer, which is
// the gate queueClipJob applies: a channel that is not buffering has nothing to
// assemble, and recording that answer once per detection would grow a table
// nobody reads. Tests about the window/queue behaviour must state this
// precondition rather than rely on the gate being absent.
func markBuffering(t *testing.T, fixture publishFixture) {
	t.Helper()
	ctx := context.Background()
	var session id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT stream_session_id FROM recording_runs WHERE id=$1`, fixture.Run).Scan(&session); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO event_buffers(channel_id,run_id,stream_session_id,source_revision_id,cache_path,keep_seconds,owner_token)
		VALUES($1,$2,$3,$4,'/nonexistent',30,'owner')`, fixture.Channel, mustID(t), session, fixture.Revision); err != nil {
		t.Fatal(err)
	}
}

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
	if _, err := fixture.Service.PutSchedule(ctx, fixture.Admin, fixture.Channel, nil, channelVersion(t, fixture)); err != nil {
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
	markBuffering(t, fixture)
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

// Assembly with an empty window records why and stays retryable while the event
// is young.
//
// The earlier version of this test asserted 'failed'. That was the bug: an empty
// window usually means the ring has not closed a fragment yet — an event detected
// in the first seconds after a worker restart — and 'failed' is never reopened, so
// footage arriving moments later was never assembled. Retry is bounded by the
// ring's keep window (see TestEmptyWindowStaysRetryableForARecentEvent for the old
// -event side).
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
	seedClipJob(ctx, t, fixture.DB, eventID, fixture.Channel, now.Add(-10*time.Second), now, "queued")
	fixture.Service.AssembleClips(ctx)
	var state, code string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,coalesce(error_code,'') FROM event_clip_jobs WHERE event_id=$1`, eventID).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || code != "window_empty" {
		t.Fatalf("empty window: state=%q code=%q, want queued/window_empty (retryable, reason recorded)", state, code)
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
	markBuffering(t, fixture)
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
	// The trail is a site setting now, so ask the site rather than a Go constant:
	// asserting against the constant would keep passing if the migration's default
	// drifted away from it.
	var trailSeconds int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT event_clip_trail_seconds FROM sites WHERE singleton`).Scan(&trailSeconds); err != nil {
		t.Fatal(err)
	}
	want := ended.Add(time.Duration(trailSeconds) * time.Second)
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
	markBuffering(t, fixture)
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

// A channel that is not buffering must not queue clip jobs: the assembler can
// only answer "no window", and recording that answer once per detection per
// channel grows a table nobody reads. Measured on the live site: three such rows
// within twenty minutes on channels that had never enabled the switch.
func TestEventWithoutABufferQueuesNothing(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-nobuf", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM event_clip_jobs WHERE channel_id=$1`, fixture.Channel).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("a channel with no buffer queued %d clip jobs that can only fail", jobs)
	}
}

// The same event on a buffering channel does queue: the difference is the buffer,
// not the event.
func TestBufferingChannelQueuesClipWork(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO event_buffers(channel_id,run_id,stream_session_id,source_revision_id,cache_path,keep_seconds,owner_token)
		VALUES($1,$2,$3,$4,'/tmp',30,'test-owner')`, fixture.Channel, mustID(t), mustID(t), fixture.Revision); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-buf", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM event_clip_jobs WHERE channel_id=$1`, fixture.Channel).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("a buffering channel queued %d jobs, want 1", jobs)
	}
}

// A stale buffer row must not count: its tmpfs died with the worker that owned it,
// so queueing against it would assemble footage that no longer exists.
func TestStaleBufferDoesNotQueueClipWork(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO event_buffers(channel_id,run_id,stream_session_id,source_revision_id,cache_path,keep_seconds,owner_token,heartbeat_at)
		VALUES($1,$2,$3,$4,'/tmp',30,'dead-owner',clock_timestamp()-interval '10 minutes')`, fixture.Channel, mustID(t), mustID(t), fixture.Revision); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-2 * time.Minute)
	if err := service.Ingest(ctx, frigatePayload("new", "evt-stalebuf", "front-door", "person", float64(started.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM event_clip_jobs WHERE channel_id=$1`, fixture.Channel).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("a stale buffer row queued %d jobs against footage that is gone", jobs)
	}
}

// Two detections on one visit share one recording, driven through Ingest — the
// path the detector actually uses.
//
// TestOneActivityStretchProducesOneClip exercises recording.AttachEvent, but the
// worker never calls that: events.queueClipJob is a second implementation of the
// same rule (written in SQL because events must not import recording). A test of
// only the first would leave the operator's rule covered by the copy nobody runs.
func TestTwoDetectionsOnOneVisitShareARecording(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	markBuffering(t, fixture)
	// Realtime, so the first event's close is still ahead when the second arrives —
	// that is the condition the rule is about. A base in the past would make every
	// window already closed and assert the other branch.
	firstStart := time.Now().UTC().Add(-6 * time.Second).Truncate(time.Second)
	secondStart := firstStart.Add(5 * time.Second)
	firstEnd := firstStart.Add(2 * time.Second)
	secondEnd := secondStart.Add(3 * time.Second)

	firstEndUnix := float64(firstEnd.Unix())
	if err := service.Ingest(ctx, frigatePayload("new", "evt-visit-a", "front-door", "person",
		float64(firstStart.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	if err := service.Ingest(ctx, frigatePayload("update", "evt-visit-a", "front-door", "person",
		float64(firstStart.Unix()), &firstEndUnix, nil, false)); err != nil {
		t.Fatal(err)
	}
	// The second event begins inside the first recording's trail window, so it must
	// extend that recording rather than start another.
	secondEndUnix := float64(secondEnd.Unix())
	if err := service.Ingest(ctx, frigatePayload("new", "evt-visit-b", "front-door", "person",
		float64(secondStart.Unix()), nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	if err := service.Ingest(ctx, frigatePayload("update", "evt-visit-b", "front-door", "person",
		float64(secondStart.Unix()), &secondEndUnix, nil, false)); err != nil {
		t.Fatal(err)
	}

	var windows int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM clip_windows WHERE channel_id=$1`, fixture.Channel).Scan(&windows); err != nil {
		t.Fatal(err)
	}
	if windows != 1 {
		var detail string
		_ = fixture.DB.Pool.QueryRow(ctx, `SELECT string_agg(window_start::text||' .. '||window_end::text||' '||state, ' | ' ORDER BY window_start)
			FROM clip_windows WHERE channel_id=$1`, fixture.Channel).Scan(&detail)
		t.Fatalf("two detections on one visit produced %d recordings, want 1: the second arrived inside the first one's trail window; windows: %s", windows, detail)
	}
	// Both anchors point at that one recording.
	var shared, jobs int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(DISTINCT window_id), count(*) FROM event_clip_jobs WHERE channel_id=$1`, fixture.Channel).Scan(&shared, &jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 2 || shared != 1 {
		t.Fatalf("%d events produced %d distinct windows, want 2 events on 1 window", jobs, shared)
	}
	// The recording must reach the last event's end plus the trail: extending means
	// the window grows, not that it keeps the first event's close.
	var covers bool
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT window_end >= $2::timestamptz + interval '10 seconds'
		FROM clip_windows WHERE channel_id=$1`, fixture.Channel, secondEnd).Scan(&covers); err != nil {
		t.Fatal(err)
	}
	if !covers {
		t.Fatal("the recording did not extend to the second event's end plus the trail")
	}
}

// Two detections far apart stay two recordings, through the same Ingest path.
func TestDetectionsFarApartStaySeparateRecordings(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	markBuffering(t, fixture)
	// Both events are replayed from the past, so each "new" message arrives with no
	// end_time. A running event's close is measured from NOW, deliberately — waiting
	// for the detector to close it would stall the window — so replaying two old
	// events a second apart would give both a close of now+trail and merge them.
	// Each event has to be driven to its stored end before the next one arrives, so
	// its close lands at its own end + trail; the second one is a minute later and
	// therefore starts a second recording.
	firstStart := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	firstEnd := firstStart.Add(10 * time.Second)
	secondStart := firstEnd.Add(60 * time.Second)
	secondEnd := secondStart.Add(10 * time.Second)
	for _, item := range []struct {
		sourceID string
		start    time.Time
		end      time.Time
	}{
		{"evt-sep-a", firstStart, firstEnd},
		{"evt-sep-b", secondStart, secondEnd},
	} {
		endUnix := float64(item.end.Unix())
		if err := service.Ingest(ctx, frigatePayload("new", item.sourceID, "front-door", "person",
			float64(item.start.Unix()), &endUnix, nil, false)); err != nil {
			t.Fatal(err)
		}
	}
	var windows int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM clip_windows WHERE channel_id=$1`, fixture.Channel).Scan(&windows); err != nil {
		t.Fatal(err)
	}
	if windows != 2 {
		var detail string
		_ = fixture.DB.Pool.QueryRow(ctx, `SELECT string_agg(window_start::text||' .. '||window_end::text, ' | ' ORDER BY window_start)
			FROM clip_windows WHERE channel_id=$1`, fixture.Channel).Scan(&detail)
		t.Fatalf("two distant detections produced %d recordings, want 2; windows: %s", windows, detail)
	}
}

// Settled jobs are swept; live ones are not, however old they look.
func TestClipJobRetentionKeepsLiveWork(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	fixture.Service.EventCacheDir = t.TempDir()
	eventID := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-ret','person',0.5,'[]',clock_timestamp(),'gone','unavailable','{}')`, eventID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for state, age := range map[string]time.Duration{
		"done":    30 * 24 * time.Hour,
		"failed":  30 * 24 * time.Hour,
		"growing": 30 * 24 * time.Hour,
		"queued":  30 * 24 * time.Hour,
	} {
		ev := mustID(t)
		if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
			VALUES($1,$2,'frigate',$3,'person',0.5,'[]',clock_timestamp(),'gone','unavailable','{}')`, ev, fixture.Channel, "src-"+state); err != nil {
			t.Fatal(err)
		}
		windowID := seedClipJob(ctx, t, fixture.DB, ev, fixture.Channel, now.Add(-time.Minute), now, state)
		if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET updated_at=$2 WHERE event_id=$1`, ev, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
		_, _ = fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows SET updated_at=$2 WHERE id=$1`, windowID, now.Add(-age))
	}
	fixture.Service.SweepEventClipJobs(ctx)
	var remaining int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM event_clip_jobs WHERE channel_id=$1`, fixture.Channel).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	// Only the two live rows survive.
	if remaining != 2 {
		t.Fatalf("%d jobs remained; settled ones must be swept and live ones kept", remaining)
	}
}

// Toggling the event-recording switch must not fail with a version conflict.
//
// The caller's token comes from GET recording-policy, which reports
// channels.version; the first version of this endpoint compared it against
// recording_policies.version instead. The two counters advance independently, so
// every toggle returned 409 and the switch could not be turned on at all — the
// defect the operator saw as "配置已被其他人修改，请刷新后重试".
func TestEventRecordingToggleUsesTheChannelVersion(t *testing.T) {
	// testedSource wires Sources; the lighter fixture does not.
	fixture, _, _, _ := testedSource(t)
	ctx := context.Background()
	// The fixture starts with the switch on, so start from off: the point is a
	// real change, and toggling to the value it already holds is a no-op that
	// never exercises the version check.
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET event_recording=false WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	policy, err := fixture.Service.Sources.GetPolicy(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Version < 1 {
		t.Fatalf("the policy read returned version %d", policy.Version)
	}
	if err := fixture.Service.Sources.SetEventRecording(ctx, fixture.Admin, fixture.Channel, true, policy.Version); err != nil {
		t.Fatalf("toggling with the version the read endpoint returned failed: %v", err)
	}
	// The switch is on, and the next read reports it with a NEW version — a
	// second toggle with the old token must be rejected, or concurrent edits
	// would silently overwrite each other.
	after, err := fixture.Service.Sources.GetPolicy(ctx, fixture.Admin, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	if !after.EventRecordingEnabled {
		t.Fatal("the switch did not turn on")
	}
	if after.Version == policy.Version {
		t.Fatal("the version did not advance; a repeat toggle would reuse a stale token")
	}
	if err := fixture.Service.Sources.SetEventRecording(ctx, fixture.Admin, fixture.Channel, false, policy.Version); err == nil {
		t.Fatal("a stale version was accepted")
	}
	// And the new token works.
	if err := fixture.Service.Sources.SetEventRecording(ctx, fixture.Admin, fixture.Channel, false, after.Version); err != nil {
		t.Fatalf("toggling with the fresh version failed: %v", err)
	}
}

// writeSyntheticClip writes a short real MP4 for the repair pass to probe: the
// pass restores a clip only when ffprobe can read it, so a byte string would look
// exactly like a file that is actually broken.
func writeSyntheticClip(t *testing.T, path string) time.Duration {
	t.Helper()
	ffmpeg := mediaTestTool(t, "ffmpeg")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=5", "-t", "2",
		"-c:v", "libx264", "-threads", "1", "-y", path).Run(); err != nil {
		t.Fatal("synthetic clip creation failed", err)
	}
	return 2 * time.Second
}

// A clip that already exists must be EXTENDED, never rebuilt from the ring.
//
// Measured on the live site: one event produced seven files for the same run —
// CH03_20261019_155243, _155250, _155253, _155257, _155303, _155306, _155313 — and
// the assembled file shrank from 13.7 MB to 10.9 MB. The ring keeps thirty
// seconds, so an event longer than that evicts its own beginning; every re-cut
// then started later, named a different file, and left the previous one orphaned.
// The index kept pointing at the FIRST name while file_identity and size_bytes
// described the LAST, which the storage reconciler correctly reported as "the
// file was replaced" — every long event ended up damaged.
func TestClipRecutExtendsTheSameFile(t *testing.T) {
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ffprobe := mediaTestTool(t, "ffprobe")
	fixture := scheduledFixture(t)
	ctx := context.Background()
	cache := t.TempDir()
	fixture.Service.EventCacheDir = cache
	fixture.Service.EventCacheBytes = 512 << 20
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: ffprobe}

	// ZLM writes the fragments as it goes; the test writes the same shape. The
	// names are container-local, which for this fixture is UTC because the site is.
	ring := filepath.Join(cache, "events", string(fixture.Channel))
	if err := os.MkdirAll(ring, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Second)
	writeFragment := func(offset int) {
		start := base.Add(time.Duration(offset) * time.Second)
		// ZLM names a fragment in the CONTAINER's local zone. Formatting in UTC
		// here made the parser read every name eight hours early on a +08 machine,
		// which is the same mismatch the production fix addressed.
		name := start.In(time.Local).Format("2006-01-02-15-04-05") + "-0.mp4"
		path := filepath.Join(ring, name)
		if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=5", "-t", "3",
			"-c:v", "libx264", "-threads", "1", "-y", path).Run(); err != nil {
			t.Fatal("synthetic fragment creation failed", err)
		}
		// The parser reads a fragment's end from its mtime, because that is when
		// ZLM closed the file. ffmpeg writes "now" instead, so the fixture sets
		// the real end explicitly — otherwise every fragment looks minutes long
		// and the clip's covered span swallows material that is still pending.
		end := start.Add(3 * time.Second)
		if err := os.Chtimes(path, end, end); err != nil {
			t.Fatal(err)
		}
	}
	writeFragment(0)
	writeFragment(3)

	eventID := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-recut','person',0.5,'[]',$3,'gone','unavailable','{}')`, eventID, fixture.Channel, base); err != nil {
		t.Fatal(err)
	}
	seedClipJob(ctx, t, fixture.DB, eventID, fixture.Channel, base, base.Add(6*time.Second), "queued")
	fixture.Service.AssembleClips(ctx)

	var (
		firstPath  string
		firstStart time.Time
		firstSize  int64
		firstName  string
	)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT target_relative_path,original_start,size_bytes
		FROM recording_segments WHERE run_id=(SELECT clip_run_id FROM event_clip_jobs WHERE event_id=$1)`, eventID).
		Scan(&firstPath, &firstStart, &firstSize); err != nil {
		t.Fatal(err)
	}
	firstName = filepath.Base(firstPath)

	// The event keeps running: two more fragments land and the job re-queues.
	writeFragment(6)
	writeFragment(9)
	// A running event: the close is still ahead, so the assembler re-cuts rather
	// than settling. A close in the past is a finished recording and only gets
	// published — correct behaviour, and not what this test is about. The start
	// stays where the fragments are: moving it would put them outside the window.
	openEnd := base.Add(20 * time.Second)
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows SET state='open',window_end=$2
		WHERE id=(SELECT window_id FROM event_clip_jobs WHERE event_id=$1)`, eventID, openEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET state='queued',window_end=$2 WHERE event_id=$1`, eventID, openEnd); err != nil {
		t.Fatal(err)
	}
	// The ring evicts the oldest fragments, exactly as the sweeper would: this is
	// what made a rebuild produce a SMALLER file and a later start.
	for _, offset := range []int{0, 3} {
		name := base.Add(time.Duration(offset)*time.Second).In(time.Local).Format("2006-01-02-15-04-05") + "-0.mp4"
		if err := os.Remove(filepath.Join(ring, name)); err != nil {
			t.Fatal(err)
		}
	}
	fixture.Service.AssembleClips(ctx)
	var (
		secondPath  string
		secondStart time.Time
		secondSize  int64
	)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT target_relative_path,original_start,size_bytes
		FROM recording_segments WHERE run_id=(SELECT clip_run_id FROM event_clip_jobs WHERE event_id=$1)`, eventID).
		Scan(&secondPath, &secondStart, &secondSize); err != nil {
		t.Fatal(err)
	}
	if secondPath != firstPath {
		t.Fatalf("a re-cut must land on the same file:\n  first  %s\n  second %s", firstPath, secondPath)
	}
	if !secondStart.Equal(firstStart) {
		t.Fatalf("a re-cut moved the clip's start from %v to %v; the ring had evicted the beginning", firstStart, secondStart)
	}
	if secondSize <= firstSize {
		t.Fatalf("the clip did not grow (first %d bytes, second %d): the beginning was dropped and only the tail survived", firstSize, secondSize)
	}
	// One file on disk under the run, not one per assembly.
	runSuffix := "_" + fileNameRun(firstName) + ".mp4"
	entries, err := os.ReadDir(filepath.Dir(filepath.Join(fixture.Pool.Path, secondPath)))
	if err != nil {
		t.Fatal(err)
	}
	var owned []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), runSuffix) {
			owned = append(owned, entry.Name())
		}
	}
	if len(owned) != 1 {
		t.Fatalf("the same run left %d files on disk, expected exactly 1: %v", len(owned), owned)
	}

	// The stored identity must describe the file that is actually there. This is
	// the verdict the storage reconciler applies: if it disagrees, the clip shows
	// as 损坏 on every page even though it plays fine.
	var rawID []byte
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT file_identity FROM recording_segments
		WHERE run_id=(SELECT clip_run_id FROM event_clip_jobs WHERE event_id=$1)`, eventID).Scan(&rawID); err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		Device, Inode uint64
		Size          int64
	}
	if err := json.Unmarshal(rawID, &recorded); err != nil {
		t.Fatalf("file_identity is not a JSON identity: %v (%s)", err, rawID)
	}
	disk := filepath.Join(fixture.Pool.Path, secondPath)
	info, err := os.Stat(disk)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no inode on this platform")
	}
	if recorded.Inode != uint64(stat.Ino) || recorded.Device != uint64(stat.Dev) || recorded.Size != info.Size() {
		t.Fatalf("stored identity does not describe the file on disk:\n  recorded %+v\n  on disk  inode=%d dev=%d size=%d",
			recorded, stat.Ino, stat.Dev, info.Size())
	}
	// The fragment count accumulates instead of resetting to the newest tail: the
	// page's "N 段" must describe the whole clip.
	var fragments int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT coalesce((media_evidence->>'buffer_fragments')::int,0)
		FROM recording_segments WHERE run_id=(SELECT clip_run_id FROM event_clip_jobs WHERE event_id=$1)`, eventID).Scan(&fragments); err != nil {
		t.Fatal(err)
	}
	if fragments != 4 {
		t.Fatalf("clip holds 4 fragments (two per assembly), evidence says %d", fragments)
	}
	// The bytes must actually accumulate. A remux that dropped the head would
	// still produce a larger file than the first assembly in some cases (the ring
	// shifts forward), so the duration is what proves the whole event is present.
	probeRunner := probe.Runner{FFprobe: ffprobe}
	file, err := os.Open(disk)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := probeRunner.InspectMP4(ctx, file)
	file.Close()
	if err != nil {
		t.Fatalf("the published clip is not a readable MP4: %v", err)
	}
	if evidence.Duration < 11*time.Second || evidence.Duration > 13*time.Second {
		t.Fatalf("clip duration = %v, want about 12s (four 3-second fragments)", evidence.Duration)
	}
}

// fileNameRun extracts the run suffix from a clip file name
// (CH01_20261019_155243_<run>.mp4 -> <run>).
func fileNameRun(name string) string {
	trimmed := strings.TrimSuffix(name, ".mp4")
	parts := strings.Split(trimmed, "_")
	if len(parts) < 4 {
		return ""
	}
	return parts[len(parts)-1]
}

// Event clips quarantined by the re-cut defect must be restorable, because the
// file was never damaged.
//
// The defect left size_bytes and file_identity describing the LAST assembly while
// target_relative_path named the FIRST file, so the reconciler compared a correct
// file against evidence about a different one. Measured on the live site: 58 event
// clips reported 损坏 and all 58 files were present. This pass re-reads the
// identity from the file the index points at.
func TestRepairRestoresEventClipsMarkedDamaged(t *testing.T) {
	fixture := scheduledFixture(t)
	fixture.Service.Probe = probe.Runner{FFmpeg: mediaTestTool(t, "ffmpeg"), FFprobe: mediaTestTool(t, "ffprobe")}
	ctx := context.Background()
	clipID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	clipPath := "recordings/" + string(clipID) + ".mp4"
	clipTarget := filepath.Join(fixture.Pool.Path, clipPath)
	writeSyntheticClip(t, clipTarget)
	clipInfo, err := os.Stat(clipTarget)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	// The run must reference a real stream session; fixture.Run is the existing
	// continuous run whose session this fixture created.
	work := ".work/event/" + string(runID)
	var sessionID id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT stream_session_id FROM recording_runs WHERE id=$1`, fixture.Run).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state)
		VALUES($1,$2,$3,$4,$5,$6,$7,'event','stopped')`,
		runID, fixture.Site.Secrets.SiteID, fixture.Channel, fixture.Revision, sessionID, fixture.Pool.ID, work); err != nil {
		t.Fatal(err)
	}
	// The defect's exact shape: correct path, stale size and a foreign identity.
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
		original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,state,error_code)
		VALUES($1,$2,$3,$4,$5,$6,$6,clock_timestamp()-interval '5 minutes','UTC',0,
			clock_timestamp()-interval '5 minutes',clock_timestamp(),$7,'{"Device":1,"Inode":2,"Size":3}','damaged','damaged')`,
		clipID, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, clipPath, clipInfo.Size()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
		VALUES($1,$2,$3,$4,'damaged')`, clipID, fixture.Pool.ID, clipPath, clipInfo.Size()); err != nil {
		t.Fatal(err)
	}

	fixture.Service.RepairEventClipDamage(ctx)

	var (
		state   string
		size    int64
		rawID   []byte
		locSize int64
		locSt   string
	)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT s.state,s.size_bytes,s.file_identity,l.size_bytes,l.state
		FROM recording_segments s JOIN recording_locations l ON l.segment_id=s.id AND l.pool_id=s.pool_id
		WHERE s.id=$1`, clipID).Scan(&state, &size, &rawID, &locSize, &locSt); err != nil {
		t.Fatal(err)
	}
	if state != "ready" || locSt != "ready" {
		t.Fatalf("an intact clip stayed quarantined: segment=%s location=%s", state, locSt)
	}
	if size != clipInfo.Size() || locSize != clipInfo.Size() {
		t.Fatalf("size not realigned with the file: segment=%d location=%d file=%d", size, locSize, clipInfo.Size())
	}
	var recorded struct {
		Device, Inode uint64
		Size          int64
	}
	if err := json.Unmarshal(rawID, &recorded); err != nil {
		t.Fatalf("repaired identity is not valid JSON: %v (%s)", err, rawID)
	}
	stat, ok := clipInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no inode on this platform")
	}
	if recorded.Inode != uint64(stat.Ino) || recorded.Size != clipInfo.Size() {
		t.Fatalf("repaired identity describes a different file: recorded %+v, on disk inode=%d size=%d", recorded, stat.Ino, clipInfo.Size())
	}
}

// A clip whose file is genuinely gone must stay damaged: the repair exists to
// correct stale evidence, not to hide loss.
func TestRepairLeavesMissingClipDamaged(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	runID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	// The run must reference a real stream session; fixture.Run is the existing
	// continuous run whose session this fixture created.
	work := ".work/event/" + string(runID)
	var sessionID id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT stream_session_id FROM recording_runs WHERE id=$1`, fixture.Run).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state)
		VALUES($1,$2,$3,$4,$5,$6,$7,'event','stopped')`,
		runID, fixture.Site.Secrets.SiteID, fixture.Channel, fixture.Revision, sessionID, fixture.Pool.ID, work); err != nil {
		t.Fatal(err)
	}
	clipID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
		original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,state,error_code)
		VALUES($1,$2,$3,$4,$5,'recordings/CH01/2026-10-19/gone.mp4','recordings/CH01/2026-10-19/gone.mp4',
			clock_timestamp()-interval '5 minutes','UTC',0,clock_timestamp()-interval '5 minutes',clock_timestamp(),4096,'{"Device":1,"Inode":2,"Size":4096}','damaged','damaged')`,
		clipID, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
		VALUES($1,$2,'recordings/CH01/2026-10-19/gone.mp4',4096,'damaged')`, clipID, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
	fixture.Service.RepairEventClipDamage(ctx)
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, clipID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "damaged" {
		t.Fatalf("a clip with no file was restored to %q; the repair must not hide loss", state)
	}
}

// The orphan sweep removes the duplicates the re-cut defect left, and only those.
//
// It must never delete the published file: a naming change would otherwise look
// exactly like a pile of orphans.
func TestOrphanSweepRemovesDuplicatesKeepsPublished(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	dir := filepath.Join(fixture.Pool.Path, "recordings", "CH01", "2026-10-19")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	runID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	suffix := "_" + strings.ReplaceAll(string(runID), "-", "") + ".mp4"
	publishedName := "CH01_20261019_155243" + suffix
	for _, name := range []string{
		publishedName,
		"CH01_20261019_155250" + suffix,
		"CH01_20261019_155257" + suffix,
		"CH01_20261019_160000_otherrun.mp4",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
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
	// The segment is created already in the clip run: the publication identity is
	// immutable by trigger, which is exactly the guard that kept the defect from
	// renaming published files.
	segment, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	published := filepath.Join("recordings", "CH01", "2026-10-19", publishedName)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
		original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,state,ready_at)
		VALUES($1,$2,$3,$4,$5,$6,$6,clock_timestamp()-interval '5 minutes','UTC',0,
			clock_timestamp()-interval '5 minutes',clock_timestamp(),1024,'ready',clock_timestamp())`,
		segment, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, published); err != nil {
		t.Fatal(err)
	}

	fixture.Service.SweepEventClipOrphans(ctx)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 2 {
		t.Fatalf("expected the published file and the other run's file to survive, got %v", names)
	}
	want := map[string]bool{publishedName: true, "CH01_20261019_160000_otherrun.mp4": true}
	for _, name := range names {
		if !want[name] {
			t.Fatalf("the sweep removed the wrong file or missed a duplicate: %v", names)
		}
	}
}

// A truncated file must stay damaged: the repair corrects stale evidence, and a
// file far below the size the index recorded is evidence of loss, not of
// staleness. The threshold is a ratio because the defect wrote the same event
// twice and the two files differed by a fraction of a percent.
func TestRepairLeavesTruncatedClipDamaged(t *testing.T) {
	fixture := scheduledFixture(t)
	fixture.Service.Probe = probe.Runner{FFmpeg: mediaTestTool(t, "ffmpeg"), FFprobe: mediaTestTool(t, "ffprobe")}
	ctx := context.Background()
	clipID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	clipPath := "recordings/" + string(clipID) + ".mp4"
	target := filepath.Join(fixture.Pool.Path, clipPath)
	writeSyntheticClip(t, target)
	// Cut the moov box away: the bytes are present but no reader can play them,
	// which is exactly what a file interrupted mid-write looks like.
	full, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, full[:len(full)/4], 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
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
	// The index says the clip was 1 MB; the file is 5 bytes — far below the 90%
	// threshold, so this is loss rather than the defect's two-file variance.
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
		original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,state,error_code)
		VALUES($1,$2,$3,$4,$5,$6,$6,clock_timestamp()-interval '5 minutes','UTC',0,
			clock_timestamp()-interval '5 minutes',clock_timestamp(),1048576,'{"Device":1,"Inode":2,"Size":1048576}','damaged','damaged')`,
		clipID, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, clipPath); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
		VALUES($1,$2,$3,1048576,'damaged')`, clipID, fixture.Pool.ID, clipPath); err != nil {
		t.Fatal(err)
	}
	fixture.Service.RepairEventClipDamage(ctx)
	var state string
	var size int64
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,size_bytes FROM recording_segments WHERE id=$1`, clipID).Scan(&state, &size); err != nil {
		t.Fatal(err)
	}
	if state != "damaged" {
		t.Fatalf("a truncated clip was restored to %q (file %d bytes, index %d); the repair laundered real loss", state, info.Size(), size)
	}
}

// The defect wrote the same event twice, so its files differ by a fraction of a
// percent. The repair must accept that as stale evidence rather than call it
// truncation — measured on the site: 10901509 bytes on disk against 10914971 in
// the index for the same clip.
func TestRepairAcceptsTheDefectsSmallSizeVariance(t *testing.T) {
	fixture := scheduledFixture(t)
	fixture.Service.Probe = probe.Runner{FFmpeg: mediaTestTool(t, "ffmpeg"), FFprobe: mediaTestTool(t, "ffprobe")}
	ctx := context.Background()
	if err := os.MkdirAll(filepath.Join(fixture.Pool.Path, "recordings"), 0o700); err != nil {
		t.Fatal(err)
	}
	clipID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	clipPath := "recordings/" + string(clipID) + ".mp4"
	target := filepath.Join(fixture.Pool.Path, clipPath)
	writeSyntheticClip(t, target)
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
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
	// 0.12% larger than the file on disk, exactly the site's ratio.
	recorded := info.Size() + info.Size()/800
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
		original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,state,error_code)
		VALUES($1,$2,$3,$4,$5,$6,$6,clock_timestamp()-interval '5 minutes','UTC',0,
			clock_timestamp()-interval '5 minutes',clock_timestamp(),$7,'{}','damaged','damaged')`,
		clipID, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, clipPath, recorded); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
		VALUES($1,$2,$3,$4,'damaged')`, clipID, fixture.Pool.ID, clipPath, recorded); err != nil {
		t.Fatal(err)
	}
	fixture.Service.RepairEventClipDamage(ctx)
	var state string
	var size int64
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,size_bytes FROM recording_segments WHERE id=$1`, clipID).Scan(&state, &size); err != nil {
		t.Fatal(err)
	}
	if state != "ready" {
		t.Fatalf("a 0.12%% size variance was treated as truncation (state=%s); the defect wrote the same event twice", state)
	}
	if size != info.Size() {
		t.Fatalf("size not realigned with the file: recorded %d, file %d", size, info.Size())
	}
}

// A running event's clip must not be published: 'ready' is what the archive
// scheduler uploads, what an export concatenates, what the watermark path may
// delete, and what pool usage counts.
//
// PRD PLAY-05: only a formally completed, verified MP4 is playable. REC-09: an
// unfinished MP4 must not be treated as published. Measured before the fix: an
// event lasting a minute uploaded half a minute to the cloud and reported a
// growing segment as finished footage.
func TestRunningEventClipStaysUnpublishedUntilItEnds(t *testing.T) {
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ffprobe := mediaTestTool(t, "ffprobe")
	fixture := scheduledFixture(t)
	ctx := context.Background()
	cache := t.TempDir()
	fixture.Service.EventCacheDir = cache
	fixture.Service.EventCacheBytes = 512 << 20
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: ffprobe}

	ring := filepath.Join(cache, "events", string(fixture.Channel))
	if err := os.MkdirAll(ring, 0o700); err != nil {
		t.Fatal(err)
	}
	// Near now, with the window's close ahead of us: "still running" is now a
	// property of the window, not of the event. A close in the past is a finished
	// recording and only gets published — correct behaviour, and the second half of
	// this test drives exactly that.
	base := time.Now().UTC().Add(-20 * time.Second).Truncate(time.Second)
	writeFragment := func(offset int) {
		start := base.Add(time.Duration(offset) * time.Second)
		path := filepath.Join(ring, start.In(time.Local).Format("2006-01-02-15-04-05")+"-0.mp4")
		if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=5", "-t", "3",
			"-c:v", "libx264", "-threads", "1", "-y", path).Run(); err != nil {
			t.Fatal("synthetic fragment creation failed", err)
		}
		end := start.Add(3 * time.Second)
		if err := os.Chtimes(path, end, end); err != nil {
			t.Fatal(err)
		}
	}
	writeFragment(0)
	writeFragment(3)

	eventID := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-running','person',0.5,'[]',$3,'gone','pending','{}')`, eventID, fixture.Channel, base); err != nil {
		t.Fatal(err)
	}
	seedClipJob(ctx, t, fixture.DB, eventID, fixture.Channel, base, base.Add(40*time.Second), "queued")
	fixture.Service.AssembleClips(ctx)

	segmentStateOf := func() (string, string) {
		t.Helper()
		var seg, loc string
		if err := fixture.DB.Pool.QueryRow(ctx, `SELECT s.state,l.state FROM recording_segments s
			JOIN recording_locations l ON l.segment_id=s.id AND l.pool_id=s.pool_id
			JOIN event_clip_jobs j ON j.segment_id=s.id WHERE j.event_id=$1`, eventID).Scan(&seg, &loc); err != nil {
			t.Fatal(err)
		}
		return seg, loc
	}
	// The event is still open: the clip exists and can be played by a player that
	// tolerates growth, but it is NOT published media.
	if seg, loc := segmentStateOf(); seg != "finalizing" || loc != "finalizing" {
		t.Fatalf("a running event's clip was published: segment=%q location=%q", seg, loc)
	}
	// None of the consumers that act on finished footage may see it.
	var exported int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_segments s
		JOIN event_clip_jobs j ON j.segment_id=s.id WHERE j.event_id=$1 AND s.state='ready'`, eventID).Scan(&exported); err != nil {
		t.Fatal(err)
	}
	if exported != 0 {
		t.Fatal("the archive/export/watermark set (state='ready') included a running event's clip")
	}

	// The event ends with no new fragments: this pass must publish what exists.
	// Its ended_at is what StillOpen reads — marking only the job would leave the
	// assembler believing the event is running and keeping the clip unpublished.
	endedAt := base.Add(7 * time.Second)
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE events SET ended_at=$2 WHERE id=$1`, eventID, endedAt); err != nil {
		t.Fatal(err)
	}
	// The window must move so this pass has work; the fragments are unchanged.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows SET state='open',window_end=$2
		WHERE id=(SELECT window_id FROM event_clip_jobs WHERE event_id=$1)`, eventID, endedAt.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET state='queued',window_end=$2 WHERE event_id=$1`, eventID, endedAt.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	fixture.Service.AssembleClips(ctx)
	if seg, loc := segmentStateOf(); seg != "ready" || loc != "ready" {
		t.Fatalf("an ended event's clip stayed unpublished: segment=%q location=%q", seg, loc)
	}
}

// An event can end while its job is already 'done' — it sat idle, the last
// assembly settled, and only then did the end message arrive. Nothing re-queues
// that job, so without a safety net the clip stays 'finalizing' forever:
// unplayable, unarchived, unexportable.
func TestSettledEventStillPublishesItsClip(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	eventID := mustID(t)
	started := time.Now().UTC().Add(-5 * time.Minute)
	ended := started.Add(30 * time.Second)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-idle','person',0.5,'[]',$3,$4,'gone','linked','{}')`, eventID, fixture.Channel, started, ended); err != nil {
		t.Fatal(err)
	}
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
		VALUES($1,$2,$3,$4,$5,$6,$6,$7,'UTC',0,$7,$8,1024,'{}','finalizing')`,
		segment, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, path, started, ended); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
		VALUES($1,$2,$3,1024,'finalizing')`, segment, fixture.Pool.ID, path); err != nil {
		t.Fatal(err)
	}
	// The job is settled: the assembly loop will never pick it up again.
	doneWindow := seedClipJob(ctx, t, fixture.DB, eventID, fixture.Channel, started, ended, "done")
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows SET segment_id=$2,clip_run_id=$3 WHERE id=$1`, doneWindow, segment, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET segment_id=$2,clip_run_id=$3 WHERE event_id=$1`, eventID, segment, runID); err != nil {
		t.Fatal(err)
	}

	fixture.Service.PublishSettledClips(ctx)

	var segState, locState string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT s.state,l.state FROM recording_segments s
		JOIN recording_locations l ON l.segment_id=s.id AND l.pool_id=s.pool_id WHERE s.id=$1`, segment).Scan(&segState, &locState); err != nil {
		t.Fatal(err)
	}
	if segState != "ready" || locState != "ready" {
		t.Fatalf("an ended event's clip stayed unpublished: segment=%q location=%q", segState, locState)
	}
}

// The safety net must not publish a clip whose event is still running: that is
// the whole point of keeping it finalizing.
func TestSettledPublicationSkipsRunningEvents(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	eventID := mustID(t)
	started := time.Now().UTC().Add(-5 * time.Minute)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-still-running','person',0.5,'[]',$3,'gone','linked','{}')`, eventID, fixture.Channel, started); err != nil {
		t.Fatal(err)
	}
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
		segment, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, path, started); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
		VALUES($1,$2,$3,1024,'finalizing')`, segment, fixture.Pool.ID, path); err != nil {
		t.Fatal(err)
	}
	growingWindow := seedClipJob(ctx, t, fixture.DB, eventID, fixture.Channel, started, time.Now().UTC(), "growing")
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows SET segment_id=$2,clip_run_id=$3 WHERE id=$1`, growingWindow, segment, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows SET window_end=clock_timestamp() WHERE id=$1`, growingWindow); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET segment_id=$2,clip_run_id=$3 WHERE event_id=$1`, eventID, segment, runID); err != nil {
		t.Fatal(err)
	}
	fixture.Service.PublishSettledClips(ctx)
	var segState string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, segment).Scan(&segState); err != nil {
		t.Fatal(err)
	}
	if segState != "finalizing" {
		t.Fatalf("a running event's clip was published by the safety net: %q", segState)
	}
}

// A clip must be published as soon as reconciliation learns the event ended.
//
// The end message is the fast path and it can be lost. Reconciliation closes the
// event minutes later, but the clip's publication is driven by the assembly loop
// and the retention safety net — neither runs on that path. Measured on the live
// site: clips for events that ended at 17:11 were still 写入中 at 17:17 while
// later clips had already been published, which is what an operator notices.
func TestReconcilePublishesTheClipItCloses(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	markBuffering(t, fixture)

	// The event arrived and never closed: an old start, no end.
	started := time.Now().UTC().Add(-20 * time.Minute)
	startedUnix := float64(started.Unix())
	if err := service.Ingest(ctx, frigatePayload("new", "evt-lost-end", "front-door", "person", startedUnix, nil, nil, false)); err != nil {
		t.Fatal(err)
	}
	var eventID id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id FROM events WHERE source_id='evt-lost-end'`).Scan(&eventID); err != nil {
		t.Fatal(err)
	}

	// It has a clip that is still finalizing, as a running event's would be.
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
		segment, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, path, started); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
		VALUES($1,$2,$3,1024,'finalizing')`, segment, fixture.Pool.ID, path); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET segment_id=$2,clip_run_id=$3,state='growing' WHERE event_id=$1`, eventID, segment, runID); err != nil {
		t.Fatal(err)
	}

	// The detector reports the real end.
	ended := started.Add(40 * time.Second)
	endedUnix := ended.Unix()
	detector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events/evt-lost-end" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"evt-lost-end","label":"person","camera":"front-door",` +
			`"start_time":` + strconv.FormatFloat(startedUnix, 'f', -1, 64) + `,` +
			`"end_time":` + strconv.FormatInt(endedUnix, 10) + `,` +
			`"has_snapshot":true,"zones":[],"data":{"score":0.7,"zones":[]}}`))
	}))
	defer detector.Close()
	service.Frigate = detector.URL

	service.ReconcileOpenEvents(ctx)

	var segState, locState string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT s.state,l.state FROM recording_segments s
		JOIN recording_locations l ON l.segment_id=s.id AND l.pool_id=s.pool_id WHERE s.id=$1`, segment).Scan(&segState, &locState); err != nil {
		t.Fatal(err)
	}
	if segState != "ready" || locState != "ready" {
		t.Fatalf("reconciliation closed the event but left its clip unpublished: segment=%q location=%q", segState, locState)
	}
}

// A clip assembled in several passes must not be a concatenation of a
// concatenation.
//
// The first version fed the previously published clip back in as the head of the
// next assembly. That kept the start from moving, but it spliced AAC at a boundary
// the previous pass had already spliced, and every generation left more invalid
// audio. Measured against real ZLM fragments: one splice produced no AAC errors,
// splicing that result again produced 51. A twenty-pass clip carried thousands,
// and Chrome fails the whole clip on an undecodable audio track even though every
// video frame is intact — the "可用但打不开" an operator reported.
//
// The parts directory is what breaks the chain: every input stays an original
// recording, and each pass rebuilds from all of them.
func TestClipNeverConcatenatesAPreviousClip(t *testing.T) {
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ffprobe := mediaTestTool(t, "ffprobe")
	fixture := scheduledFixture(t)
	ctx := context.Background()
	cache := t.TempDir()
	fixture.Service.EventCacheDir = cache
	fixture.Service.EventCacheBytes = 512 << 20
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: ffprobe}

	ring := filepath.Join(cache, "events", string(fixture.Channel))
	if err := os.MkdirAll(ring, 0o700); err != nil {
		t.Fatal(err)
	}
	// Near now, and the window's close is pushed ahead of the fragments each pass
	// (see below): an open window re-cuts, a closed one only settles, and this test
	// is about re-cutting.
	base := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Second)
	writeFragment := func(offset int) {
		start := base.Add(time.Duration(offset) * time.Second)
		path := filepath.Join(ring, start.In(time.Local).Format("2006-01-02-15-04-05")+"-0.mp4")
		if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:duration=3",
			"-t", "3", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", "-b:a", "32k", "-y", path).Run(); err != nil {
			t.Fatal("synthetic fragment creation failed", err)
		}
		end := start.Add(3 * time.Second)
		if err := os.Chtimes(path, end, end); err != nil {
			t.Fatal(err)
		}
	}

	eventID := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-generations','person',0.5,'[]',$3,'gone','pending','{}')`, eventID, fixture.Channel, base); err != nil {
		t.Fatal(err)
	}
	seedClipJob(ctx, t, fixture.DB, eventID, fixture.Channel, base, base.Add(6*time.Second), "queued")

	// Four passes, each adding two fragments and evicting the oldest — exactly what
	// a two-minute event does to a thirty-second ring.
	for pass := 0; pass < 4; pass++ {
		writeFragment(pass * 6)
		writeFragment(pass*6 + 3)
		if pass > 0 {
			windowEnd := base.Add(time.Duration(pass*6+6) * time.Second)
			if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows SET state='open',window_end=$2
				WHERE id=(SELECT window_id FROM event_clip_jobs WHERE event_id=$1)`, eventID, windowEnd); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET state='queued',window_end=$2 WHERE event_id=$1`, eventID, windowEnd); err != nil {
				t.Fatal(err)
			}
		}
		// Keep only the newest two fragments, as a thirty-second ring would after a
		// long event: everything the earlier passes produced is already evicted.
		entries, err := os.ReadDir(ring)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		if len(names) > 2 {
			sort.Strings(names)
			for _, name := range names[:len(names)-2] {
				if err := os.Remove(filepath.Join(ring, name)); err != nil {
					t.Fatal(err)
				}
			}
		}
		fixture.Service.AssembleClips(ctx)
	}

	var target string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT s.target_relative_path FROM recording_segments s
		JOIN event_clip_jobs j ON j.segment_id=s.id WHERE j.event_id=$1`, eventID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	clip := filepath.Join(fixture.Pool.Path, target)
	if err := exec.Command(ffmpeg, "-nostdin", "-v", "error", "-i", clip, "-f", "null", "-").Run(); err != nil {
		t.Fatalf("the assembled clip no longer decodes cleanly after four passes: %v", err)
	}
	// The clip must cover the whole event, not just the newest fragments: the
	// beginning survives in the parts directory after the ring evicted it.
	probeRunner := probe.Runner{FFprobe: ffprobe}
	file, err := os.Open(clip)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := probeRunner.InspectMP4(ctx, file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Duration < 20*time.Second {
		t.Fatalf("clip duration %v: the beginning was lost when the ring evicted it", evidence.Duration)
	}
}

// A quarantined segment must be removable. The reconciler marks a file damaged
// precisely because its identity no longer matches the index entry that
// authorizes serving it, so gating deletion on that identity would make the state
// permanent — the row would claim a recording exists that nothing can serve.
//
// It still verifies identity for every other state: that check is what stops a
// stale path from authorizing the removal of whatever now sits there.
func TestDamagedSegmentCanBeDeleted(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	segment := seedSegment(t, fixture, time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(-59*time.Minute))
	path := "recordings/" + string(segment) + ".mp4"
	target := filepath.Join(fixture.Pool.Path, path)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The row carries an identity for a different file — the defect's shape.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_segments
		SET state='damaged',error_code='damaged',file_identity='{"Device":1,"Inode":2,"Size":3}' WHERE id=$1`, segment); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state)
		VALUES($1,$2,$3,1024,'damaged')`, segment, fixture.Pool.ID, path); err != nil {
		t.Fatal(err)
	}

	outcomes, err := fixture.Service.DeleteCorruptLocal(ctx, []id.ID{segment})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].State != "deleted" {
		t.Fatalf("a damaged segment could not be deleted: %+v", outcomes)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("the file is still on disk after a successful deletion")
	}
	var state, reason string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT s.state,l.delete_reason FROM recording_segments s
		JOIN recording_locations l ON l.segment_id=s.id WHERE s.id=$1`, segment).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "deleted" || reason != "corrupt" {
		t.Fatalf("state=%q reason=%q, want deleted/corrupt", state, reason)
	}
}

// A clip job abandoned in 'running' must be reclaimed.
//
// A worker that dies mid-assembly (or a context cancelled by SIGTERM) leaves the
// row in 'running'; the assembly scan only looks at queued/growing and retention
// only removes done/failed, so without a requeue the event never gets a clip and
// the row is not even eligible for cleanup.
func TestAbandonedClipJobIsRequeued(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	eventID := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-stuck','person',0.5,'[]',clock_timestamp()-interval '5 minutes','gone','pending','{}')`, eventID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	// A claim from a worker that is gone: 'running', last touched long ago.
	staleWindow := seedClipJob(ctx, t, fixture.DB, eventID, fixture.Channel,
		time.Now().UTC().Add(-5*time.Minute), time.Now().UTC().Add(-4*time.Minute), "running")
	// Both rows must look abandoned: the sweep fences on updated_at, and the job
	// row is the one this test reads back.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows SET state='finalizing',updated_at=clock_timestamp()-interval '10 minutes' WHERE id=$1`, staleWindow); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET updated_at=clock_timestamp()-interval '10 minutes' WHERE event_id=$1`, eventID); err != nil {
		t.Fatal(err)
	}

	fixture.Service.SweepEventClipJobs(ctx)

	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM event_clip_jobs WHERE event_id=$1`, eventID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("an abandoned job stayed %q; the event would never get a clip", state)
	}

	// A FRESH claim is not stolen: an assembly pass is seconds, and requeueing a
	// job that is genuinely in progress would assemble the same footage twice.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET state='running',updated_at=clock_timestamp() WHERE event_id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	fixture.Service.SweepEventClipJobs(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM event_clip_jobs WHERE event_id=$1`, eventID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Fatalf("a live claim was stolen: state=%q", state)
	}
}

// The premise the retention progress guard exists for: a skip leaves the row
// A missing file settles the row instead of leaving it selectable.
//
// This used to pin the opposite: a skip returned WITHOUT touching the row, so
// the same segment was selected again on the next iteration, and an unguarded
// round would loop forever — which is why the retention round carries a
// no-progress guard.
//
// That premise was the bug in disguise. The candidate query selects
// `state='ready' ... ORDER BY end_at LIMIT n`, so a row whose file is gone stays
// at the head of the queue forever and every sweep reports "no safe candidate"
// while hundreds of deletable segments sit behind it. The fix records the verdict
// ('missing'), which removes the row from the queue and makes the starvation
// impossible rather than merely guarded.
//
// The no-progress guard stays: it is what stops a round from spinning on
// identity mismatches, which still leave the row untouched.
func TestMissingFileSettlesTheRowInsteadOfStarvingTheSweep(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	segment := seedSegment(t, fixture, time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(-59*time.Minute))
	// The index points at a file that is not on disk.
	outcomes, err := fixture.Service.DeleteCorruptLocal(ctx, []id.ID{segment})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("expected one outcome, got %d", len(outcomes))
	}
	if outcomes[0].State == "deleted" {
		t.Fatal("a segment with no file reported a successful deletion")
	}

	// The verdict is recorded, so the candidate query no longer returns this row
	// and it cannot occupy the head of the queue.
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, segment).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "missing" {
		t.Fatalf("a lost file must be recorded as missing, got %q", state)
	}
	// And the same call reports missing rather than looping: a second attempt
	// finds the row in that state and answers immediately.
	again, err := fixture.Service.DeleteCorruptLocal(ctx, []id.ID{segment})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].State != "missing" {
		t.Fatalf("a repeat attempt is idempotent, got %+v", again)
	}
}

// An empty window is a timing answer, not a verdict, and must stay retryable.
//
// The ring may hold nothing yet — an event detected in the first seconds after a
// worker restart, before the media server closed its first fragment. Marking the
// job 'failed' made that permanent, because queueClipJob only reopens
// queued/growing, so footage arriving moments later was never assembled.
//
// The retry is bounded by the event's age: a fragment cannot outlive the ring's
// own keep window, so an older event can never gain one.
func TestEmptyWindowStaysRetryableForARecentEvent(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	cache := t.TempDir()
	fixture.Service.EventCacheDir = cache
	fixture.Service.EventCacheBytes = 64 << 20

	// An empty ring: the cache directory exists, nothing has been written yet.
	recent := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-recent','person',0.5,'[]',clock_timestamp()-interval '10 seconds','gone','unavailable','{}')`, recent, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	seedClipJob(ctx, t, fixture.DB, recent, fixture.Channel, now.Add(-15*time.Second), now, "queued")

	fixture.Service.AssembleClips(ctx)

	var state, code string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,coalesce(error_code,'') FROM event_clip_jobs WHERE event_id=$1`, recent).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("a recent event's empty window became %q; the footage arriving later would never be assembled", state)
	}

	// An OLD event can never gain fragments: the ring has long dropped anything it
	// could have had, so retrying forever is busywork.
	old := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-old','person',0.5,'[]',clock_timestamp()-interval '30 minutes','gone','unavailable','{}')`, old, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	seedClipJob(ctx, t, fixture.DB, old, fixture.Channel, now.Add(-31*time.Minute), now.Add(-30*time.Minute), "queued")
	fixture.Service.AssembleClips(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM event_clip_jobs WHERE event_id=$1`, old).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("an event older than the ring's keep window kept retrying: state=%q", state)
	}
}

// A quarantined segment must be reclaimable.
//
// It records the path the file WOULD have been published to, while the file itself
// is still in the working directory — the publish never happened. deleteOne only
// looked at Target, so those rows were permanently undeletable: the bytes stayed on
// disk forever while the index described a publication that never occurred, and no
// retention or quota sweep could free them.
func TestQuarantinedSegmentCanBeReclaimed(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	// The file sits in the working directory; the row names a published path that
	// does not exist. Inserted this way rather than updated: the path columns are
	// part of the publication identity and a trigger refuses to change them, which
	// is the same guard that keeps a running system from renaming published media.
	workPath := ".work/zlm/" + string(fixture.Run) + "/record/one_nvr/s1/2026-10-20/2026-10-20-10-00-00-0.mp4"
	publishedPath := "recordings/CH01/2026-10-20/CH01_20261020_100000_review.mp4"
	target := filepath.Join(fixture.Pool.Path, workPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("quarantined-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	segment, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,
		original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,
		size_bytes,file_identity,state,error_code)
		VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp()-interval '1 hour','UTC',0,
			clock_timestamp()-interval '1 hour',clock_timestamp()-interval '59 minutes',1024,'{}','damaged','media_requires_review')`,
		segment, fixture.Channel, fixture.Revision, fixture.Run, fixture.Pool.ID, workPath, publishedPath); err != nil {
		t.Fatal(err)
	}

	outcomes, err := fixture.Service.DeleteCorruptLocal(ctx, []id.ID{segment})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].State != "deleted" {
		t.Fatalf("a quarantined segment could not be reclaimed: %+v", outcomes)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("the quarantined file is still on disk after a successful deletion")
	}
}

// One stretch of activity is ONE recording, even when the detector reports it as
// several events.
//
// The detector ends an event when it stops recognising the subject — routinely
// while the person is still walking out — and starts a new one when it sees them
// again. Measured on this site, 67 of 162 six-hour activity stretches arrived as
// two or more events, up to six. Each produced its own clip, so one visit to the
// driveway is several files with unwatched time between them.
//
// The rule the operator stated: a recording is planned to close at `event end +
// trail`; an event arriving BEFORE that closes it moves the close out to its own
// `end + trail`, repeatedly. The recording is the subject; events are anchors on
// it.
func TestOneActivityStretchProducesOneClip(t *testing.T) {
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ffprobe := mediaTestTool(t, "ffprobe")
	fixture := scheduledFixture(t)
	ctx := context.Background()
	cache := t.TempDir()
	fixture.Service.EventCacheDir = cache
	fixture.Service.EventCacheBytes = 512 << 20
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: ffprobe}

	ring := filepath.Join(cache, "events", string(fixture.Channel))
	if err := os.MkdirAll(ring, 0o700); err != nil {
		t.Fatal(err)
	}
	// Realtime: the events arrive now, so their windows' closes are in the future
	// and an arriving event extends the open one. Placing them a minute in the past
	// would make every window already closed, and the test would be asserting the
	// wrong branch — that case is TestEventsFarApartStaySeparateClips.
	base := time.Now().UTC().Add(-8 * time.Second).Truncate(time.Second)
	// Nine seconds of continuous footage: one visit, two events.
	for offset := 0; offset < 9; offset += 3 {
		start := base.Add(time.Duration(offset) * time.Second)
		name := start.In(time.Local).Format("2006-01-02-15-04-05") + "-0.mp4"
		path := filepath.Join(ring, name)
		if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=5", "-t", "3",
			"-c:v", "libx264", "-threads", "1", "-y", path).Run(); err != nil {
			t.Fatal("synthetic fragment creation failed", err)
		}
		end := start.Add(3 * time.Second)
		if err := os.Chtimes(path, end, end); err != nil {
			t.Fatal(err)
		}
	}

	// Event A: 0-5s. Event B: 8-12s — inside A's trail window (5s + 10s trail
	// reaches 15s), so the recording must be extended rather than restarted.
	first := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-a','person',0.5,'[]',$3,$4,'gone','unavailable','{}')`,
		first, fixture.Channel, base, base.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	second := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-b','person',0.5,'[]',$3,$4,'gone','unavailable','{}')`,
		second, fixture.Channel, base.Add(5*time.Second), base.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Attach both through the same path the detector uses.
	for _, item := range []struct {
		id    id.ID
		start time.Time
		end   time.Time
	}{
		{first, base, base.Add(2 * time.Second)},
		{second, base.Add(5 * time.Second), base.Add(8 * time.Second)},
	} {
		end := item.end
		if err := fixture.Service.QueueClipWindow(ctx, item.id, fixture.Channel, item.start, &end); err != nil {
			t.Fatal(err)
		}
	}
	// The window's close is in the future (trail after the last event), so move it
	// into the past to let the assembler treat the recording as finished. Scoped to
	// this channel: an unqualified UPDATE would also rewrite the very windows this
	// test is counting.
	// Make the recording look finished so the assembler will publish it, without
	// shrinking it below the footage: the close moves to one second after the last
	// fragment's end, which is in the past. The table's CHECK forbids
	// window_end <= window_start, and a real close is always after it.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows
		SET window_end = window_start + interval '11 seconds'
		WHERE channel_id=$1`, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	fixture.Service.AssembleClips(ctx)

	var runs int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(DISTINCT run_id) FROM recording_segments
		WHERE channel_id=$1 AND run_id IN (SELECT clip_run_id FROM clip_windows WHERE channel_id=$1)`, fixture.Channel).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		var detail string
		_ = fixture.DB.Pool.QueryRow(ctx, `SELECT string_agg(id::text||' '||window_start::text||' .. '||window_end::text||' '||state, ' | ' ORDER BY window_start)
			FROM clip_windows WHERE channel_id=$1`, fixture.Channel).Scan(&detail)
		t.Fatalf("one visit produced %d clips, want 1: the second event arrived inside the first recording's trail window; windows: %s", runs, detail)
	}
}

// Two visits far apart stay two recordings. The rule above must not merge
// everything a channel ever recorded into one file.
func TestEventsFarApartStaySeparateClips(t *testing.T) {
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ffprobe := mediaTestTool(t, "ffprobe")
	fixture := scheduledFixture(t)
	ctx := context.Background()
	cache := t.TempDir()
	fixture.Service.EventCacheDir = cache
	fixture.Service.EventCacheBytes = 512 << 20
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: ffprobe}

	ring := filepath.Join(cache, "events", string(fixture.Channel))
	if err := os.MkdirAll(ring, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-180 * time.Second).Truncate(time.Second)
	writeAt := func(offset int) {
		start := base.Add(time.Duration(offset) * time.Second)
		name := start.In(time.Local).Format("2006-01-02-15-04-05") + "-0.mp4"
		path := filepath.Join(ring, name)
		if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=5", "-t", "3",
			"-c:v", "libx264", "-threads", "1", "-y", path).Run(); err != nil {
			t.Fatal(err)
		}
		end := start.Add(3 * time.Second)
		if err := os.Chtimes(path, end, end); err != nil {
			t.Fatal(err)
		}
	}
	// Footage for each visit, plus a 120-second gap between them.
	writeAt(0)
	writeAt(120)

	first := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-far-a','person',0.5,'[]',$3,$4,'gone','unavailable','{}')`,
		first, fixture.Channel, base, base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	second := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-far-b','person',0.5,'[]',$3,$4,'gone','unavailable','{}')`,
		second, fixture.Channel, base.Add(120*time.Second), base.Add(123*time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id    id.ID
		start time.Time
		end   time.Time
	}{
		{first, base, base.Add(3 * time.Second)},
		{second, base.Add(120 * time.Second), base.Add(123 * time.Second)},
	} {
		end := item.end
		if err := fixture.Service.QueueClipWindow(ctx, item.id, fixture.Channel, item.start, &end); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE clip_windows
		SET window_end = window_start + interval '1 second'
		WHERE channel_id=$1`, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	fixture.Service.AssembleClips(ctx)

	var windows int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM clip_windows WHERE channel_id=$1`, fixture.Channel).Scan(&windows); err != nil {
		t.Fatal(err)
	}
	if windows != 2 {
		t.Fatalf("two visits two minutes apart produced %d windows, want 2", windows)
	}
}

// seedClipJob inserts an event_clip_jobs row together with the clip_windows row it
// now belongs to.
//
// Since migration 0050 a job is an anchor on a recording window: window_id is NOT
// NULL, and the assembler reads clip_windows. Tests that used to insert a job
// directly have to create its window first — the job alone is no longer a unit of
// work.
func seedClipJob(ctx context.Context, t *testing.T, db *database.DB, eventID, channelID id.ID, from, to time.Time, state string) id.ID {
	t.Helper()
	windowID := mustID(t)
	windowState := "open"
	switch state {
	case "done":
		windowState = "done"
	case "failed":
		windowState = "failed"
	case "growing":
		windowState = "finalizing"
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO clip_windows(id,channel_id,window_start,window_end,state)
		VALUES($1,$2,$3,$4,$5)`, windowID, channelID, from, to, windowState); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO event_clip_jobs(event_id,channel_id,window_start,window_end,state,window_id)
		VALUES($1,$2,$3,$4,$5,$6)`, eventID, channelID, from, to, state, windowID); err != nil {
		t.Fatal(err)
	}
	return windowID
}
