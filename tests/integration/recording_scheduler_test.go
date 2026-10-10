package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/recording"
	"os"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/storage"
)

func TestRecordingMonitorEmptySlotsDoNotExpireConfiguredSource(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "none", "monitor-no-recording"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM recording_bitrate_samples WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	var slots int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM channels").Scan(&slots); err != nil || slots != 16 {
		t.Fatal("requires permanent empty slots", slots, err)
	}
	monitor, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- f.Service.Monitor(monitor) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(18 * time.Second)
	for time.Now().Before(deadline) {
		var samples int
		if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_bitrate_samples WHERE channel_id=$1 AND valid AND observed_at>clock_timestamp()-interval '30 seconds'", f.Channel).Scan(&samples); err != nil {
			t.Fatal(err)
		}
		if samples >= 2 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("empty permanent slots starved fresh paired observations of the configured source")
}

func TestRecordingPolicyRefreshesExpiredBitrateBeforeStart(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(map[bool]string{false: "advancing", true: "stalled"}[stalled], func(t *testing.T) {
			f, media, _, _ := testedSource(t)
			ctx := context.Background()
			if _, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "none", "fresh-policy-off"); err != nil {
				t.Fatal(err)
			}
			executeChange(t, f, "source.policy_apply")
			// A user may spend longer than the 30-second freshness window in settings.
			// Once the policy job owns this channel, Monitor cannot refresh it for us.
			if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_bitrate_samples SET observed_at=clock_timestamp()-interval '1 minute' WHERE channel_id=$1", f.Channel); err != nil {
				t.Fatal(err)
			}
			if stalled {
				media.FrozenFrames = 100
			}
			var version int64
			if err := f.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", f.Channel).Scan(&version); err != nil {
				t.Fatal(err)
			}
			change, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, version, "continuous", "fresh-policy-on")
			if err != nil {
				t.Fatal(err)
			}
			executeChange(t, f, "source.policy_apply")
			var state, reason, mode string
			expectedState, expectedMode, expectedCount := "succeeded", "continuous", 1
			if stalled {
				expectedState, expectedMode, expectedCount = "failed", "none", 0
			}
			if err := f.DB.Pool.QueryRow(ctx, "SELECT j.state,coalesce(j.error_code,''),p.mode FROM jobs j JOIN recording_policies p ON p.channel_id=$2 WHERE j.id=$1", change.JobID, f.Channel).Scan(&state, &reason, &mode); err != nil || state != expectedState || mode != expectedMode {
				t.Fatal("policy did not collect fresh evidence under its own channel lock", state, reason, mode, err)
			}
			if stalled && reason != "bitrate_unknown" {
				t.Fatal("stalled source accepted as fresh", reason)
			}
			media.mu.Lock()
			defer media.mu.Unlock()
			var recordingCount int
			for _, active := range media.recording {
				if active {
					recordingCount++
				}
			}
			if recordingCount != expectedCount {
				t.Fatal("policy did not start exactly one verified recorder", recordingCount)
			}
		})
	}
}

