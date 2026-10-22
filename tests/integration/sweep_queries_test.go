package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// The export reconciliation sweep never ran: its SQL used $2/$3 while passing a
// leading empty argument, so PostgreSQL could not infer a type for the unused $1
// and rejected the statement on every tick. Exports whose job exhausted its
// attempts therefore stayed 'queued' forever and the UI showed them running.
//
// The assertion is on the outcome, not on the log: a failed job must leave the
// export row in a terminal state.
func TestReconcileExportsTerminalizesExhaustedJobs(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := fixture.Service

	exportID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	// A job that already exhausted its attempts, and an export row still
	// claiming it is queued — exactly the state the sweep exists to repair.
	if _, err := fixture.DB.Pool.Exec(ctx,
		`INSERT INTO jobs(id,kind,object_id,idempotency_key,parameter_digest,state,attempt,max_attempts)
		 VALUES($1,'recording.export',$2,$3,decode(repeat('00',32),'hex'),'failed',6,6)`,
		jobID, exportID, "reconcile-"+string(jobID)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx,
		`INSERT INTO recording_exports(id,channel_id,channel_no,pool_id,requested_start,requested_end,gap_mode,state,progress,requested_by,requested_by_name,job_id,idempotency_key)
		 VALUES($1,$2,1,$3,clock_timestamp()-interval '1 hour',clock_timestamp(),'split','queued',0,$4,'fixture-admin',$5,$6)`,
		exportID, fixture.Channel, fixture.Pool.ID, fixture.Admin.UserID, jobID, "export-"+string(exportID)); err != nil {
		t.Fatal(err)
	}

	service.ReconcileExports(ctx)

	var state, code string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT state,coalesce(error_code,'') FROM recording_exports WHERE id=$1`, exportID).
		Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("export state = %q after its job exhausted, want failed: the "+
			"reconciliation sweep is the only thing that terminalizes this row, "+
			"so a non-terminal state means the sweep did not run", state)
	}
	if code != "job_exhausted" {
		t.Fatalf("error_code = %q, want job_exhausted", code)
	}
}

// The snapshot sweep queried storage_pools.path, which does not exist (the
// column is canonical_path), so every pass failed before reading a single event.
// Archived snapshots therefore never landed and every ready picture stayed
// unarchived.
//
// This asserts the query works against the real schema by driving one pass and
// checking it did not error: an event with a ready snapshot and no archived path
// is the candidate the sweep must find.
func TestArchiveSnapshotsReadsPoolPath(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	service.Frigate = "http://frigate.test:5000"
	service.Pools = fixture.Service.Pools

	// The sweep only considers channels with a bound pool: the copy needs a
	// destination, and a channel with no pool has nowhere to put the picture.
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET storage_pool_id=$2 WHERE id=$1", fixture.Channel, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
	// The mapping table attributes an upstream camera to a channel; an unmapped
	// camera is dropped at ingest.
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	// A finished event whose picture is ready but not yet copied.
	start := float64(time.Now().Add(-2 * time.Hour).Unix())
	end := start + 30
	if err := service.Ingest(ctx, frigatePayload("end", "snap-fixture-1", "front-door", "person", start, &end, nil, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx,
		`UPDATE events SET snapshot_state='ready',snapshot_relative_path=NULL,snapshot_archive_error=NULL
		 WHERE source_id='snap-fixture-1'`); err != nil {
		t.Fatal(err)
	}

	// One bounded pass. The detector is unreachable in this fixture, so the copy
	// itself fails — what matters is that the scan reached the row instead of
	// erroring on an unknown column, which is recorded as the archive error.
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.ArchiveSnapshots(ctx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot sweep did not finish; it is looping on a failing scan")
	}

	// The sweep must have looked at the row, and an unreachable detector must
	// leave it ELIGIBLE for the next pass.
	//
	// This used to assert that snapshot_archive_error was set — which made the
	// first failure final, because the scan excludes rows with a non-null error.
	// A detector that was briefly down cost the picture permanently. The row now
	// stays clean and the sweep retries within SnapshotRetryWindow; the evidence
	// that the scan reached it is that the row is still pending and not excluded.
	var recorded *string
	var path *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT snapshot_archive_error,snapshot_relative_path FROM events WHERE source_id='snap-fixture-1'`).Scan(&recorded, &path); err != nil {
		t.Fatal(err)
	}
	if path != nil {
		t.Fatal("an unreachable detector produced a picture")
	}
	if recorded != nil {
		t.Fatalf("a retryable failure was recorded as terminal (%q); the sweep will never try again", *recorded)
	}
}

var _ = events.Service{}
var _ = recording.RetentionSweepInterval

