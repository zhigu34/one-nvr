package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/storage"
)

func switchAcceptance() error   { return sourceLifecycleAcceptance("switch") }
func recoveryAcceptance() error { return sourceLifecycleAcceptance("recovery") }
func sourceLifecycleAcceptance(mode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	db, err := database.Open(ctx, fixtureDSN)
	if err != nil {
		return err
	}
	defer db.Pool.Close()
	secret, err := secrets.Load(fixtureData)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile("/evidence/probe-ids.json")
	if err != nil {
		return err
	}
	var ids poolFixtureIDs
	if json.Unmarshal(raw, &ids) != nil {
		return fmt.Errorf("switch fixture unavailable")
	}
	accounts := auth.NewService(db, secret, auth.NewPasswordHasher(2))
	login, err := accounts.Login(ctx, "admin", "isolated-media-probe-password")
	if err != nil {
		return err
	}
	policy, err := channel.ParseNetworkPolicy(ids.Config.IP+"/32", nil)
	if err != nil {
		return err
	}
	sources := channel.NewSources(db, accounts, secret, policy)
	apiKey, _ := secret.ComponentCredential("zlm")
	hookKey, _ := secret.ComponentCredential("recording-hook")
	probeKey, _ := secret.ComponentCredential("media-probe")
	client, err := zlm.New("http://zlm", apiKey, nil)
	if err != nil {
		return err
	}
	recordings := recording.New(db, client, probe.Runner{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"}, []string{"/storage"}, fixtureData)
	recordings.Auth = accounts
	recordings.Sources = sources
	recordings.ProbeToken = probeKey
	recordings.FreshNetwork = func(context.Context) (channel.NetworkPolicy, error) { return policy, nil }
	listener, err := net.Listen("tcp", ":8083")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: recording.NewHookHandler(recordings, hookKey, probeKey), ReadHeaderTimeout: 2 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	for attempt := 0; client.Health(ctx) != nil; attempt++ {
		if attempt > 20 {
			return fmt.Errorf("switch media startup unavailable")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	pools := recordings.Pools
	pools.Auth = accounts
	go pools.Monitor(ctx, "api")
	go pools.Monitor(ctx, "worker")
	defaultPool := true
	if _, err := pools.Update(ctx, login.Principal, ids.Pool, 1, storage.UpdateInput{IsDefault: &defaultPool}); err != nil {
		return err
	}
	execute := func(kind string, handler jobs.Handler) error {
		repo := jobs.Repository{DB: db}
		lease, err := repo.Claim(ctx, kind)
		if err != nil {
			return err
		}
		if err := jobs.Execute(ctx, repo, lease, handler); err != nil {
			return err
		}
		var state string
		if err := db.Pool.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", lease.ID).Scan(&state); err != nil {
			return err
		}
		if state != "succeeded" && state != "failed" {
			return fmt.Errorf("source handler did not finish its durable receipt")
		}
		return nil
	}
	testSource := func(revision id.ID, key string) (channel.Change, error) {
		change, err := sources.RequestTest(ctx, login.Principal, ids.Channel, revision, key)
		if err != nil {
			return change, err
		}
		if err := execute("source.test", recordings.ExecuteSourceTest); err != nil {
			return change, err
		}
		if change.TestID == nil {
			return change, fmt.Errorf("source test identity missing")
		}
		result, err := sources.TestResult(ctx, login.Principal, ids.Channel, *change.TestID)
		if err != nil || result.State != "succeeded" || !result.Main.FirstFrame {
			return change, fmt.Errorf("production source-test first frame failed")
		}
		return change, nil
	}
	first, err := testSource(ids.Revision, "switch-first-test")
	if err != nil {
		return err
	}
	if err := pools.Sample(ctx, "api"); err != nil {
		return err
	}
	if err := recordings.CheckPool(ctx, ids.Pool); err != nil {
		return fmt.Errorf("first-source actual pool proof: %w", err)
	}
	continuous := "continuous"
	if _, err := sources.RequestApply(ctx, login.Principal, ids.Channel, channel.SourceApplyInput{RevisionID: ids.Revision, TestID: *first.TestID, ExpectedVersion: 2, FirstRecordingMode: &continuous}, "switch-first-apply"); err != nil {
		return err
	}
	if err := execute("source.apply", recordings.ExecuteSourceChange); err != nil {
		return err
	}
	var oldRun, oldSession id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id,stream_session_id FROM recording_runs WHERE channel_id=$1 AND state='recording'", ids.Channel).Scan(&oldRun, &oldSession); err != nil {
		return fmt.Errorf("initial recorder not verified")
	}
	if mode == "import" {
		return recordingImportScenario(ctx, db, recordings, login.Principal, ids, oldRun, execute)
	}
	if mode == "recovery" {
		return recordingRecoveryScenario(ctx, db, recordings, client, login.Principal, ids, oldRun, oldSession, execute)
	}
	config := ids.Config
	config.MainPath = "/one_nvr/" + streams[2]
	config.SubPath = "/missing-sub-fixture"
	second, err := sources.CreateDraft(ctx, login.Principal, ids.Channel, 4, channel.DraftInput{Config: config, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		return err
	}
	tested, err := testSource(second.ID, "switch-second-test")
	if err != nil {
		return err
	}
	oldSnapshot, err := client.Inspect(ctx, zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(oldSession)})
	if err != nil || !oldSnapshot.Recording {
		return fmt.Errorf("draft test interrupted actual old recorder")
	}
	result, err := sources.TestResult(ctx, login.Principal, ids.Channel, *tested.TestID)
	if err != nil || result.Sub.State != "unavailable" {
		return fmt.Errorf("actual sub degradation hidden")
	}
	change, err := sources.RequestApply(ctx, login.Principal, ids.Channel, channel.SourceApplyInput{RevisionID: second.ID, TestID: *tested.TestID, ExpectedVersion: 5}, "switch-second-apply")
	if err != nil {
		return err
	}
	if err := execute("source.apply", recordings.ExecuteSourceChange); err != nil {
		return err
	}
	var current id.ID
	var phase string
	if err := db.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", ids.Channel).Scan(&current); err != nil || current != second.ID {
		return fmt.Errorf("verified new source not current")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT phase FROM source_switches WHERE job_id=$1", change.JobID).Scan(&phase); err != nil || phase != "committed" {
		return fmt.Errorf("actual switch not committed")
	}
	var inbox id.ID
	for attempt := 0; attempt < 100; attempt++ {
		err = db.Pool.QueryRow(ctx, "SELECT id FROM hook_inbox WHERE run_id=$1 AND state='pending' ORDER BY received_at LIMIT 1", oldRun).Scan(&inbox)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err != nil {
		return fmt.Errorf("old source final tail not received")
	}
	segment, err := recordings.Publish(ctx, inbox)
	if err != nil || segment.RunID != oldRun || segment.SourceRevisionID != ids.Revision {
		return fmt.Errorf("actual late tail lost original provenance")
	}
	// Test a working source, then stop its synthetic publisher before apply.
	rollback, err := sources.CreateDraft(ctx, login.Principal, ids.Channel, 7, channel.DraftInput{Config: ids.Config, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		return err
	}
	rollbackTest, err := testSource(rollback.ID, "switch-rollback-test")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://fixture:8557/stop-one", nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("synthetic stop control unavailable")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("synthetic stop failed")
	}
	camera, err := zlm.New("http://camera", syntheticSecret, nil)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 50; attempt++ {
		_, err = camera.Inspect(ctx, zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: streams[0]})
		if errors.Is(err, zlm.ErrStreamAbsent) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !errors.Is(err, zlm.ErrStreamAbsent) {
		return fmt.Errorf("failed upstream not actually absent")
	}
	rolled, err := sources.RequestApply(ctx, login.Principal, ids.Channel, channel.SourceApplyInput{RevisionID: rollback.ID, TestID: *rollbackTest.TestID, ExpectedVersion: 8}, "switch-rollback-apply")
	if err != nil {
		return err
	}
	if err := execute("source.apply", recordings.ExecuteSourceChange); err != nil {
		return err
	}
	var rollbackCode string
	if err := db.Pool.QueryRow(ctx, "SELECT phase,coalesce(error_code,'') FROM source_switches WHERE job_id=$1", rolled.JobID).Scan(&phase, &rollbackCode); err != nil || phase != "rolled_back" {
		return fmt.Errorf("actual failed new source did not rollback: phase=%s code=%s", phase, rollbackCode)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", ids.Channel).Scan(&current); err != nil || current != second.ID {
		return fmt.Errorf("rollback current revision incorrect")
	}
	var restoredRun, restoredSession id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id,stream_session_id FROM recording_runs WHERE channel_id=$1 AND state='recording'", ids.Channel).Scan(&restoredRun, &restoredSession); err != nil {
		return fmt.Errorf("rollback recorder unavailable")
	}
	snapshot, err := client.Inspect(ctx, zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(restoredSession)})
	if err != nil || !snapshot.Recording || restoredRun == oldRun {
		return fmt.Errorf("rollback did not create a verified new run")
	}
	raw, _ = json.MarshalIndent(map[string]any{"production_source_test": true, "old_recording_not_interrupted_by_test": true, "sub_degradation_visible": true, "source_switch_committed": true, "late_tail_original_run": true, "actual_upstream_failure": true, "rollback_new_run_verified": true}, "", "  ")
	if err := os.WriteFile("/evidence/switch.json", raw, 0644); err != nil {
		return err
	}
	fmt.Println("Actual source test, continuous source switch, sub degradation, late tail provenance and failed-source rollback PASS")
	return nil
}