func TestRecordingPolicyPreflightFailureReleasesChannel(t *testing.T) {
	for _, missing := range []string{"main", "capacity_peers", "zlm_proof"} {
		t.Run(missing, func(t *testing.T) {
			reason := "pool_unavailable"
			if missing == "main" {
				reason = "source_unavailable"
			}
			f, _, _, _ := testedSource(t)
			ctx := context.Background()
			if _, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "none", "preflight-off"); err != nil {
				t.Fatal(err)
			}
			executeChange(t, f, "source.policy_apply")
			var version int64
			if err := f.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", f.Channel).Scan(&version); err != nil {
				t.Fatal(err)
			}
			change, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, version, "continuous", "preflight-on")
			if err != nil {
				t.Fatal(err)
			}
			// Evidence can disappear after admission but before the durable job runs.
			if missing == "main" {
				if _, err := f.DB.Pool.Exec(ctx, "UPDATE stream_sessions SET state='closed' WHERE channel_id=$1 AND purpose='main'", f.Channel); err != nil {
					t.Fatal(err)
				}
				if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM recording_bitrate_samples WHERE channel_id=$1", f.Channel); err != nil {
					t.Fatal(err)
				}
			} else {
				statement := "UPDATE storage_pool_checks SET expires_at=clock_timestamp()-interval '1 second' WHERE pool_id=$1"
				if missing == "zlm_proof" {
					statement += " AND service='zlm'"
				}
				if _, err := f.DB.Pool.Exec(ctx, statement, f.Pool.ID); err != nil {
					t.Fatal(err)
				}
			}
			executeChange(t, f, "source.policy_apply")
			var state, code, phase, mode string
			if err := f.DB.Pool.QueryRow(ctx, "SELECT j.state,coalesce(j.error_code,''),s.phase,p.mode FROM jobs j JOIN source_switches s ON s.job_id=j.id JOIN recording_policies p ON p.channel_id=s.channel_id WHERE j.id=$1", change.JobID).Scan(&state, &code, &phase, &mode); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || code != reason || phase != "rolled_back" || mode != "none" {
				t.Fatal("preflight failure retained a durable channel guard instead of preserving the old policy", state, code, phase, mode)
			}
			var live, recorders int
			if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM source_switches WHERE channel_id=$1 AND state='running'", f.Channel).Scan(&live); err != nil || live != 0 {
				t.Fatal("monitor recovery remains blocked", live, err)
			}
			if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_runs WHERE channel_id=$1 AND state IN ('starting','recording','stopping')", f.Channel).Scan(&recorders); err != nil || recorders != 0 {
				t.Fatal("failed enable changed recording state", recorders, err)
			}
		})
	}
}

