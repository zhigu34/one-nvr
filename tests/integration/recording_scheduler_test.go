package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/recording"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/storage"
)

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
