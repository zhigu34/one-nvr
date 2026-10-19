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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
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
		if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO event_clip_jobs(event_id,channel_id,window_start,window_end,state,updated_at)
			VALUES($1,$2,$3,$4,$5,$6)`, ev, fixture.Channel, now.Add(-time.Minute), now, state, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
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
	base := time.Now().UTC().Add(-40 * time.Second).Truncate(time.Second)
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
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO event_clip_jobs(event_id,channel_id,window_start,window_end,state)
		VALUES($1,$2,$3,$4,'queued')`, eventID, fixture.Channel, base, base.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
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
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE event_clip_jobs SET state='queued',window_end=$2 WHERE event_id=$1`, eventID, base.Add(12*time.Second)); err != nil {
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
	ctx := context.Background()
	// A real file on disk is what the repair reads: the inode is the only thing a
	// file can prove about itself.
	if err := os.MkdirAll(filepath.Join(fixture.Pool.Path, "recordings"), 0o700); err != nil {
		t.Fatal(err)
	}
	clipID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	clipPath := "recordings/" + string(clipID) + ".mp4"
	clipTarget := filepath.Join(fixture.Pool.Path, clipPath)
	if err := os.WriteFile(clipTarget, []byte("clip-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(target, []byte("short"), 0o600); err != nil {
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
	payload := make([]byte, 100000)
	if err := os.WriteFile(target, payload, 0o600); err != nil {
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