func TestRecordingPolicyMaintainsFreshBitrateDuringCapacityRecovery(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "none", "recovery-rate-off"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM recording_bitrate_samples WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	for _, age := range []int{28, 18} {
		sample, err := id.New()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO recording_bitrate_samples(id,channel_id,source_revision_id,stream_session_id,bytes_per_second,frames,valid,observed_at) SELECT $2,channel_id,source_revision_id,id,1048576,1,true,clock_timestamp()-make_interval(secs=>$3) FROM stream_sessions WHERE channel_id=$1 AND purpose='main' AND state='active'`, f.Channel, sample, age); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.DB.Pool.Exec(ctx, "INSERT INTO recording_capacity_blocks(channel_id,reason_code) VALUES($1,'pool_unavailable')", f.Channel); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := f.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", f.Channel).Scan(&version); err != nil {
		t.Fatal(err)
	}
	change, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, version, "continuous", "recovery-rate-on")
	if err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	var state, reason, mode string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT j.state,coalesce(j.error_code,''),p.mode FROM jobs j JOIN recording_policies p ON p.channel_id=$2 WHERE j.id=$1", change.JobID, f.Channel).Scan(&state, &reason, &mode); err != nil || state != "succeeded" || mode != "continuous" {
		t.Fatal("owned policy let valid bitrate expire while awaiting two capacity recovery observations", state, reason, mode, err)
	}
}

func TestRecordingPolicyRefreshesWriteProofDuringCapacityRecovery(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	data, err := os.ReadFile(f.Completion.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	f.Service.Media = poolWritingMedia{media, f, data}
	if _, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "none", "recovery-proof-off"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	// Admission and preflight see a valid real-write proof. It expires while
	// the owned job waits for the second mandatory capacity recovery sample.
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE storage_pool_checks SET expires_at=clock_timestamp()+interval '5 seconds' WHERE pool_id=$1 AND service='zlm'", f.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "INSERT INTO recording_capacity_blocks(channel_id,reason_code) VALUES($1,'pool_unavailable')", f.Channel); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := f.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", f.Channel).Scan(&version); err != nil {
		t.Fatal(err)
	}
	change, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, version, "continuous", "recovery-proof-on")
	if err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	var state, code, mode string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT j.state,coalesce(j.error_code,''),p.mode FROM jobs j JOIN recording_policies p ON p.channel_id=$2 WHERE j.id=$1", change.JobID, f.Channel).Scan(&state, &code, &mode); err != nil || state != "succeeded" || mode != "continuous" {
		t.Fatal("owned recovery let its write proof expire instead of verifying a fresh pool file", state, code, mode, err)
	}
	var fresh bool
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state='healthy' AND reason_code='zlm_media_verified' AND expires_at>clock_timestamp() FROM storage_pool_checks WHERE pool_id=$1 AND service='zlm'", f.Pool.ID).Scan(&fresh); err != nil || !fresh {
		t.Fatal("recording started without fresh actual pool file proof", fresh, err)
	}
}

func TestRecordingMonitorCleansDisabledRuntimeSessions(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	// The existing physical main was adopted without a source-switch receipt.
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET enabled=false WHERE id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	monitor, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- f.Service.Monitor(monitor) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		media.mu.Lock()
		left := len(media.urls)
		media.mu.Unlock()
		if left == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("disabled runtime session was excluded from cleanup")
}

func TestRecordingOffKeepsPullAndHistory(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	change, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "none", "recording-off")
	if err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	var mode, runState string
	var current id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT mode FROM recording_policies WHERE channel_id=$1", f.Channel).Scan(&mode); err != nil || mode != "none" {
		t.Fatal("recording off not committed", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&runState); err != nil || runState != "stopped" {
		t.Fatal("old recording not stopped", runState, err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", f.Channel).Scan(&current); err != nil || current != f.Revision {
		t.Fatal("off changed current source", err)
	}
	media.mu.Lock()
	if len(media.urls) != 1 {
		t.Error("off stopped/replaced upstream pull")
	}
	for _, recording := range media.recording {
		if recording {
			t.Error("off still recording")
		}
	}
	media.mu.Unlock()
	segment, err := f.Service.Publish(ctx, f.Inbox)
	if err != nil || segment.SourceRevisionID != f.Revision {
		t.Fatal("off lost history", err)
	}
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	var open int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_runs WHERE channel_id=$1 AND state IN ('starting','recording','stopping')", f.Channel).Scan(&open); err != nil || open != 0 {
		t.Fatal("reconcile reset off policy", open, err)
	}
	replay, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "none", "recording-off")
	if err != nil || replay.JobID != change.JobID {
		t.Fatal("policy replay not stable", err)
	}
}

func TestPoolSwitchKeepsOldLocations(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	segment, err := f.Service.Publish(ctx, f.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	f.Service.Pools.Roots = append(f.Service.Pools.Roots, base)
	f.Service.Pools.Auth = f.Auth
	pool, err := f.Service.Pools.Register(ctx, f.Admin, storage.RegisterInput{Name: "New recording pool", Path: base})
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"api", "worker", "zlm"} {
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,free_bytes,total_bytes,filesystem_id) VALUES($1,$2,'healthy','controlled',clock_timestamp(),clock_timestamp()+interval '3 minutes',1099511627776,2199023255552,'controlled-filesystem') ON CONFLICT(pool_id,service) DO UPDATE SET state='healthy',expires_at=EXCLUDED.expires_at,free_bytes=EXCLUDED.free_bytes,filesystem_id=EXCLUDED.filesystem_id`, pool.ID, peer); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Service.Sources.BindPool(ctx, f.Admin, f.Channel, pool.ID, 3, "switch-pool"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.pool_switch")
	var bound, runPool, locationPool id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT storage_pool_id FROM channels WHERE id=$1", f.Channel).Scan(&bound); err != nil || bound != pool.ID {
		t.Fatal("new pool not bound", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT pool_id FROM recording_runs WHERE channel_id=$1 AND state='recording'", f.Channel).Scan(&runPool); err != nil || runPool != pool.ID {
		t.Fatal("new run not in selected pool", err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT pool_id FROM recording_locations WHERE segment_id=$1", segment.ID).Scan(&locationPool); err != nil || locationPool != f.Pool.ID {
		t.Fatal("old location moved on pool change", err)
	}
	defaultPool := true
	if _, err := f.Service.Pools.Update(ctx, f.Admin, f.Pool.ID, f.Pool.Version, storage.UpdateInput{IsDefault: &defaultPool}); err != nil {
		t.Fatal(err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT storage_pool_id FROM channels WHERE id=$1", f.Channel).Scan(&bound); err != nil || bound != pool.ID {
		t.Fatal("default pool changed explicit binding", err)
	}
	if err := f.Service.Pools.Delete(ctx, f.Admin, pool.ID, pool.Version); err == nil {
		t.Fatal("referenced pool removed")
	}
}

func TestRecordingRecoveryKeepsLiveRunAcrossWorkerRestart(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	var run id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM recording_runs WHERE channel_id=$1 AND state='recording'", f.Channel).Scan(&run); err != nil || run != f.Run {
		t.Fatal("live recorder was restarted on worker recovery", err)
	}
}
func TestRecordingRecoveryRuntimeFenceTracksConfiguration(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	e, err := channel.NewRuntimeExecution(ctx, f.DB, f.Channel)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET version=version+1 WHERE id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	if err := e.Check(ctx); err == nil {
		t.Fatal("old runtime accepted changed configuration")
	}
}

func TestRecordingRecoveryDisabledPoolStopsWritesAndKeepsHistory(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	f.Service.Pools.Auth = f.Auth
	disabled := false
	if _, err := f.Service.Pools.Update(ctx, f.Admin, f.Pool.ID, f.Pool.Version, storage.UpdateInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&state); err != nil || state != "stopped" {
		t.Fatal("disabled pool still has an active recorder", state, err)
	}
	media.mu.Lock()
	if len(media.urls) != 1 {
		t.Error("pool failure stopped upstream pull")
	}
	for _, recording := range media.recording {
		if recording {
			t.Error("disabled pool still writes")
		}
	}
	media.mu.Unlock()
	if segment, err := f.Service.Publish(ctx, f.Inbox); err != nil || segment.State != "ready" {
		t.Fatal("disabled pool hid readable historical tail", err)
	}
	status, err := f.Service.Sources.GetStatus(ctx, f.Admin, f.Channel)
	if err != nil || status.Recording.State != "unavailable" || status.Recording.Reason != "pool_disabled" {
		t.Fatal("pool-disabled reason hidden", status.Recording, err)
	}
}

func TestRecordingRecoveryFrozenFramesNotHealthy(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_bitrate_samples SET observed_at=clock_timestamp()-CASE WHEN frames=101 THEN interval '11 seconds' ELSE interval '22 seconds' END WHERE stream_session_id=(SELECT stream_session_id FROM recording_runs WHERE id=$1)", f.Run); err != nil {
		t.Fatal(err)
	}
	media.mu.Lock()
	media.FrozenFrames = 101
	media.mu.Unlock()
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	status, err := f.Service.Sources.GetStatus(ctx, f.Admin, f.Channel)
	if err != nil || status.Main.State != "unavailable" || status.Main.Reason != "frame_progress_stalled" {
		t.Fatal("registered frozen stream reported available", status.Main, err)
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&state); err != nil || state != "stopped" {
		t.Fatal("frozen source kept recording", state, err)
	}
	var gaps int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_gaps WHERE channel_id=$1 AND end_at IS NULL AND reason_code='frame_progress_stalled'", f.Channel).Scan(&gaps); err != nil || gaps != 1 {
		t.Fatal("missing observed gap", gaps, err)
	}
}