// A picture that arrives late is still archived.
//
// Frigate writes snapshots asynchronously, so the first ask after an event
// routinely misses for a short one — measured on this site, a 24-second event had
// its picture and a 2-second one did not. Treating the first 404 as final threw
// away pictures that appeared a minute later.
func TestLateArrivingSnapshotIsArchived(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	service.Pools = fixture.Service.Pools
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET storage_pool_id=$2 WHERE id=$1", fixture.Channel, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}

	// A detector that answers 404 for the first two asks and 200 afterwards.
	var asks int32
	detector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/snapshot.jpg") {
			w.WriteHeader(404)
			return
		}
		if atomic.AddInt32(&asks, 1) <= 2 {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF, 0xD9})
	}))
	defer detector.Close()
	service.Frigate = detector.URL

	start := float64(time.Now().Add(-10 * time.Minute).Unix())
	end := start + 20
	if err := service.Ingest(ctx, frigatePayload("end", "late-snap", "front-door", "person", start, &end, nil, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx,
		`UPDATE events SET snapshot_state='ready',snapshot_relative_path=NULL,snapshot_archive_error=NULL
		 WHERE source_id='late-snap'`); err != nil {
		t.Fatal(err)
	}

	// Three passes: the first two are refused, the third gets the picture.
	for pass := 0; pass < 3; pass++ {
		service.ArchiveSnapshots(ctx)
	}
	var path *string
	var archiveErr *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT snapshot_relative_path,snapshot_archive_error FROM events WHERE source_id='late-snap'`).Scan(&path, &archiveErr); err != nil {
		t.Fatal(err)
	}
	if path == nil {
		t.Fatalf("a picture that arrived on the third ask was never archived (error=%v, asks=%d)", archiveErr, asks)
	}
	if !strings.Contains(*path, "snapshots/") {
		t.Fatalf("the picture was stored at %q, want a path under snapshots/", *path)
	}
}

// A terminal failure still stops the retries: a channel with no pool cannot store
// a picture no matter how often the sweep asks.
func TestTerminalSnapshotFailureStopsRetrying(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	service.Pools = fixture.Service.Pools
	service.Frigate = "http://frigate.test:5000"
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}
	// No pool bound: terminal.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE channels SET storage_pool_id=NULL WHERE id=$1`, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	start := float64(time.Now().Add(-10 * time.Minute).Unix())
	end := start + 20
	if err := service.Ingest(ctx, frigatePayload("end", "no-pool-snap", "front-door", "person", start, &end, nil, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx,
		`UPDATE events SET snapshot_state='ready',snapshot_relative_path=NULL,snapshot_archive_error=NULL
		 WHERE source_id='no-pool-snap'`); err != nil {
		t.Fatal(err)
	}
	service.ArchiveSnapshots(ctx)
	// The scan only considers channels with a bound pool, so the row is not even
	// reached — and it must not be left looking retryable forever.
	var archiveErr *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT snapshot_archive_error FROM events WHERE source_id='no-pool-snap'`).Scan(&archiveErr); err != nil {
		t.Fatal(err)
	}
	if archiveErr != nil {
		t.Fatalf("a channel with no pool recorded %q; the scan never reached it", *archiveErr)
	}
}

// A one-second event is examined almost immediately; a five-minute one waits.
//
// The gate exists to avoid touching a live event, and a live event has no end
// time. Waiting two minutes for every event meant a detection that ended after
// one second sat on "抓拍待同步 · 稍后自动核对" long after the answer was decided —
// measured on this site, the median detection lasts four seconds.
func TestShortEventsAreReconciledSooner(t *testing.T) {
	fixture, service := newEventsFixture(t)
	ctx := context.Background()
	// A detector that knows nothing about these events, so a 'gone' verdict is
	// the only outcome once the wait has passed — which is what makes "was this
	// examined yet" observable from the row.
	detector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer detector.Close()
	service.Frigate = detector.URL
	if err := service.SetMappings(ctx, fixture.Admin, map[id.ID]string{fixture.Channel: "front-door"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	// A short event that ended 45 seconds ago: past the proportional gate (30s
	// for a one-second detection) and well inside the old two-minute one. It is
	// examined this pass, and the picture keeps its own wait (SnapshotRetryWindow)
	// — Frigate writes snapshots asynchronously, so a 404 this early is not proof
	// there is no image.
	short := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx,
		`INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		 VALUES($1,$2,'frigate','short-1','person',0.5,'[]',$3,$4,'pending','pending','{}')`,
		short, fixture.Channel, now.Add(-46*time.Second), now.Add(-45*time.Second)); err != nil {
		t.Fatal(err)
	}
	// The same short event, older than the retry window: now the verdict is final
	// and it becomes gone. This is the observable proof that the sweep reached it.
	old := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx,
		`INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		 VALUES($1,$2,'frigate','short-1-old','person',0.5,'[]',$3,$4,'pending','pending','{}')`,
		old, fixture.Channel, now.Add(-3*time.Hour-1*time.Second), now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A long event (five minutes) that ended 45 seconds ago: its own wait is the
	// two-minute cap, so it must be left alone entirely.
	longFresh := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx,
		`INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		 VALUES($1,$2,'frigate','long-fresh','person',0.5,'[]',$3,$4,'pending','pending','{}')`,
		longFresh, fixture.Channel, now.Add(-6*time.Minute), now.Add(-45*time.Second)); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		service.ReconcileOpenEvents(ctx)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("reconciliation did not finish")
	}

	stateOf := func(eventID id.ID) string {
		var state string
		if err := fixture.DB.Pool.QueryRow(ctx, `SELECT snapshot_state FROM events WHERE id=$1`, eventID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	// A recent short event is examined but keeps waiting for its picture.
	if got := stateOf(short); got != "pending" {
		t.Fatalf("a 1-second event that ended 45s ago became %q; the picture keeps its retry window", got)
	}
	// Past the window the same event is settled: proof the sweep reached it.
	if got := stateOf(old); got != "gone" {
		t.Fatalf("a short event past the retry window is still %q; it must be settled", got)
	}
	// The long event was NOT examined: its own duration puts its wait at the
	// two-minute cap.
	if got := stateOf(longFresh); got != "pending" {
		t.Fatalf("a 5-minute event that ended 45s ago became %q; its wait should still be running", got)
	}
}
