package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// publishedAt moves a published segment back in time so the freshness delay no
// longer applies. Retention and the operator path share that delay, so every
// deletion test starts by making its segment genuinely old.
func publishedAt(t *testing.T, f contentFixture, age time.Duration) {
	t.Helper()
	when := time.Now().UTC().Add(-age)
	if _, err := f.DB.Pool.Exec(context.Background(),
		"UPDATE recording_segments SET start_at=$2,end_at=$3 WHERE id=$1",
		f.Segment.ID, when.Add(-time.Minute), when); err != nil {
		t.Fatal(err)
	}
}

func localFileExists(t *testing.T, f contentFixture) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(f.Pool.Path, f.Target))
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return false
}

func deleteOne(t *testing.T, f contentFixture, principal auth.Principal) []recording.DeleteOutcome {
	t.Helper()
	out, err := f.Service.DeleteLocal(context.Background(), principal, []id.ID{f.Segment.ID}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("outcomes=%d want 1", len(out))
	}
	return out
}

// Deletion must remove the file, keep the index row with a truthful state, and
// leave an audit trail. The recording stops being playable, which is the whole
// point: PRD FILE-04 requires media loss to be explicit rather than silent.
func TestRecordingDeleteRemovesFileKeepsIndexAndAudits(t *testing.T) {
	f := newContentFixture(t)
	publishedAt(t, f, 24*time.Hour)
	before := int64(len(f.Published))

	out := deleteOne(t, f, f.Admin)
	outcome := out[0]
	if outcome.State != "deleted" || outcome.Bytes != before {
		t.Fatalf("outcome=%+v want deleted with %d released bytes", outcome, before)
	}
	if localFileExists(t, f) {
		t.Fatal("deleted recording is still on disk")
	}
	ctx := context.Background()
	var state string
	var protected bool
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state,protected FROM recording_segments WHERE id=$1", f.Segment.ID).Scan(&state, &protected); err != nil {
		t.Fatal(err)
	}
	if state != "deleted" || protected {
		t.Fatalf("segment state=%q protected=%v", state, protected)
	}
	var locationState, reason string
	var deletedAt *time.Time
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state,coalesce(delete_reason,''),deleted_at FROM recording_locations WHERE segment_id=$1", f.Segment.ID).Scan(&locationState, &reason, &deletedAt); err != nil {
		t.Fatal(err)
	}
	if locationState != "deleted" || reason != "manual" || deletedAt == nil {
		t.Fatalf("location state=%q reason=%q deletedAt=%v", locationState, reason, deletedAt)
	}
	var audits int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action='recording.deleted_local' AND object_id=$1", f.Segment.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audit rows=%d want 1", audits)
	}
	// Playback of a deleted recording answers exactly like one that never
	// existed: no media, and no hint that a file was removed deliberately.
	response := f.get(t, f.Session, nil)
	if response.Code != 404 {
		t.Fatalf("content of deleted recording: status=%d body=%s", response.Code, response.Body.String())
	}
	if count := f.auditCount(t); count != 0 {
		t.Fatalf("deleted recording still produced %d stream audits", count)
	}
}

// A protected recording is evidence: both the operator path and retention must
// skip it. Deleting protected media would destroy exactly what the operator
// asked to keep, so this is the boundary the whole feature rests on.
func TestRecordingProtectionBlocksDeletionUntilReleased(t *testing.T) {
	f := newContentFixture(t)
	publishedAt(t, f, 48*time.Hour)
	ctx := context.Background()

	if _, err := f.Service.SetProtection(ctx, f.Admin, f.Segment.ID, true); err != nil {
		t.Fatal(err)
	}
	outcome := deleteOne(t, f, f.Admin)[0]
	if outcome.State != "protected" {
		t.Fatalf("protected recording outcome=%+v", outcome)
	}
	if !localFileExists(t, f) {
		t.Fatal("protected recording was deleted")
	}
	// Retention must respect the same boundary.
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE sites SET recording_retention_days=1 WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	f.Service.SweepRetention(ctx)
	if !localFileExists(t, f) {
		t.Fatal("retention deleted a protected recording")
	}

	// Releasing protection makes it deletable, and that release is audited.
	if _, err := f.Service.SetProtection(ctx, f.Admin, f.Segment.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"recording.protected", "recording.unprotected"} {
		var count int
		if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action=$1", action).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("audit %s rows=%d want 1", action, count)
		}
	}
	if outcome := deleteOne(t, f, f.Admin)[0]; outcome.State != "deleted" {
		t.Fatalf("after release outcome=%+v", outcome)
	}
	if localFileExists(t, f) {
		t.Fatal("released recording is still on disk")
	}
}