func TestRecordingRecoveryLowSpaceStopsOnlyRecorder(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE storage_pool_checks SET free_bytes=1073741824 WHERE pool_id=$1 AND service IN ('api','worker')", f.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&state); err != nil || state != "stopped" {
		t.Fatal("low capacity kept writing", state, err)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if len(media.urls) != 1 {
		t.Fatal("low capacity stopped pull")
	}
	status, err := f.Service.Sources.GetStatus(ctx, f.Admin, f.Channel)
	if err != nil || status.Recording.State != "unavailable" || status.Recording.Reason != "low_space" {
		t.Fatal("missing capacity reason", status.Recording, err)
	}
}

func TestRecordingRecoveryRequiresRecentCompletion(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET started_at=clock_timestamp()-interval '5 minutes',last_completion_at=NULL WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	status, err := f.Service.Sources.GetStatus(ctx, f.Admin, f.Channel)
	if err != nil || status.Recording.State != "unknown" || status.Recording.Reason != "completion_stale" {
		t.Fatal("recording flag treated as file completeness", status.Recording, err)
	}
	var evidence string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT evidence FROM recording_gaps WHERE channel_id=$1 AND end_at IS NULL", f.Channel).Scan(&evidence); err != nil || evidence != "unknown" {
		t.Fatal("missing unknown interval", evidence, err)
	}
}

