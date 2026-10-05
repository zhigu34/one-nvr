package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

func testedSource(t *testing.T) (publishFixture, *controlledMedia, channel.Change, id.ID) {
	t.Helper()
	f, media, test, revision := prepareControlledSourceTest(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE stream_sessions SET state='active' WHERE id=(SELECT stream_session_id FROM recording_runs WHERE id=$1)", f.Run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET storage_pool_id=$2 WHERE id=$1", f.Channel, f.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='continuous',event_recording=true WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"api", "worker", "zlm"} {
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,free_bytes,total_bytes) VALUES($1,$2,'healthy','controlled_peer',clock_timestamp(),clock_timestamp()+interval '3 minutes',1099511627776,2199023255552) ON CONFLICT(pool_id,service) DO UPDATE SET state='healthy',expires_at=EXCLUDED.expires_at,free_bytes=EXCLUDED.free_bytes`, f.Pool.ID, service); err != nil {
			t.Fatal(err)
		}
	}
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteSourceTest); err != nil {
		t.Fatal(err)
	}
	return f, media, test, revision
}
func executeChange(t *testing.T, f publishFixture, kind string) {
	t.Helper()
	ctx := context.Background()
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteSourceChange); err != nil {
		t.Fatal(err)
	}
}
func TestSourceApplyRejectsExpiredOrMismatchedMainEvidence(t *testing.T) {
	for _, fault := range []string{"expired", "digest"} {
		t.Run(fault, func(t *testing.T) {
			f, _, test, revision := testedSource(t)
			ctx := context.Background()
			statement := "UPDATE source_tests SET expires_at=clock_timestamp()-interval '1 second',observed_at=clock_timestamp()-interval '6 minutes' WHERE id=$1"
			if fault == "digest" {
				statement = "UPDATE source_tests SET configuration_digest=decode(repeat('00',32),'hex') WHERE id=$1"
			}
			if _, err := f.DB.Pool.Exec(ctx, statement, test.TestID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, channel.SourceApplyInput{RevisionID: revision, TestID: *test.TestID, ExpectedVersion: 3}, "apply-invalid"); !errors.Is(err, auth.ErrConflict) {
				t.Fatal("invalid evidence admitted", err)
			}
			var current id.ID
			if err := f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", f.Channel).Scan(&current); err != nil || current != f.Revision {
				t.Fatal("rejected request interrupted old source", err)
			}
		})
	}
}
func TestSourceSwitchCommitAndClearPreserveHistory(t *testing.T) {
	f, media, test, revision := testedSource(t)
	ctx := context.Background()
	media.failPath = "/sub-bad" // degraded sub does not prevent a healthy main switch.
	input := channel.SourceApplyInput{RevisionID: revision, TestID: *test.TestID, ExpectedVersion: 3}
	change, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, input, "apply-source")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, input, "apply-source")
	if err != nil || replay.JobID != change.JobID {
		t.Fatal("apply not idempotent", err)
	}
	executeChange(t, f, "source.apply")
	var current id.ID
	var phase string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", f.Channel).Scan(&current); err != nil || current != revision {
		t.Fatal("verified source not committed", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT phase FROM source_switches WHERE job_id=$1", change.JobID).Scan(&phase); err != nil || phase != "committed" {
		t.Fatal("switch outcome missing", phase, err)
	}
	segment, err := f.Service.Publish(ctx, f.Inbox)
	if err != nil || segment.SourceRevisionID != f.Revision || segment.RunID != f.Run {
		t.Fatal("late old tail changed provenance", segment, err)
	}
	var version int64
	if err := f.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", f.Channel).Scan(&version); err != nil {
		t.Fatal(err)
	}
	clear, err := f.Service.Sources.RequestClear(ctx, f.Admin, f.Channel, version, "clear-source")
	if err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.clear")
	var empty, events bool
	var mode string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id IS NULL FROM channels WHERE id=$1", f.Channel).Scan(&empty); err != nil || !empty {
		t.Fatal("source not cleared", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT mode,event_recording FROM recording_policies WHERE channel_id=$1", f.Channel).Scan(&mode, &events); err != nil || mode != "continuous" || !events {
		t.Fatal("clear reset recording policy", mode, events, err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT phase FROM source_switches WHERE job_id=$1", clear.JobID).Scan(&phase); err != nil || phase != "committed" {
		t.Fatal("clear not committed", err)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if len(media.urls) != 0 {
		t.Fatal("cleared source still pulled")
	}
}
func TestSourceSwitchRollbackAndLateTail(t *testing.T) {
	f, media, test, revision := testedSource(t)
	ctx := context.Background()
	// Only the new source fails. The old source has a different IP.
	media.failURL = "rtsp://192.168.33.21:554/main"
	change, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, channel.SourceApplyInput{RevisionID: revision, TestID: *test.TestID, ExpectedVersion: 3}, "rollback-source")
	if err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.apply")
	var current id.ID
	var phase string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", f.Channel).Scan(&current); err != nil || current != f.Revision {
		t.Fatal("old revision not restored", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT phase FROM source_switches WHERE job_id=$1", change.JobID).Scan(&phase); err != nil || phase != "rolled_back" {
		t.Fatal("failed source not rolled back", phase, err)
	}
	var count int
	var restoredRun id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_runs WHERE channel_id=$1 AND state='recording'", f.Channel).Scan(&count); err != nil || count != 1 {
		t.Fatal("rollback recorder count", count, err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM recording_runs WHERE channel_id=$1 AND state='recording'", f.Channel).Scan(&restoredRun); err != nil || restoredRun == f.Run {
		t.Fatal("rollback reused historical run", err)
	}
	var newer bool
	if err := f.DB.Pool.QueryRow(ctx, "SELECT ss.generation>old.generation FROM recording_runs r JOIN stream_sessions ss ON ss.id=r.stream_session_id JOIN recording_runs oldrun ON oldrun.id=$2 JOIN stream_sessions old ON old.id=oldrun.stream_session_id WHERE r.id=$1", restoredRun, f.Run).Scan(&newer); err != nil || !newer {
		t.Fatal("rollback physical generation reused", err)
	}
	segment, err := f.Service.Publish(ctx, f.Inbox)
	if err != nil || segment.RunID != f.Run || segment.SourceRevisionID != f.Revision {
		t.Fatal("late tail not owned by old run", err)
	}
	var gaps int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_gaps WHERE switch_id=(SELECT id FROM source_switches WHERE job_id=$1) AND end_at IS NOT NULL", change.JobID).Scan(&gaps); err != nil || gaps != 1 {
		t.Fatal("switch gap not recorded", gaps, err)
	}
}
func TestSourceSwitchRollbackFailureIsVisible(t *testing.T) {
	f, media, test, revision := testedSource(t)
	ctx := context.Background()
	media.failPath = "/main"
	change, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, channel.SourceApplyInput{RevisionID: revision, TestID: *test.TestID, ExpectedVersion: 3}, "rollback-failed")
	if err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.apply")
	var phase string
	var active int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT phase FROM source_switches WHERE job_id=$1", change.JobID).Scan(&phase); err != nil || phase != "failed" {
		t.Fatal("rollback failure hidden", phase, err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM stream_sessions WHERE channel_id=$1 AND purpose='main' AND state='active'", f.Channel).Scan(&active); err != nil || active != 0 {
		t.Fatal("failed rollback falsely online", active, err)
	}
	var unhealthy bool
	if err := f.DB.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM source_observations WHERE channel_id=$1 AND kind='main' AND state='unavailable' AND expires_at>$2)", f.Channel, time.Now()).Scan(&unhealthy); err != nil || !unhealthy {
		t.Fatal("rollback failure not observable", err)
	}
}

func TestSourceSwitchQueuedEvidenceExpiredDoesNotStopOld(t *testing.T) {
	f, media, test, revision := testedSource(t)
	ctx := context.Background()
	change, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, channel.SourceApplyInput{RevisionID: revision, TestID: *test.TestID, ExpectedVersion: 3}, "queued-proof-expired")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE source_tests SET expires_at=clock_timestamp()-interval '1 second',observed_at=clock_timestamp()-interval '6 minutes' WHERE id=$1", test.TestID); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.apply")
	var phase string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT phase FROM source_switches WHERE job_id=$1", change.JobID).Scan(&phase); err != nil || phase != "cancelled" {
		t.Fatal("queued expired evidence still switched", phase, err)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if len(media.urls) != 1 {
		t.Fatal("invalid queued evidence stopped old source")
	}
}

func TestSourceSwitchResumeRecorderAfterLostResponse(t *testing.T) {
	f, media, test, revision := testedSource(t)
	ctx := context.Background()
	change, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, channel.SourceApplyInput{RevisionID: revision, TestID: *test.TestID, ExpectedVersion: 3}, "start-response-lost")
	if err != nil {
		t.Fatal(err)
	}
	var existingRun, session id.ID
	media.AfterStart = func() {
		media.AfterStart = nil
		if err := f.DB.Pool.QueryRow(ctx, "SELECT id,stream_session_id FROM recording_runs WHERE channel_id=$1 AND state='starting' AND purpose='continuous'", f.Channel).Scan(&existingRun, &session); err != nil {
			t.Error(err)
		}
		if _, err := f.DB.Pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", change.JobID); err != nil {
			t.Error(err)
		}
	}
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.apply")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, f.Service.ExecuteSourceChange); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatal("lost recorder response did not lose lease", err)
	}
	var beforeDeadline time.Time
	if err := f.DB.Pool.QueryRow(ctx, "SELECT phase_deadline FROM source_switches WHERE job_id=$1", change.JobID).Scan(&beforeDeadline); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.apply")
	var run, activeSession id.ID
	var afterDeadline time.Time
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id,stream_session_id FROM recording_runs WHERE channel_id=$1 AND state='recording'", f.Channel).Scan(&run, &activeSession); err != nil || run != existingRun || activeSession != session {
		t.Fatal("lost response allocated duplicate run", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT phase_deadline FROM source_switches WHERE job_id=$1", change.JobID).Scan(&afterDeadline); err != nil || !beforeDeadline.Equal(afterDeadline) {
		t.Fatal("recovery reset persisted deadline", err)
	}
}

func TestSourceFirstApplyNoneRequiresExplicitChoiceAndNoPool(t *testing.T) {
	f, media, test, revision := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=NULL,storage_pool_id=NULL WHERE id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET state='stopped' WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE stream_sessions SET state='closed' WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	media.urls = map[string]string{}
	media.recording = map[string]bool{}
	input := channel.SourceApplyInput{RevisionID: revision, TestID: *test.TestID, ExpectedVersion: 3}
	if _, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, input, "first-implicit"); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("first policy implicitly selected", err)
	}
	none := "none"
	input.FirstRecordingMode = &none
	if _, err := f.Service.Sources.RequestApply(ctx, f.Admin, f.Channel, input, "first-explicit-none"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.apply")
	var poolNil, events bool
	var mode string
	var recorders int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT storage_pool_id IS NULL FROM channels WHERE id=$1", f.Channel).Scan(&poolNil); err != nil || !poolNil {
		t.Fatal("none bound a pool", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT mode,event_recording FROM recording_policies WHERE channel_id=$1", f.Channel).Scan(&mode, &events); err != nil || mode != "none" || !events {
		t.Fatal("explicit none changed event flag", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_runs WHERE channel_id=$1 AND state IN ('starting','recording','stopping')", f.Channel).Scan(&recorders); err != nil || recorders != 0 {
		t.Fatal("none implicitly records", recorders, err)
	}
}

func TestSourceClearPreservesExistingPlannedPolicy(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='planned' WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Sources.RequestClear(ctx, f.Admin, f.Channel, 3, "clear-planned"); err != nil {
		t.Fatal("clear rejected existing policy", err)
	}
	executeChange(t, f, "source.clear")
	var mode string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT mode FROM recording_policies WHERE channel_id=$1", f.Channel).Scan(&mode); err != nil || mode != "planned" {
		t.Fatal("clear rewrote historical policy", err)
	}
}
