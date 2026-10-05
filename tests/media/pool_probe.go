package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/site"
	"github.com/zhigu34/one-nvr/internal/storage"
)

const fixtureDSN = "postgres://one_nvr_media:isolated-media-database@postgres:5432/one_nvr_media?sslmode=disable"
const fixtureData = "/evidence/probe-data"

type poolFixtureIDs struct {
	Pool, Channel, Revision, Session id.ID
	Config                           channel.SourceConfig
}

func preparePoolProbe() error { return prepareMediaSession("test") }
func prepareMediaSession(purpose string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, fixtureDSN)
	if err != nil {
		return fmt.Errorf("isolated probe database unavailable")
	}
	defer db.Pool.Close()
	if err := database.Migrate(ctx, db); err != nil {
		return err
	}
	secret, err := secrets.Init(fixtureData)
	if err != nil {
		return err
	}
	hasher := auth.NewPasswordHasher(2)
	accounts := auth.NewService(db, secret, hasher)
	if err := accounts.ApplyEntryProtocol(ctx, "http"); err != nil {
		return err
	}
	sites := site.Service{DB: db, Secrets: secret, Passwords: hasher}
	if _, err := sites.Setup(ctx, site.SetupInput{Token: secret.SetupToken, AdminName: "admin", AdminPassword: "isolated-media-probe-password", Name: "Synthetic media only", Timezone: "Asia/Shanghai", ChannelCount: 16}); err != nil {
		return err
	}
	login, err := accounts.Login(ctx, "admin", "isolated-media-probe-password")
	if err != nil {
		return err
	}
	pools := storage.New(db, accounts, []string{"/storage"})
	pool, err := pools.Register(ctx, login.Principal, storage.RegisterInput{Name: "Synthetic pool", Path: "/storage/pool"})
	if err != nil {
		return err
	}
	var ids poolFixtureIDs
	ids.Pool = pool.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&ids.Channel); err != nil {
		return err
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", "camera")
	if err != nil || len(ips) != 1 {
		return fmt.Errorf("isolated camera unavailable")
	}
	policy, err := channel.ParseNetworkPolicy(ips[0].String()+"/32", nil)
	if err != nil {
		return err
	}
	sources := channel.NewSources(db, accounts, secret, policy)
	ids.Config = channel.SourceConfig{IP: ips[0].String(), RTSPPort: 554, MainPath: "/one_nvr/" + streams[0], Transport: "tcp"}
	revision, err := sources.CreateDraft(ctx, login.Principal, ids.Channel, 1, channel.DraftInput{Config: ids.Config, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		return err
	}
	ids.Revision = revision.ID
	ids.Session, _ = id.New()
	if purpose != "switch" {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose) VALUES($1,$2,$3,1,'one_nvr',$1::uuid::text,$4)`, ids.Session, ids.Channel, ids.Revision, purpose); err != nil {
			return err
		}
	}
	rendered, err := zlm.RenderConfig(config.Config{MediaHost: "127.0.0.1", RTCPort: 8000}, secret)
	if err != nil {
		return err
	}
	// The isolated runner is the Worker for this fixture; production uses worker.
	rendered = replaceWorkerHost(rendered)
	if err := os.WriteFile("/evidence/zlm-probe.ini", []byte(rendered), 0644); err != nil {
		return err
	}
	raw, _ := json.Marshal(ids)
	return os.WriteFile("/evidence/probe-ids.json", raw, 0600)
}
func poolProbeAcceptance() error {
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
		return fmt.Errorf("probe identities unavailable")
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
	listener, err := net.Listen("tcp", ":8083")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: recording.NewHookHandler(recordings, hookKey, probeKey), ReadHeaderTimeout: 2 * time.Second}
	go server.Serve(listener)
	defer server.Close()
	for attempt := 0; client.Health(ctx) != nil; attempt++ {
		if attempt > 20 {
			return fmt.Errorf("production media config did not start")
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
		return fmt.Errorf("production private play hook rejected mapped probe")
	}
	publicURL, _ := probe.InternalURL(key, "")
	if _, err := checker.FirstFrame(ctx, publicURL); err == nil {
		return fmt.Errorf("production private play hook accepted anonymous reader")
	}
	root, pool, err := recordings.Pools.OpenMediaRoot(ctx, ids.Pool)
	if err != nil {
		return err
	}
	root.Close()
	if err := recordings.Pools.SamplePool(ctx, pool, "api"); err != nil {
		return err
	}
	if err := recordings.ProbeSession(ctx, ids.Pool, ids.Session); err != nil {
		return fmt.Errorf("actual ZLM pool proof failed: %w", err)
	}
	var verified bool
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT state='healthy' AND reason_code='zlm_media_verified' AND expires_at>clock_timestamp() FROM storage_pool_checks WHERE pool_id=$1 AND service='zlm'", ids.Pool).Scan(&verified); err != nil || !verified {
		return fmt.Errorf("actual media proof did not produce fresh ZLM evidence")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_segments").Scan(&count); err != nil || count != 0 {
		return fmt.Errorf("probe contaminated formal recording index")
	}
	rows, err := db.Pool.Query(ctx, "SELECT payload->>'FilePath' FROM hook_inbox WHERE run_id IS NOT NULL")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return fmt.Errorf("verified probe media was not removed")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	evidence, _ := json.MarshalIndent(map[string]any{"actual_zlm_pool_write": true, "private_mapped_probe_allowed": true, "anonymous_probe_denied": true, "formal_segments": count, "probe_media_removed": true, "evidence_lifetime_seconds": 30}, "", "  ")
	if err := os.WriteFile("/evidence/probe.json", evidence, 0644); err != nil {
		return err
	}
	fmt.Println("Production hooks, durable run/completion, real ZLM pool write, MP4 proof and probe cleanup PASS")
	return nil
}

func replaceWorkerHost(text string) string {
	return strings.ReplaceAll(text, "http://worker:8083/", "http://runner:8083/")
}
