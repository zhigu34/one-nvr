package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/storage"
)

func TestHookDurabilityAndWrongIdentity(t *testing.T) {
	dir := t.TempDir()
	db, _, sites, _ := authFixture(t, dir)
	svc := recording.New(db, nil, nil, []string{t.TempDir()}, dir)
	handler := recording.NewHookHandler(svc, "isolated-hook-key")
	c := recording.Completion{Key: zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: "bb6f7f61-e7dd-4eee-a03c-a53167d6d688"}, MediaServerID: "one-nvr-" + string(sites.Secrets.SiteID), FilePath: "/storage/pool/.work/zlm/unknown/closed.mp4", Size: 1024, StartTime: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), Duration: time.Second}
	request := func(body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://worker:8083/on_record_mp4?token="+token, strings.NewReader(body))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"vhost":"__defaultVhost__","app":"one_nvr","stream":"` + c.Key.Stream + `","mediaServerId":"` + c.MediaServerID + `","file_path":"` + c.FilePath + `","file_size":1024,"start_time":1791158400,"time_len":1,"url":"http://ignored/upstream","folder":"/wrong-folder","file_name":"ignored.mp4"}`
	if w := request(body, "wrong"); w.Code != 403 {
		t.Fatal("wrong token accepted", w.Code)
	}
	if w := request(strings.Repeat("x", 65537), "isolated-hook-key"); w.Code != 413 {
		t.Fatal("oversized callback accepted", w.Code)
	}
	wrong := c
	wrong.MediaServerID = "another-media-server"
	if err := svc.Accept(context.Background(), wrong); err == nil {
		t.Fatal("wrong media server accepted")
	}
	if w := request(body, "isolated-hook-key"); w.Code != 200 {
		t.Fatal("valid unknown-stream diagnostic not persisted", w.Code, w.Body.String())
	}
	if err := svc.Accept(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	var count int
	var state, payload string
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM hook_inbox").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate inbox", count, err)
	}
	if err := db.Pool.QueryRow(context.Background(), "SELECT state,payload::text FROM hook_inbox").Scan(&state, &payload); err != nil {
		t.Fatal(err)
	}
	if state != "diagnostic" || strings.Contains(payload, "ignored/upstream") || strings.Contains(payload, "wrong-folder") {
		t.Fatal("unknown stream misassigned or callback URL/folder persisted")
	}
	closed, err := pgxpool.NewWithConfig(context.Background(), db.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	svc.DB = &database.DB{Pool: closed}
	if err := svc.Accept(context.Background(), c); err != nil {
		t.Fatal("database outage did not spool", err)
	}
	files, err := os.ReadDir(filepath.Join(dir, "recording-spool"))
	if err != nil {
		t.Fatal(err)
	}
	durable := false
	for _, f := range files {
		durable = durable || filepath.Ext(f.Name()) == ".json"
	}
	if !durable {
		t.Fatal("success without durable spool")
	}
	svc.DB = db
	if err := svc.DrainSpool(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, _ = os.ReadDir(filepath.Join(dir, "recording-spool"))
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".json" {
			t.Fatal("successful replay left spool payload")
		}
	}
	svc.DB = &database.DB{Pool: closed}
	svc.DataDir = filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(svc.DataDir, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Accept(context.Background(), c); err == nil {
		t.Fatal("database and spool failure returned success")
	}
	var persisted recording.Completion
	if err := json.Unmarshal([]byte(payload), &persisted); err != nil || persisted.FilePath != c.FilePath {
		t.Fatal("callback media path lost", err)
	}
}

func TestHookBindsLateTailToImmutableOldRun(t *testing.T) {
	dir := t.TempDir()
	db, accounts, sites, admin := authFixture(t, dir)
	ctx := context.Background()
	rootDir := t.TempDir()
	pools := storage.New(db, accounts, []string{rootDir})
	pool, err := pools.Register(ctx, admin.Principal, storage.RegisterInput{Name: "Hook pool", Path: rootDir})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(db, accounts, sites.Secrets, policy)
	var c id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&c); err != nil {
		t.Fatal(err)
	}
	rev, err := sources.CreateDraft(ctx, admin.Principal, c, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := id.New()
	run, _ := id.New()
	relative := ".work/zlm/" + string(run)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state) VALUES($1,$2,$3,1,'one_nvr',$1::uuid::text,'main','closed')`, session, c, rev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state) VALUES($1,$2,$3,$4,$5,$6,$7,'continuous','stopped')`, run, sites.Secrets.SiteID, c, rev.ID, session, pool.ID, relative); err != nil {
		t.Fatal(err)
	}
	completion := recording.Completion{Key: zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(session)}, MediaServerID: "one-nvr-" + string(sites.Secrets.SiteID), FilePath: filepath.Join(pool.Path, relative, "date", "tail.mp4"), Size: 4096, StartTime: time.Now().UTC(), Duration: 2 * time.Second}
	svc := recording.New(db, nil, nil, []string{rootDir}, dir)
	if err := svc.Accept(ctx, completion); err != nil {
		t.Fatal(err)
	}
	var bound id.ID
	var state string
	if err := db.Pool.QueryRow(ctx, "SELECT run_id,state FROM hook_inbox WHERE run_id=$1", run).Scan(&bound, &state); err != nil || bound != run || state != "pending" {
		t.Fatal("late tail lost old-source/run provenance", err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE stream_sessions SET stream='another-physical-key' WHERE id=$1", session); err == nil {
		t.Fatal("run's physical session identity can be changed")
	}
}

