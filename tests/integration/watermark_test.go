package integration

import (
	"context"
	"testing"
	"time"
)

// setPoolCapacity writes fresh api+worker capacity evidence for the fixture
// pool. Both probes must exist and agree on the filesystem, exactly like the
// recorder-start gate: one fresh probe is "capacity unknown", not "ready".
func setPoolCapacity(t *testing.T, f contentFixture, total, free int64, filesystem string) {
	t.Helper()
	for _, service := range []string{"api", "worker"} {
		if _, err := f.DB.Pool.Exec(context.Background(), `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,total_bytes,free_bytes,filesystem_id)
			VALUES($1,$2,'healthy','watermark_fixture',clock_timestamp(),clock_timestamp()+interval '3 minutes',$3,$4,$5)
			ON CONFLICT(pool_id,service) DO UPDATE SET state='healthy',reason_code='watermark_fixture',observed_at=clock_timestamp(),expires_at=clock_timestamp()+interval '3 minutes',total_bytes=EXCLUDED.total_bytes,free_bytes=EXCLUDED.free_bytes,filesystem_id=EXCLUDED.filesystem_id`,
			f.Pool.ID, service, total, free, filesystem); err != nil {
			t.Fatal(err)
		}
	}
}

// Pressure cleanup must actually delete the oldest safe recording when a pool
// crosses its pressure line.
//
// The fixture publishes one real segment with a real file on disk, then reports
// the pool as nearly full. That is the exact situation this feature exists for:
// recordings are already running, the disk is filling, and nothing is old
// enough for the retention window — so without pressure cleanup the site simply
// stops recording.
func TestWatermarkPressureCleanupDeletesOldestSafeSegment(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	publishedAt(t, f, 24*time.Hour)

	// 95% used on a 100 GiB volume: comfortably across pressure and critical.
	setPoolCapacity(t, f, 107374182400, 5368709120, "fs-watermark")
	if _, err := f.DB.Pool.Exec(ctx, `UPDATE storage_pools SET warning_percent=80,pressure_percent=85,pressure_target_percent=80,critical_percent=95 WHERE id=$1`, f.Pool.ID); err != nil {
		t.Fatal(err)
	}

	f.Service.SweepWatermarks(ctx)

	var state, reason string
	if err := f.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, f.Segment.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := f.DB.Pool.QueryRow(ctx, `SELECT coalesce(delete_reason,'') FROM recording_locations WHERE segment_id=$1`, f.Segment.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if state != "deleted" {
		t.Fatalf("segment state=%q after pressure cleanup, want deleted: a nearly full "+
			"pool must free space before the recorder stops", state)
	}
	if reason != "watermark" {
		t.Fatalf("delete reason=%q, want watermark: the audit must distinguish "+
			"pressure cleanup from age-based retention", reason)
	}
	if localFileExists(t, f) {
		t.Fatal("pressure cleanup left the file on disk")
	}

	// The decision is recorded, including why it stopped: an operator asking
	// "what did it delete and why" must find an answer.
	var recorded, deleted int
	var outcome string
	if err := f.DB.Pool.QueryRow(ctx, `SELECT requested,deleted,outcome FROM storage_watermark_events WHERE pool_id=$1 ORDER BY observed_at DESC LIMIT 1`, f.Pool.ID).Scan(&recorded, &deleted, &outcome); err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("watermark event recorded deleted=%d, want 1 (requested=%d outcome=%s)", deleted, recorded, outcome)
	}
}

// A protected recording is evidence. Pressure cleanup must never be the back
// door that removes it, and it must report that it could not free space rather
// than silently claiming success.
func TestWatermarkPressureCleanupSkipsProtected(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	publishedAt(t, f, 24*time.Hour)
	if _, err := f.Service.SetProtection(ctx, f.Admin, f.Segment.ID, true); err != nil {
		t.Fatal(err)
	}
	setPoolCapacity(t, f, 107374182400, 5368709120, "fs-watermark")

	f.Service.SweepWatermarks(ctx)

	if !localFileExists(t, f) {
		t.Fatal("pressure cleanup deleted a protected recording; protection must " +
			"outrank capacity pressure")
	}
	var outcome string
	if err := f.DB.Pool.QueryRow(ctx, `SELECT outcome FROM storage_watermark_events WHERE pool_id=$1 ORDER BY observed_at DESC LIMIT 1`, f.Pool.ID).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != "no_safe_candidate" {
		t.Fatalf("outcome=%q with only protected media, want no_safe_candidate: "+
			"the sweeper has to say it could not free space", outcome)
	}
}

// A pool below its warning line must not delete anything, and must not even be
// reported as needing cleanup.
func TestWatermarkBelowThresholdDeletesNothing(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	publishedAt(t, f, 24*time.Hour)
	setPoolCapacity(t, f, 107374182400, 42949672960, "fs-watermark")

	f.Service.SweepWatermarks(ctx)

	if !localFileExists(t, f) {
		t.Fatal("cleanup deleted a recording while the pool was 60% used")
	}
	var events int
	if err := f.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM storage_watermark_events WHERE pool_id=$1`, f.Pool.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("%d watermark events recorded below every threshold; a healthy "+
			"pool should not write a decision row each round", events)
	}
}

// Quota cleanup removes only this channel's oldest safe segments, independent of
// how full the volume is.
func TestQuotaCleanupDeletesOldestChannelSegment(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	publishedAt(t, f, 24*time.Hour)

	// A quota smaller than the published segment forces one deletion.
	if _, err := f.DB.Pool.Exec(ctx, `UPDATE channels SET recording_quota_bytes=1 WHERE id=$1`, f.Channel); err != nil {
		t.Fatal(err)
	}

	f.Service.SweepQuotas(ctx)

	if localFileExists(t, f) {
		t.Fatal("quota cleanup left a segment that exceeds the channel quota")
	}
	var reason string
	if err := f.DB.Pool.QueryRow(ctx, `SELECT coalesce(delete_reason,'') FROM recording_locations WHERE segment_id=$1`, f.Segment.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "quota" {
		t.Fatalf("delete reason=%q, want quota", reason)
	}
}

// An unmeasured pool is unknown, not healthy: nothing is deleted, and the state
// is recorded as unknown so the UI cannot paint it green.
func TestWatermarkUnknownCapacityDoesNotDelete(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	publishedAt(t, f, 24*time.Hour)
	// Expire the capacity evidence.
	if _, err := f.DB.Pool.Exec(ctx, `UPDATE storage_pool_checks SET expires_at=clock_timestamp()-interval '1 minute' WHERE pool_id=$1 AND service IN ('api','worker')`, f.Pool.ID); err != nil {
		t.Fatal(err)
	}

	f.Service.SweepWatermarks(ctx)

	if !localFileExists(t, f) {
		t.Fatal("cleanup deleted media while the pool capacity was unknown")
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, `SELECT state FROM storage_watermark_events WHERE pool_id=$1 ORDER BY observed_at DESC LIMIT 1`, f.Pool.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" {
		t.Fatalf("state=%q with no fresh capacity evidence, want unknown", state)
	}
}
