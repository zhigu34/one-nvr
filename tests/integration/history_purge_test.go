package integration

// Purging all recorded history, against the real migrated schema.
//
// The statement order in recording.PurgeHistoryRows is dictated by foreign keys,
// and the only way to know it is right is to run it on a schema that has the keys.
// A wrong order fails halfway through a production purge — the moment nobody
// wants to be discovering it — so it is asserted here instead.

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/recording"
)

func TestPurgeHistoryEmptiesMediaAndEventsButKeepsConfiguration(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()

	// One of everything the purge must remove, built through the real tables so
	// every foreign key the delete order depends on actually exists.
	runID := fixture.Run
	segmentID := mustID(t)
	path := "recordings/" + string(segmentID) + ".mp4"
	start := time.Now().UTC().Add(-time.Hour)
	end := start.Add(30 * time.Second)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
		original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,state)
		VALUES($1,$2,$3,$4,$5,$6,$6,$7,'UTC',0,$7,$8,1024,'{}','ready')`,
		segmentID, fixture.Channel, fixture.Revision, runID, fixture.Pool.ID, path, start, end); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(segment_id,pool_id,relative_path,size_bytes,state) VALUES($1,$2,$3,1024,'ready')`,
		segmentID, fixture.Pool.ID, path); err != nil {
		t.Fatal(err)
	}
	eventID := mustID(t)
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO events(id,channel_id,source,source_id,category,score,zones,started_at,ended_at,snapshot_state,media_state,raw)
		VALUES($1,$2,'frigate','evt-purge','person',0.5,'[]',$3,$4,'gone','linked','{}')`,
		eventID, fixture.Channel, start, end); err != nil {
		t.Fatal(err)
	}
	seedClipJob(ctx, t, fixture.DB, eventID, fixture.Channel, start, end, "queued")
	// hook_inbox has ON DELETE NO ACTION to recording_runs, which is the constraint
	// a naive delete order trips over.
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO hook_inbox(id,source_key,run_id,state,payload,available_at)
		VALUES($1,$2,$3,'processed','{}',clock_timestamp())`, mustID(t), "purge-"+string(runID), runID); err != nil {
		t.Fatal(err)
	}

	// Configuration that must survive: the point of the purge is data, not a
	// re-install.
	var channelsBefore, poolsBefore, policiesBefore, usersBefore int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM channels),(SELECT count(*) FROM storage_pools),
		(SELECT count(*) FROM recording_policies),(SELECT count(*) FROM users)`).Scan(&channelsBefore, &poolsBefore, &policiesBefore, &usersBefore); err != nil {
		t.Fatal(err)
	}
	var auditBefore int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}

	if err := recording.PurgeHistoryRows(ctx, fixture.DB); err != nil {
		t.Fatalf("purge failed against the real schema: %v", err)
	}

	var segments, events, locations, clipJobs, clipWindows, runs, inbox int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT
		  (SELECT count(*) FROM recording_segments)
		, (SELECT count(*) FROM events)
		, (SELECT count(*) FROM recording_locations)
		, (SELECT count(*) FROM event_clip_jobs)
		, (SELECT count(*) FROM clip_windows)
		, (SELECT count(*) FROM recording_runs)
		, (SELECT count(*) FROM hook_inbox)`).Scan(&segments, &events, &locations, &clipJobs, &clipWindows, &runs, &inbox); err != nil {
		t.Fatal(err)
	}
	if segments != 0 || events != 0 || locations != 0 || clipJobs != 0 || clipWindows != 0 || runs != 0 || inbox != 0 {
		t.Fatalf("history survived the purge: segments=%d events=%d locations=%d clip_jobs=%d clip_windows=%d runs=%d inbox=%d",
			segments, events, locations, clipJobs, clipWindows, runs, inbox)
	}

	var channelsAfter, poolsAfter, policiesAfter, usersAfter int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM channels),(SELECT count(*) FROM storage_pools),
		(SELECT count(*) FROM recording_policies),(SELECT count(*) FROM users)`).Scan(&channelsAfter, &poolsAfter, &policiesAfter, &usersAfter); err != nil {
		t.Fatal(err)
	}
	if channelsAfter != channelsBefore || poolsAfter != poolsBefore || policiesAfter != policiesBefore || usersAfter != usersBefore {
		t.Fatalf("the purge removed configuration: channels %d->%d pools %d->%d policies %d->%d users %d->%d",
			channelsBefore, channelsAfter, poolsBefore, poolsAfter, policiesBefore, policiesAfter, usersBefore, usersAfter)
	}

	// The audit trail is the evidence that this happened. Wiping it would make the
	// purge unverifiable, which is the one thing an irreversible operation must not be.
	var auditAfter int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs`).Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	if auditAfter <= auditBefore {
		t.Fatalf("the purge left no audit row: %d -> %d", auditBefore, auditAfter)
	}
	var purgedRows int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='recording.history_purged'`).Scan(&purgedRows); err != nil {
		t.Fatal(err)
	}
	if purgedRows != 1 {
		t.Fatalf("expected exactly one purge audit row, got %d", purgedRows)
	}
}

// Running it twice must be harmless: an operator retrying an interrupted purge is
// the normal case, not an error.
func TestPurgeHistoryIsRepeatable(t *testing.T) {
	fixture := scheduledFixture(t)
	ctx := context.Background()
	if err := recording.PurgeHistoryRows(ctx, fixture.DB); err != nil {
		t.Fatal(err)
	}
	if err := recording.PurgeHistoryRows(ctx, fixture.DB); err != nil {
		t.Fatalf("a second purge failed on an empty index: %v", err)
	}
}