func TestPrivatePlaybackHookRequiresMappedActiveSession(t *testing.T) {
	dir := t.TempDir()
	db, accounts, sites, admin := authFixture(t, dir)
	ctx := context.Background()
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(db, accounts, sites.Secrets, policy)
	var ch id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&ch); err != nil {
		t.Fatal(err)
	}
	rev, err := sources.CreateDraft(ctx, admin.Principal, ch, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := id.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state) VALUES($1,$2,$3,1,'one_nvr',$1::uuid::text,'test','active')`, session, ch, rev.ID); err != nil {
		t.Fatal(err)
	}
	handler := recording.NewHookHandler(recording.New(db, nil, nil, nil, dir), "hook-key", "probe-key")
	request := func(path, stream, params string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"vhost": "__defaultVhost__", "app": "one_nvr", "stream": stream, "mediaServerId": "one-nvr-" + string(sites.Secrets.SiteID), "params": params})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", "http://worker:8083/"+path+"?token=hook-key", strings.NewReader(string(body))))
		return w
	}
	for _, params := range []string{"", "probe_token=wrong", "probe_token=probe-key&probe_token=probe-key"} {
		w := request("on_play", string(session), params)
		if strings.Contains(w.Body.String(), `"code":0`) {
			t.Fatal("anonymous or invalid probe accepted")
		}
	}
	if w := request("on_play", string(session), "probe_token=probe-key"); w.Code != 200 || !strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatal("mapped private probe rejected", w.Code, w.Body.String())
	}
	if w := request("on_play", "bb6f7f61-e7dd-4eee-a03c-a53167d6d688", "probe_token=probe-key"); strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatal("unmapped private stream accepted")
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE stream_sessions SET state='closed' WHERE id=$1", session); err != nil {
		t.Fatal(err)
	}
	if w := request("on_play", string(session), "probe_token=probe-key"); strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatal("closed session accepted")
	}
	if w := request("on_http_access", string(session), "probe_token=probe-key"); strings.Contains(w.Body.String(), `"err":""`) {
		t.Fatal("native media files readable")
	}
	if w := request("on_stream_none_reader", string(session), ""); !strings.Contains(w.Body.String(), `"close":false`) {
		t.Fatal("no-viewer stream closed")
	}
}

func TestPoolProbeRequiresZLMFile(t *testing.T) {
	dir := t.TempDir()
	db, accounts, _, admin := authFixture(t, dir)
	ctx := context.Background()
	poolDir := t.TempDir()
	pools := storage.New(db, accounts, []string{poolDir})
	pool, err := pools.Register(ctx, admin.Principal, storage.RegisterInput{Name: "Media proof", Path: poolDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker"} {
		if err := pools.SamplePool(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
	}
	run, _ := id.New()
	// An application-created file plus claimed media evidence is not a ZLM proof.
	fake := filepath.Join(pool.Path, "fake.mp4")
	if err := os.WriteFile(fake, []byte("application wrote this"), 0600); err != nil {
		t.Fatal(err)
	}
	evidence := zlm.WriteEvidence{PoolID: pool.ID, RecordingID: run, FilePath: fake, Size: 21, Duration: time.Second, VideoVerified: true, ObservedAt: time.Now().UTC()}
	if err := pools.PublishZLMEvidence(ctx, pool.ID, run, evidence); err == nil {
		t.Fatal("application file accepted as ZLM writeability")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM storage_pool_checks WHERE pool_id=$1 AND service='zlm' AND state='healthy'", pool.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("false ZLM healthy", err)
	}
	root, current, err := pools.OpenMediaRoot(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if current.Path != pool.Path {
		t.Fatal("pool identity changed")
	}
	if err := os.Remove(filepath.Join(pool.Path, ".one-nvr.json")); err != nil {
		t.Fatal(err)
	}
	if root, _, err := pools.OpenMediaRoot(ctx, pool.ID); err == nil {
		root.Close()
		t.Fatal("missing pool identity accepted")
	}
	if _, err := os.Stat(filepath.Join(pool.Path, ".one-nvr.json")); !os.IsNotExist(err) {
		t.Fatal("missing marker recreated")
	}
}

func TestWorkerConnectionUsesFreshAddressBoundary(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	allowed, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(db, accounts, sites.Secrets, allowed)
	var ch id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&ch); err != nil {
		t.Fatal(err)
	}
	username, password := "user@sample", "pass:sample"
	rev, err := sources.CreateDraft(ctx, admin.Principal, ch, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{Username: &username, Password: &password, PasswordAction: "replace"}})
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := channel.ParseNetworkPolicy("192.168.33.0/24", []netip.Addr{netip.MustParseAddr("192.168.33.20")})
	if input, err := sources.PrivateConnection(ctx, ch, rev.ID, "main", fresh); err == nil || input.URL != "" {
		t.Fatal("new internal peer escaped fresh connection boundary")
	}
	input, err := sources.PrivateConnection(ctx, ch, rev.ID, "main", allowed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.URL, "user%40sample:pass%3Asample@") || strings.Contains(fmt.Sprint(input), password) {
		t.Fatal("private credentials not encoded/redacted")
	}
	wrong, _ := id.New()
	if input, err := sources.PrivateConnection(ctx, wrong, rev.ID, "main", allowed); err == nil || input.URL != "" {
		t.Fatal("cross-channel worker credentials returned")
	}
}

func TestPoolCheckWithoutSourceRemainsPending(t *testing.T) {
	dir := t.TempDir()
	db, accounts, _, admin := authFixture(t, dir)
	ctx := context.Background()
	base := t.TempDir()
	pools := storage.New(db, accounts, []string{base})
	pool, err := pools.Register(ctx, admin.Principal, storage.RegisterInput{Name: "No source", Path: base})
	if err != nil {
		t.Fatal(err)
	}
	recordings := recording.New(db, nil, nil, []string{base}, dir)
	if err := recordings.CheckPool(ctx, pool.ID); !errors.Is(err, zlm.ErrTestSourceRequired) {
		t.Fatal("empty source did not remain pending", err)
	}
	pools.MediaCheck = recordings.CheckPool
	jobID, err := pools.RequestCheck(ctx, admin.Principal, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := (jobs.Repository{DB: db}).Claim(ctx, "storage.check")
	if err != nil || lease.ID != jobID {
		t.Fatal("check job unavailable", err)
	}
	if _, err := pools.HandleCheck(ctx, lease); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := db.Pool.QueryRow(ctx, "SELECT state,reason_code FROM storage_pool_checks WHERE pool_id=$1 AND service='zlm'", pool.ID).Scan(&state, &reason); err != nil || state != "pending" || reason != "test_source_required" {
		t.Fatal("source-less check reported false readiness", state, reason, err)
	}
}

func TestPoolMediaStructureDoesNotRequireLiveDecodedFrame(t *testing.T) {
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ffprobe := mediaTestTool(t, "ffprobe")
	dir := t.TempDir()
	db, accounts, sites, admin := authFixture(t, dir)
	ctx := context.Background()
	base := t.TempDir()
	pools := storage.New(db, accounts, []string{base})
	pool, err := pools.Register(ctx, admin.Principal, storage.RegisterInput{Name: "Structural proof", Path: base})
	if err != nil {
		t.Fatal(err)
	}
	network, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(db, accounts, sites.Secrets, network)
	var ch id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&ch); err != nil {
		t.Fatal(err)
	}
	rev, err := sources.CreateDraft(ctx, admin.Principal, ch, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := id.New()
	run, _ := id.New()
	work := ".work/probes/zlm/" + string(run)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state) VALUES($1,$2,$3,1,'one_nvr',$1::uuid::text,'test','active')`, session, ch, rev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state) VALUES($1,$2,$3,$4,$5,$6,$7,'probe','stopped')`, run, sites.Secrets.SiteID, ch, rev.ID, session, pool.ID, work); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pool.Path, work, "closed.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=5", "-t", "2", "-c:v", "libx264", "-threads", "1", path).Run(); err != nil {
		t.Fatal("synthetic MP4 creation failed", err)
	}
	checker := probe.Runner{FFprobe: ffprobe}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := checker.InspectMP4(ctx, f)
	f.Close()
	if err != nil || !evidence.Readable || evidence.Video.FirstFrame {
		t.Fatal("structure probe claims decoded frame", err)
	}
	recordings := recording.New(db, nil, checker, []string{base}, dir)
	completion := recording.Completion{Key: zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(session)}, MediaServerID: "one-nvr-" + string(sites.Secrets.SiteID), FilePath: path, Size: evidence.Size, StartTime: time.Now().UTC(), Duration: evidence.Duration}
	if err := recordings.Accept(ctx, completion); err != nil {
		t.Fatal(err)
	}
	pools.MediaInspector = checker
	if err := pools.PublishZLMEvidence(ctx, pool.ID, run, zlm.WriteEvidence{PoolID: pool.ID, RecordingID: run, FilePath: path, Size: evidence.Size, Duration: evidence.Duration, VideoVerified: true, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal("valid structure incorrectly requires decoded first frame", err)
	}
}
