package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

type interruptedPublication struct{}

func (interruptedPublication) Move(ctx context.Context, root *os.Root, original, target string, expected os.FileInfo) error {
	if err := (recording.NativePublisher{}).Move(ctx, root, original, target, expected); err != nil {
		return err
	}
	return fmt.Errorf("isolated post-move interruption")
}
func publishAcceptance() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
		return fmt.Errorf("publish fixture identities unavailable")
	}
	apiKey, _ := secret.ComponentCredential("zlm")
	hookKey, _ := secret.ComponentCredential("recording-hook")
	probeKey, _ := secret.ComponentCredential("media-probe")
	client, err := zlm.New("http://zlm", apiKey, nil)
	if err != nil {
		return err
	}
	checker := probe.Runner{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"}
	recordings := recording.New(db, client, checker, []string{"/storage"}, fixtureData)
	accounts := auth.NewService(db, secret, auth.NewPasswordHasher(2))
	recordings.Auth = accounts
	login, err := accounts.Login(ctx, "admin", "isolated-media-probe-password")
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", ":8083")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: recording.NewHookHandler(recordings, hookKey, probeKey), ReadHeaderTimeout: 2 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	for attempt := 0; client.Health(ctx) != nil; attempt++ {
		if attempt > 20 {
			return fmt.Errorf("production media startup unavailable")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(ids.Session)}
	upstream, err := channel.BuildRTSPURL(ids.Config, "", "", ids.Config.MainPath)
	if err != nil {
		return err
	}
	ref, err := client.AddProxy(ctx, zlm.ProxyInput{Key: key, URL: upstream, Transport: "tcp"})
	if err != nil {
		return err
	}
	defer client.RemoveProxy(context.Background(), ref)
	if _, err := db.Pool.Exec(ctx, "UPDATE stream_sessions SET state='active',proxy_key=$2 WHERE id=$1", ids.Session, ref.OpaqueKey); err != nil {
		return err
	}
	privateURL, _ := probe.InternalURL(key, probeKey)
	if _, err := checker.FirstFrame(ctx, privateURL); err != nil {
		return fmt.Errorf("production first-frame probe failed")
	}
	runID, err := id.New()
	if err != nil {
		return err
	}
	work := ".work/zlm/" + string(runID)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose) VALUES($1,$2,$3,$4,$5,$6,$7,'continuous')`, runID, secret.SiteID, ids.Channel, ids.Revision, ids.Session, ids.Pool, work); err != nil {
		return err
	}
	root, pool, err := recordings.Pools.OpenMediaRoot(ctx, ids.Pool)
	if err != nil {
		return err
	}
	if err := root.MkdirAll(work, 0700); err != nil {
		root.Close()
		return err
	}
	root.Close()
	if err := recordings.DescribeRun(ctx, runID); err != nil {
		return err
	}
	if err := client.StartRecord(ctx, key, filepath.Join(pool.Path, work), 60); err != nil {
		return err
	}
	defer client.StopRecord(context.Background(), key)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
	}
	if err := client.StopRecord(ctx, key); err != nil {
		return err
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE recording_runs SET state='stopped',ended_at=clock_timestamp() WHERE id=$1", runID); err != nil {
		return err
	}
	var inboxID id.ID
	for attempt := 0; attempt < 100; attempt++ {
		err = db.Pool.QueryRow(ctx, "SELECT id,payload FROM hook_inbox WHERE run_id=$1", runID).Scan(&inboxID, &raw)
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
		return fmt.Errorf("actual media completion not durably received")
	}
	var completion recording.Completion
	if json.Unmarshal(raw, &completion) != nil {
		return fmt.Errorf("typed media completion unavailable")
	}
	before, err := os.Stat(completion.FilePath)
	if err != nil {
		return err
	}
	recordings.Files = interruptedPublication{}
	if _, err := recordings.Publish(ctx, inboxID); err == nil {
		return fmt.Errorf("interrupted publication reported ready")
	}
	var state, target string
	if err := db.Pool.QueryRow(ctx, "SELECT state,target_relative_path FROM recording_segments WHERE run_id=$1", runID).Scan(&state, &target); err != nil || state != "finalizing" {
		return fmt.Errorf("post-move durable intent missing")
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE hook_inbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1", inboxID); err != nil {
		return err
	}
	recordings.Files = nil
	if err := recordings.Recover(ctx); err != nil {
		return err
	}
	segment, err := recordings.Publish(ctx, inboxID)
	if err != nil || segment.State != "ready" {
		return fmt.Errorf("actual media publication did not recover")
	}
	after, err := os.Stat(filepath.Join(pool.Path, target))
	if err != nil || !os.SameFile(before, after) {
		return fmt.Errorf("publication replaced/copied media inode")
	}
	if _, err := os.Stat(completion.FilePath); !os.IsNotExist(err) {
		return fmt.Errorf("native original still present after publication")
	}
	original, _ := filepath.Rel(pool.Path, completion.FilePath)
	stable, _ := recording.StableID(secret.SiteID, pool.ID, runID, original)
	frozen, _ := recording.FreezePath(1, completion.StartTime, "Asia/Shanghai", stable)
	if frozen.RelativePath != target || stable != segment.ID {
		return fmt.Errorf("actual canonical local naming differs from frozen rule")
	}
	if err := recordings.Accept(ctx, completion); err != nil {
		return err
	}
	replay, err := recordings.Publish(ctx, inboxID)
	if err != nil || replay.ID != segment.ID {
		return fmt.Errorf("duplicate moved-source callback not idempotent")
	}
	page, err := recordings.List(ctx, login.Principal, recording.Query{ChannelID: ids.Channel, Start: completion.StartTime.Add(time.Second), End: completion.StartTime.Add(10 * time.Second), Limit: 10})
	if err != nil || len(page.Items) != 1 {
		return fmt.Errorf("actual historical overlap query failed")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_locations").Scan(&count); err != nil || count != 1 {
		return fmt.Errorf("actual publication duplicated location")
	}
	evidence, _ := json.MarshalIndent(map[string]any{"actual_zlm_media_published": true, "same_inode": true, "interrupted_move_recovered": true, "duplicate_callback_idempotent": true, "canonical_local_filename": filepath.Base(target), "historical_partial_overlap": true, "ready_locations": count}, "", "  ")
	if err := os.WriteFile("/evidence/publish.json", evidence, 0644); err != nil {
		return err
	}
	fmt.Println("Actual ZLM recording, durable finalizing/ready, atomic same-inode publication, interrupted recovery and historical query PASS")
	return nil
}