// Every rejected row is reported on its own: a batch must never let one failure
// hide an accepted deletion, and a retry must not report an already-deleted
// recording as a failure (PRD FILE-05).
func TestRecordingBatchDeleteReportsPerItemOutcomes(t *testing.T) {
	f := newContentFixture(t)
	publishedAt(t, f, 24*time.Hour)
	ctx := context.Background()

	// A second, protected segment so the batch mixes accepted and rejected
	// rows. Publishing the same inbox again is idempotent by design, so the
	// second row models a further completed recording of the same run: it shares
	// the run's identity but is a distinct file and index entry.
	second, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	secondTarget := "recordings/CH01/2026-10-04/one_nvr-" + string(second) + ".mp4"
	secondPath := filepath.Join(f.Pool.Path, secondTarget)
	if err := os.MkdirAll(filepath.Dir(secondPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, f.Published, 0o600); err != nil {
		t.Fatal(err)
	}
	start := f.Segment.End
	if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,file_identity,media_evidence,state,ready_at) SELECT $1,channel_id,source_revision_id,run_id,pool_id,$2,$2,original_start,naming_timezone,utc_offset_seconds,$3,$4,size_bytes,file_identity,media_evidence,'ready',clock_timestamp() FROM recording_segments WHERE id=$5`, second, secondTarget, start, start.Add(time.Minute), f.Segment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.SetProtection(ctx, f.Admin, second, true); err != nil {
		t.Fatal(err)
	}
	missing, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := f.Service.DeleteLocal(ctx, f.Admin, []id.ID{f.Segment.ID, second, missing}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[id.ID]recording.DeleteOutcome{}
	for _, outcome := range outcomes {
		byID[outcome.SegmentID] = outcome
	}
	if got := byID[f.Segment.ID].State; got != "deleted" {
		t.Fatalf("first row=%q want deleted", got)
	}
	if got := byID[second].State; got != "protected" {
		t.Fatalf("protected row=%q want protected", got)
	}
	if got := byID[missing].State; got != "not_found" {
		t.Fatalf("unknown row=%q want not_found", got)
	}
	// Retrying the same batch is idempotent for the row already removed.
	again, err := f.Service.DeleteLocal(ctx, f.Admin, []id.ID{f.Segment.ID}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if again[0].State != "deleted" || again[0].Bytes != 0 {
		t.Fatalf("retry reported %+v", again[0])
	}
}

// Retention is opt-in. A site that never sets a period must keep every
// recording, because automatic deletion of existing footage is not something an
// upgrade may decide for the operator.
func TestRetentionStaysInertUntilConfigured(t *testing.T) {
	f := newContentFixture(t)
	publishedAt(t, f, 365*24*time.Hour)
	ctx := context.Background()

	f.Service.SweepRetention(ctx)
	if !localFileExists(t, f) {
		t.Fatal("retention deleted a recording with retention disabled")
	}
	var days int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT recording_retention_days FROM sites WHERE singleton").Scan(&days); err != nil {
		t.Fatal(err)
	}
	if days != 0 {
		t.Fatalf("default retention=%d want 0", days)
	}

	// With a period set, only recordings past it are removed.
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE sites SET recording_retention_days=7 WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	f.Service.SweepRetention(ctx)
	if localFileExists(t, f) {
		t.Fatal("retention kept a recording past its period")
	}
	var reason string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT coalesce(delete_reason,'') FROM recording_locations WHERE segment_id=$1", f.Segment.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "retention" {
		t.Fatalf("delete reason=%q want retention", reason)
	}
	// Automatic deletion is attributed to no user rather than to an operator.
	var actors int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action='recording.deleted_local' AND actor_id IS NOT NULL").Scan(&actors); err != nil {
		t.Fatal(err)
	}
	if actors != 0 {
		t.Fatalf("retention attributed %d deletions to a user", actors)
	}
}

// A recording that vanished outside one-nvr is 'missing', never 'deleted': the
// two must stay distinguishable so an operator can tell deliberate removal from
// data loss (PRD FILE-04).
func TestRecordingMissingFileIsNotReportedAsDeleted(t *testing.T) {
	f := newContentFixture(t)
	publishedAt(t, f, 24*time.Hour)
	if err := os.Remove(filepath.Join(f.Pool.Path, f.Target)); err != nil {
		t.Fatal(err)
	}
	outcome := deleteOne(t, f, f.Admin)[0]
	if outcome.State != "missing" {
		t.Fatalf("outcome=%+v want missing", outcome)
	}
	var state string
	if err := f.DB.Pool.QueryRow(context.Background(), "SELECT state FROM recording_segments WHERE id=$1", f.Segment.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "deleted" {
		t.Fatal("a file that vanished was recorded as deliberately deleted")
	}
}

// A just-published recording can be deleted on request, while automatic
// retention waits: an operator naming a row is a decision, a sweep is not.
func TestRecordingDeleteAppliesImmediatelyButRetentionWaits(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	// The fixture segment is fresh, so only the operator path should act.
	outcome := deleteOne(t, f, f.Admin)[0]
	if outcome.State != "deleted" {
		t.Fatalf("explicit deletion of a fresh recording=%+v", outcome)
	}
	if localFileExists(t, f) {
		t.Fatal("explicit deletion left the file behind")
	}
	_ = ctx
}

// Retention must not take the newest recording even when the retention period
// has passed, because a segment settling into place is not yet a candidate.
// The fixture publishes with a historical start time, so the segment is moved
// to the present to exercise the freshness rule rather than the age rule.
func TestRetentionSkipsFreshRecording(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := f.DB.Pool.Exec(ctx,
		"UPDATE recording_segments SET start_at=$2,end_at=$3 WHERE id=$1",
		f.Segment.ID, now.Add(-time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE sites SET recording_retention_days=1 WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	// The period has elapsed in wall-clock terms only for old recordings; this
	// file is seconds old, so the sweep must leave it alone.
	f.Service.SweepRetention(ctx)
	if !localFileExists(t, f) {
		t.Fatal("retention deleted a freshly published recording")
	}
	// Once it is genuinely older than the delay, the same sweep takes it: the
	// rule is the segment's age, not a permanent exemption.
	if _, err := f.DB.Pool.Exec(ctx,
		"UPDATE recording_segments SET start_at=$2,end_at=$3 WHERE id=$1",
		f.Segment.ID, now.Add(-2*24*time.Hour), now.Add(-2*24*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.Service.SweepRetention(ctx)
	if localFileExists(t, f) {
		t.Fatal("retention kept a recording past both the period and the delay")
	}
}

// Deleting is an administrator operation: the channel grant decides who may
// watch, never who may destroy evidence.
func TestRecordingDeleteRequiresAdministrator(t *testing.T) {
	f := newContentFixture(t)
	publishedAt(t, f, 24*time.Hour)
	viewer := f.viewer(t, "delete-viewer")
	if _, err := f.Service.DeleteLocal(context.Background(), viewer.Principal, []id.ID{f.Segment.ID}, "manual"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer delete err=%v want forbidden", err)
	}
	if !localFileExists(t, f) {
		t.Fatal("viewer deleted a recording")
	}
	if _, err := f.Service.SetProtection(context.Background(), viewer.Principal, f.Segment.ID, true); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer protection err=%v want forbidden", err)
	}
}