func TestRecordingRecoverySourceCreatesNewGenerationAndRun(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	var oldSession id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&oldSession); err != nil {
		t.Fatal(err)
	}
	media.mu.Lock()
	delete(media.urls, string(oldSession))
	delete(media.recording, string(oldSession))
	media.mu.Unlock()
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	var fresh id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM stream_sessions WHERE channel_id=$1 AND purpose='main' AND state='active'", f.Channel).Scan(&fresh); err != nil || fresh == oldSession {
		t.Fatal("source recovery reused closed identity", fresh, err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_bitrate_samples SET observed_at=clock_timestamp()-interval '11 seconds' WHERE stream_session_id=$1", fresh); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	var run id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM recording_runs WHERE stream_session_id=$1 AND state='recording'", fresh).Scan(&run); err != nil || run == f.Run {
		t.Fatal("recovery did not allocate new run", run, err)
	}
	history, err := f.Service.Publish(ctx, f.Inbox)
	if err != nil || history.SourceRevisionID != f.Revision {
		t.Fatal("recovery destroyed old tail", err)
	}
}

func TestRecordingCapacityFailedPoolChangeKeepsOldRun(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	base := t.TempDir()
	f.Service.Pools.Roots = append(f.Service.Pools.Roots, base)
	f.Service.Pools.Auth = f.Auth
	pool, err := f.Service.Pools.Register(ctx, f.Admin, storage.RegisterInput{Name: "Target", Path: base})
	if err != nil {
		t.Fatal(err)
	}
	for _, peer := range []string{"api", "worker", "zlm"} {
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,free_bytes,total_bytes,filesystem_id) VALUES($1,$2,'healthy','controlled',clock_timestamp(),clock_timestamp()+interval '3 minutes',1099511627776,2199023255552,'controlled-filesystem') ON CONFLICT(pool_id,service) DO UPDATE SET state='healthy',expires_at=EXCLUDED.expires_at,free_bytes=EXCLUDED.free_bytes,filesystem_id=EXCLUDED.filesystem_id`, pool.ID, peer); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Service.Sources.BindPool(ctx, f.Admin, f.Channel, pool.ID, 3, "target-capacity-fails"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE storage_pool_checks SET free_bytes=1073741824 WHERE pool_id=$1 AND service IN ('api','worker')", pool.ID); err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: f.DB}
	lease, err := repo.Claim(ctx, "source.pool_switch")
	if err != nil {
		t.Fatal(err)
	}
	_ = jobs.Execute(ctx, repo, lease, f.Service.ExecuteSourceChange)
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&state); err != nil || state != "recording" {
		t.Fatal("target capacity failure interrupted old recorder", state, err)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	var old id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if !media.recording[string(old)] {
		t.Fatal("old physical recorder was stopped")
	}
}

func TestRecordingRecoveryUnconfiguredChannelRemainsIdle(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	var ch id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=2").Scan(&ch); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Reconcile(ctx, ch); err != nil {
		t.Fatal("unconfigured slot failed idle reconciliation", err)
	}
}

func TestRecordingRecoveryDBUnavailableKeepsUpstream(t *testing.T) {
	f, media, _, _ := testedSource(t)
	f.DB.Pool.Close()
	if err := f.Service.Reconcile(context.Background(), f.Channel); err == nil {
		t.Fatal("missing database treated as known configuration")
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if len(media.urls) != 1 {
		t.Fatal("DB unavailable stopped pull")
	}
	for _, active := range media.recording {
		if !active {
			t.Fatal("DB unavailable stopped known upstream recording")
		}
	}
}

func TestPoolSwitchReferencedEmptyPoolCannotDelete(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	base := t.TempDir()
	f.Service.Pools.Roots = append(f.Service.Pools.Roots, base)
	f.Service.Pools.Auth = f.Auth
	pool, err := f.Service.Pools.Register(ctx, f.Admin, storage.RegisterInput{Name: "Referenced empty pool", Path: base})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET storage_pool_id=$1 WHERE channel_no=2", pool.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Pools.Delete(ctx, f.Admin, pool.ID, pool.Version); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("referenced empty pool must return explicit conflict", err)
	}
}

func TestRecordingRecoveryCompletionUsesSegmentEnd(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	var raw []byte
	if err := f.DB.Pool.QueryRow(ctx, "SELECT payload FROM hook_inbox WHERE id=$1", f.Inbox).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var c recording.Completion
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Publish(ctx, f.Inbox); err != nil {
		t.Fatal(err)
	}
	var latest time.Time
	if err := f.DB.Pool.QueryRow(ctx, "SELECT last_completion_at FROM recording_runs WHERE id=$1", f.Run).Scan(&latest); err != nil || latest.Sub(c.StartTime.Add(c.Duration)).Abs() > time.Microsecond {
		t.Fatal("recent completed time uses segment beginning", latest, err)
	}
}

func TestRecordingOffUnchangedPolicyKeepsActiveRun(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "continuous", "unchanged-policy"); err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	var run id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM recording_runs WHERE channel_id=$1 AND state='recording'", f.Channel).Scan(&run); err != nil || run != f.Run {
		t.Fatal("unchanged policy interrupted recorder", run, err)
	}
}

func TestRecordingRecoveryIdleProbeStopsOrphanOnly(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	media := newControlledMedia()
	f.Service.Media = media
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	f.Service.Sources = channel.NewSources(f.DB, f.Auth, f.Site.Secrets, network)
	f.Service.FreshNetwork = func(context.Context) (channel.NetworkPolicy, error) { return network, nil }
	f.Service.ProbeToken = "controlled-probe"
	ss, _ := id.New()
	run, _ := id.New()
	if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state,proxy_key) VALUES($1,$2,$3,2,'one_nvr',$1::uuid::text,'test','active','__defaultVhost__/one_nvr/'||$1::uuid::text)`, ss, f.Channel, f.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state) VALUES($1,$2,$3,$4,$5,$6,$7,'probe','recording')`, run, f.Site.Secrets.SiteID, f.Channel, f.Revision, ss, f.Pool.ID, ".work/probes/zlm/"+string(run)); err != nil {
		t.Fatal(err)
	}
	var formal id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&formal); err != nil {
		t.Fatal(err)
	}
	media.urls[string(ss)] = "rtsp://192.168.33.20/main"
	media.recording[string(ss)] = true
	media.urls[string(formal)] = "rtsp://192.168.33.20/main"
	media.recording[string(formal)] = true
	_ = f.Service.CheckPool(ctx, f.Pool.ID)
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", run).Scan(&state); err != nil || state != "stopped" {
		t.Fatal("Worker restart left orphan probe recording", state, err)
	}
	media.mu.Lock()
	defer media.mu.Unlock()
	if _, exists := media.urls[string(ss)]; exists {
		t.Fatal("orphan temporary proxy retained")
	}
	if !media.recording[string(formal)] {
		t.Fatal("probe cleanup stopped ordinary recording")
	}
}

func TestRecordingOffIdlePoolDoesNotCreateAutomaticProbe(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	media := newControlledMedia()
	f.Service.Media = media
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	f.Service.Sources = channel.NewSources(f.DB, f.Auth, f.Site.Secrets, network)
	f.Service.FreshNetwork = func(context.Context) (channel.NetworkPolicy, error) { return network, nil }
	f.Service.ProbeToken = "controlled-probe"
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2,storage_pool_id=$3 WHERE id=$1", f.Channel, f.Revision, f.Pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET state='stopped' WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE storage_pool_checks SET expires_at=clock_timestamp()-interval '1 second' WHERE pool_id=$1 AND service='zlm'", f.Pool.ID); err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	f.Service.MonitorIdlePools(wait)
	media.mu.Lock()
	defer media.mu.Unlock()
	if media.AddCalls != 0 {
		t.Fatal("recording none spawned automatic pool trial", media.AddCalls)
	}
}

func TestRecordingRecoveryIdentifiesFirstFrameFailureWithoutSecrets(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	var oldSession id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&oldSession); err != nil {
		t.Fatal(err)
	}
	media.mu.Lock()
	delete(media.urls, string(oldSession))
	delete(media.recording, string(oldSession))
	media.mu.Unlock()
	f.Service.Probe = controlledProbe{media: media, file: f.Service.Probe, BeforeFrame: func() {
		media.mu.Lock()
		for k := range media.urls {
			delete(media.urls, k)
		}
		media.mu.Unlock()
	}}
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT reason_code FROM source_observations WHERE channel_id=$1 AND kind='main' ORDER BY observed_at DESC LIMIT 1", f.Channel).Scan(&reason); err != nil || reason != "source_recovery_first_frame_unavailable" {
		t.Fatal("recovery failure stage missing", reason, err)
	}
}

func TestRecordingOffWithoutActiveMainCompletes(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE stream_sessions SET state='closed',closed_at=clock_timestamp() WHERE channel_id=$1 AND purpose='main'", f.Channel); err != nil {
		t.Fatal(err)
	}
	media.mu.Lock()
	clear(media.urls)
	clear(media.recording)
	media.mu.Unlock()
	change, err := f.Service.Sources.SetPolicy(ctx, f.Admin, f.Channel, 3, "none", "off-absent-main")
	if err != nil {
		t.Fatal(err)
	}
	executeChange(t, f, "source.policy_apply")
	var state, mode string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT s.state,p.mode FROM source_switches s JOIN recording_policies p ON p.channel_id=s.channel_id WHERE s.job_id=$1", change.JobID).Scan(&state, &mode); err != nil || state != "succeeded" || mode != "none" {
		t.Fatal("absent main left policy guard active", state, mode, err)
	}
	var oldRun string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&oldRun); err != nil || oldRun != "stopped" {
		t.Fatal("absent main retained unfinished recorder receipt", oldRun, err)
	}
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal("completed policy still blocks runtime", err)
	}
}

func TestRecordingRecoveryCompletesInterruptedStop(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx, cancel := context.WithCancel(context.Background())
	e, err := channel.NewRuntimeExecution(ctx, f.DB, f.Channel)
	if err != nil {
		t.Fatal(err)
	}
	var h recording.Handle
	h.RunID = f.Run
	h.PoolID = f.Pool.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id,work_relative_path FROM recording_runs WHERE id=$1", f.Run).Scan(&h.SessionID, &h.RelativePath); err != nil {
		t.Fatal(err)
	}
	h.Key = zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(h.SessionID)}
	media.AfterStop = cancel
	if err := f.Service.Stop(ctx, e, h); err == nil {
		t.Fatal("interrupted stop unexpectedly committed")
	}
	e.Close()
	media.AfterStop = nil
	ctx = context.Background()
	var before string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&before); err != nil || before != "stopping" {
		t.Fatal("fault missed stop boundary", before, err)
	}
	seedCurrentBitrate(t, f, 1<<20)
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	var old string
	var next int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&old); err != nil || old != "stopped" {
		t.Fatal("old stop intent not finished", old, err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_runs WHERE channel_id=$1 AND id<>$2 AND state='recording'", f.Channel, f.Run).Scan(&next); err != nil || next != 1 {
		t.Fatal("no unique replacement run", next, err)
	}
}

func TestRecordingMonitorQueuesAllConfiguredChannels(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	rows, err := f.DB.Pool.Query(ctx, "SELECT id,version FROM channels WHERE id<>$1 ORDER BY channel_no", f.Channel)
	if err != nil {
		t.Fatal(err)
	}
	type slot struct {
		ch      id.ID
		version int64
	}
	var all []slot
	for rows.Next() {
		var v slot
		if err := rows.Scan(&v.ch, &v.version); err != nil {
			t.Fatal(err)
		}
		all = append(all, v)
	}
	rows.Close()
	for _, v := range all {
		draft, err := f.Service.Sources.CreateDraft(ctx, f.Admin, v.ch, v.version, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
		if err != nil {
			t.Fatal(err)
		}
		ss, err := id.New()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.DB.Pool.Exec(ctx, "UPDATE channels SET current_revision_id=$2,source_generation=1 WHERE id=$1", v.ch, draft.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state,proxy_key) VALUES($1,$2,$3,1,'one_nvr',$1::uuid::text,'main','active','__defaultVhost__/one_nvr/'||$1::uuid::text)`, ss, v.ch, draft.ID); err != nil {
			t.Fatal(err)
		}
		media.urls[string(ss)] = "rtsp://192.168.33.20/main"
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM recording_bitrate_samples"); err != nil {
		t.Fatal(err)
	}
	media.InspectDelay = 40 * time.Millisecond
	monitor, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- f.Service.Monitor(monitor) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(23 * time.Second)
	for time.Now().Before(deadline) {
		var paired int
		if err := f.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT channel_id FROM recording_bitrate_samples WHERE valid AND observed_at>clock_timestamp()-interval '30 seconds' GROUP BY channel_id HAVING max(observed_at)-min(observed_at)>=interval '10 seconds') q`).Scan(&paired); err != nil {
			t.Fatal(err)
		}
		if paired == 16 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("configured channels skipped instead of queued for fresh paired evidence")
}

func TestRecordingMonitorSamplingJitterKeepsFreshPair(t *testing.T) {
	f, media, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none' WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	var ss id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT stream_session_id FROM recording_runs WHERE id=$1", f.Run).Scan(&ss); err != nil {
		t.Fatal(err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM recording_bitrate_samples WHERE channel_id=$1", f.Channel); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sample, _ := id.New()
	if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO recording_bitrate_samples(id,channel_id,source_revision_id,stream_session_id,bytes_per_second,frames,valid,observed_at) VALUES($1,$2,$3,$4,1048576,100,true,$5)`, sample, f.Channel, f.Revision, ss, now.Add(-30100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	for i, age := range []time.Duration{20200 * time.Millisecond, 10100 * time.Millisecond, 200 * time.Millisecond} {
		media.ObservedAt = now.Add(-age)
		media.FrozenFrames = int64(200 + i*100)
		if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
			t.Fatal(err)
		}
	}
	var paired bool
	if err := f.DB.Pool.QueryRow(ctx, `SELECT coalesce(max(observed_at)-min(observed_at)>=interval '10 seconds',false) FROM recording_bitrate_samples WHERE channel_id=$1 AND valid AND observed_at>clock_timestamp()-interval '30 seconds'`, f.Channel).Scan(&paired); err != nil || !paired {
		t.Fatal("polling jitter discarded advancing frame evidence", paired, err)
	}
}

