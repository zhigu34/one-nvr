package integration

import (
	"context"
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

	// The sweep must have looked at the row. A schema error leaves the row
	// untouched with no error recorded, which is the bug's signature.
	var recorded *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT snapshot_archive_error FROM events WHERE source_id='snap-fixture-1'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded == nil {
		t.Fatal("the snapshot sweep did not record an outcome for a ready event; " +
			"an unreachable detector must be reported on the row, so a null " +
			"result means the scan never reached it")
	}
}

var _ = events.Service{}
var _ = recording.RetentionSweepInterval