// Declining admission-time write proof is what asks for the escalation: a
// recorder that produces nothing for the grace period is stopped and reported.
// The same stall must NOT stop recording when write proof is on, because that
// contract reports the gap and keeps the recorder: a database outage spools
// completions, and stopping there would destroy healthy recording.
func TestRecordingOutputWatchdogOnlyAppliesWhenWriteProofIsOff(t *testing.T) {
	f, _, _, _ := testedSource(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_runs SET started_at=clock_timestamp()-interval '5 minutes',last_completion_at=NULL WHERE id=$1", f.Run); err != nil {
		t.Fatal(err)
	}

	// Default site (write proof required): status reports the stale gap and the
	// recorder keeps running.
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	status, err := f.Service.Sources.GetStatus(ctx, f.Admin, f.Channel)
	if err != nil || status.Recording.Reason != "completion_stale" {
		t.Fatalf("write-proof site escalated a stale completion: %+v %v", status.Recording, err)
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&state); err != nil || state != "recording" {
		t.Fatalf("write-proof site stopped a running recorder: %s %v", state, err)
	}

	// Opting out asks for the escalation, so the same stall now stops it.
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE sites SET require_storage_write_proof=false WHERE singleton"); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.Reconcile(ctx, f.Channel); err != nil {
		t.Fatal(err)
	}
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_runs WHERE id=$1", f.Run).Scan(&state); err != nil || state != "stopped" {
		t.Fatalf("opted-out site kept a silent recorder: %s %v", state, err)
	}
	status, err = f.Service.Sources.GetStatus(ctx, f.Admin, f.Channel)
	if err != nil || status.Recording.State != "unavailable" || status.Recording.Reason != "recording_output_stalled" {
		t.Fatalf("missing stall reason after opting out: %+v %v", status.Recording, err)
	}
}
