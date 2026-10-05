//go:build gateway_runtime

package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/tests/testcerts"
)

type jointChannel struct {
	Channel id.ID `json:"channel_id"`
	Run     id.ID `json:"run_id"`
	Session id.ID `json:"session_id"`
	Ready   int   `json:"ready_count"`
}
type jointSnapshot struct {
	Channels []jointChannel `json:"channels"`
	TLS      string         `json:"tls_id"`
}
type jointAPI struct {
	t          *testing.T
	client     *http.Client
	base, csrf string
}

func (a *jointAPI) call(method, path string, body any, version int64, out any) {
	a.t.Helper()
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, a.base+"/api/v1/"+path, bytes.NewReader(raw))
	if err != nil {
		a.t.Fatal("invalid private fixture request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", a.base)
	req.Header.Set("X-CSRF-Token", a.csrf)
	if method != "GET" {
		key, _ := id.New()
		req.Header.Set("Idempotency-Key", string(key))
	}
	if version > 0 {
		req.Header.Set("If-Match", fmt.Sprintf("\"%d\"", version))
	}
	res, err := a.client.Do(req)
	if err != nil {
		a.t.Fatal("actual gateway request unavailable")
	}
	defer res.Body.Close()
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope) != nil {
		a.t.Fatal("invalid actual gateway response")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		a.t.Fatalf("actual gateway rejected %s: %d %s", path, res.StatusCode, envelope.Error.Code)
	}
	if out != nil && json.Unmarshal(envelope.Data, out) != nil {
		a.t.Fatal("invalid fixture DTO")
	}
}
func jointLogin(t *testing.T) *jointAPI {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	a := &jointAPI{t: t, base: os.Getenv("ONE_NVR_PUBLIC_URL"), client: &http.Client{Jar: jar, Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}} // isolated self-signed test certificate only
	var state struct {
		CSRF string `json:"csrf_token"`
	}
	a.call("GET", "setup/status", nil, 0, &state)
	a.csrf = state.CSRF
	a.call("POST", "auth/login", map[string]string{"username": "admin", "password": "Browser-test-only-2026!"}, 0, &state)
	a.csrf = state.CSRF
	return a
}
func jointWait(t *testing.T, seconds int, reason string, check func() bool) {
	t.Helper()
	end := time.Now().Add(time.Duration(seconds) * time.Second)
	for time.Now().Before(end) {
		if check() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal(reason)
}
func (a *jointAPI) change(method, path string, body any, version int64) {
	var job struct {
		ID id.ID `json:"job_id"`
	}
	a.call(method, path, body, version, &job)
	jointWait(a.t, 90, "real change did not succeed", func() bool {
		var state struct {
			State string `json:"state"`
			Code  string `json:"error_code"`
		}
		a.call("GET", "jobs/"+string(job.ID), nil, 0, &state)
		if state.State == "failed" {
			a.t.Fatalf("real change failed: %s", state.Code)
		}
		return state.State == "succeeded"
	})
}
func jointRead(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, out) != nil {
		t.Fatal("private acceptance receipt unavailable")
	}
}
func jointWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, _ := json.Marshal(value)
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("cannot retain private acceptance receipt")
	}
}
func jointMedia(t *testing.T) *zlm.Client {
	t.Helper()
	s, e := secrets.Load("/data")
	if e != nil {
		t.Fatal("fixture secrets unavailable")
	}
	key, _ := s.ComponentCredential("zlm")
	m, e := zlm.New("http://zlm", key, nil)
	if e != nil {
		t.Fatal("private media API unavailable")
	}
	return m
}
func jointCollect(t *testing.T, db *database.DB) jointSnapshot {
	t.Helper()
	ctx := context.Background()
	out := jointSnapshot{Channels: []jointChannel{}}
	rows, err := db.Pool.Query(ctx, `SELECT c.id,r.id,r.stream_session_id,(SELECT count(*) FROM recording_segments s WHERE s.channel_id=c.id AND s.state='ready') FROM channels c JOIN recording_runs r ON r.channel_id=c.id WHERE c.channel_no IN (1,2) AND r.state='recording' AND r.purpose='continuous' ORDER BY c.channel_no`)
	if err != nil {
		t.Fatal("runtime snapshot unavailable")
	}
	for rows.Next() {
		var c jointChannel
		if rows.Scan(&c.Channel, &c.Run, &c.Session, &c.Ready) != nil {
			t.Fatal("runtime snapshot invalid")
		}
		out.Channels = append(out.Channels, c)
	}
	rows.Close()
	if rows.Err() != nil {
		t.Fatal("runtime snapshot incomplete")
	}
	_ = db.Pool.QueryRow(ctx, "SELECT coalesce(active_id::text,'') FROM gateway_tls_state WHERE singleton").Scan(&out.TLS)
	return out
}
func jointTwoRecording(t *testing.T, media *zlm.Client, s jointSnapshot) bool {
	t.Helper()
	if len(s.Channels) != 2 {
		return false
	}
	for _, c := range s.Channels {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		v, err := media.Inspect(ctx, zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(c.Session)})
		cancel()
		if err != nil || !v.Recording {
			return false
		}
		good := false
		for _, tr := range v.Tracks {
			good = good || tr.Ready && tr.Frames > 0
		}
		if !good {
			return false
		}
	}
	return true
}

// All phases operate on the same real production API/Worker/ZLM/PostgreSQL
// fixture. Shell orchestrates actual container faults; no domain state is seeded.
func TestGatewayMediaRuntimeJoint(t *testing.T) {
	if os.Getenv("ONE_NVR_GATEWAY_RUNTIME") != "isolated" {
		t.Fatal("requires isolated actual media runtime")
	}
	phase := os.Getenv("ONE_NVR_JOINT_PHASE")
	if phase == "" {
		t.Fatal("requires an explicit joint acceptance phase")
	}
	media := jointMedia(t)
	if phase == "spool" {
		var before jointSnapshot
		jointRead(t, "/results/joint-before.json", &before)
		jointWait(t, 80, "actual DB outage produced no durable completion spool", func() bool { names, _ := filepath.Glob("/data/recording-spool/*.json"); return len(names) > 0 })
		if !jointTwoRecording(t, media, before) {
			t.Fatal("DB outage stopped upstream recorders")
		}
		for _, name := range mustGlob(t, "/data/recording-spool/*.json") {
			info, err := os.Stat(name)
			if err != nil || info.Mode().Perm() != 0600 || info.Size() > 65536 {
				t.Fatal("unsafe completion spool")
			}
		}
		jointWrite(t, "/results/joint-spool.json", map[string]any{"actual_db_outage": true, "upstream_recorders": 2, "private_spool": true})
		return
	}
	db, err := database.Open(context.Background(), os.Getenv("ONE_NVR_DATABASE_URL"))
	if err != nil {
		t.Fatal("actual fixture database unavailable")
	}
	defer db.Pool.Close()
	if phase == "bootstrap" {
		a := jointLogin(t)
		var fixture struct {
			IP    string   `json:"camera_ip"`
			Paths []string `json:"paths"`
		}
		jointRead(t, "/results/media-fixture.json", &fixture)
		var channels struct {
			Items []struct {
				ID id.ID `json:"id"`
				No int   `json:"channel_no"`
			} `json:"items"`
		}
		a.call("GET", "channels?limit=100", nil, 0, &channels)
		if len(channels.Items) != 32 {
			t.Fatal("permanent slot mapping lost")
		}
		var pools struct {
			Items []struct {
				ID id.ID `json:"id"`
			} `json:"items"`
		}
		a.call("GET", "storage-pools", nil, 0, &pools)
		if len(pools.Items) != 1 {
			t.Fatal("actual pool missing")
		}
		pool := pools.Items[0].ID
		for _, ch := range channels.Items {
			if ch.No != 2 {
				continue
			}
			var current struct {
				Version int64 `json:"version"`
			}
			a.call("GET", "channels/"+string(ch.ID), nil, 0, &current)
			var rev struct {
				ID id.ID `json:"id"`
			}
			a.call("POST", "channels/"+string(ch.ID)+"/source-revisions", map[string]any{"identity_intent": "replace", "config": map[string]any{"ip": fixture.IP, "rtsp_port": 554, "main_path": fixture.Paths[0], "sub_path": fixture.Paths[1], "transport": "tcp"}, "credentials": map[string]string{"password_action": "clear"}}, current.Version, &rev)
			var tested struct {
				JobID  id.ID `json:"job_id"`
				TestID id.ID `json:"test_id"`
			}
			a.call("POST", "channels/"+string(ch.ID)+"/source-revisions/"+string(rev.ID)+"/test", map[string]any{}, 0, &tested)
			jointWait(t, 70, "actual second source test failed", func() bool {
				var result struct {
					State string `json:"state"`
				}
				a.call("GET", "channels/"+string(ch.ID)+"/source-tests/"+string(tested.TestID), nil, 0, &result)
				if result.State == "failed" {
					t.Fatal("actual second source unavailable")
				}
				return result.State == "succeeded"
			})
			a.call("GET", "channels/"+string(ch.ID), nil, 0, &current)
			a.change("POST", "channels/"+string(ch.ID)+"/source/apply", map[string]any{"revision_id": rev.ID, "test_id": tested.TestID, "first_recording_mode": "none"}, current.Version)
		}
		a.change("POST", "storage-pools/"+string(pool)+"/test", map[string]any{}, 0)
		for _, ch := range channels.Items {
			if ch.No != 1 && ch.No != 2 {
				continue
			}
			var state struct {
				Version int64  `json:"version"`
				Pool    *id.ID `json:"storage_pool_id"`
			}
			a.call("GET", "channels/"+string(ch.ID)+"/source/status", nil, 0, &state)
			if state.Pool == nil {
				a.change("PUT", "channels/"+string(ch.ID)+"/storage-pool", map[string]any{"pool_id": pool}, state.Version)
				a.call("GET", "channels/"+string(ch.ID)+"/source/status", nil, 0, &state)
			}
			a.change("PUT", "channels/"+string(ch.ID)+"/recording-policy", map[string]string{"mode": "continuous"}, state.Version)
		}
		jointWait(t, 100, "two actual full-duration ready segments unavailable", func() bool {
			var count int
			err := db.Pool.QueryRow(context.Background(), `SELECT count(DISTINCT c.channel_no) FROM recording_segments s JOIN channels c ON c.id=s.channel_id WHERE c.channel_no IN (1,2) AND s.state='ready' AND s.end_at-s.start_at>=interval '55 seconds'`).Scan(&count)
			return err == nil && count == 2
		})
		current := jointCollect(t, db)
		if !jointTwoRecording(t, media, current) {
			t.Fatal("two actual recorders unavailable")
		}
		jointWrite(t, "/results/joint-before.json", current)
		return
	}
	if phase == "rollback-failure" {
		jointRollbackFailure(t, db)
		return
	}
	if phase == "snapshot" {
		current := jointCollect(t, db)
		if !jointTwoRecording(t, media, current) {
			t.Fatal("cannot snapshot two actual recorders")
		}
		jointWrite(t, "/results/joint-before.json", current)
		return
	}
	var before jointSnapshot
	jointRead(t, "/results/joint-before.json", &before)
	if len(before.Channels) != 2 {
		t.Fatal("incomplete two-channel receipt")
	}
	if phase == "tls" {
		chain, key, err := testcerts.Pair("gateway", 402)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string][]byte{"fullchain.pem": chain, "privkey.pem": key} {
			tmp := "/tls-input/" + name + ".new"
			if os.WriteFile(tmp, []byte(value), 0600) != nil || os.Rename(tmp, "/tls-input/"+name) != nil {
				t.Fatal("fixture certificate rotation failed")
			}
		}
		jointWait(t, 90, "mapped certificate did not become active", func() bool { current := jointCollect(t, db); return current.TLS != "" && current.TLS != before.TLS })
		connection, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", "gateway:443", &tls.Config{InsecureSkipVerify: true})
		if err != nil {
			t.Fatal("actual TLS listener unavailable")
		}
		certs := connection.ConnectionState().PeerCertificates
		connection.Close()
		if len(certs) == 0 || certs[0].SerialNumber.Int64() != 402 {
			t.Fatal("actual listener still uses old certificate")
		}
	}
	if phase != "restart" && phase != "db" && phase != "zlm" && phase != "tls" && phase != "modules" && phase != "rollback-recovery" {
		t.Fatal("unknown joint acceptance phase")
	}
	jointWait(t, 120, "actual runtime did not resume two recorders and new ready segments", func() bool {
		current := jointCollect(t, db)
		if !jointTwoRecording(t, media, current) {
			return false
		}
		for i, c := range current.Channels {
			if c.Channel != before.Channels[i].Channel || c.Ready <= before.Channels[i].Ready {
				return false
			}
			if phase == "zlm" || phase == "rollback-recovery" {
				if c.Run == before.Channels[i].Run || c.Session == before.Channels[i].Session {
					return false
				}
			} else if c.Run != before.Channels[i].Run || c.Session != before.Channels[i].Session {
				return false
			}
		}
		return true
	})
	if phase == "db" {
		jointWait(t, 30, "completion spool did not drain", func() bool { names, _ := filepath.Glob("/data/recording-spool/*.json"); return len(names) == 0 })
	}
	// Verify actual indexed bytes with the locked FFprobe, not just database state.
	rows, err := db.Pool.Query(context.Background(), `SELECT DISTINCT ON(c.id) p.path,l.relative_path,l.size_bytes FROM recording_locations l JOIN storage_pools p ON p.id=l.pool_id JOIN recording_segments s ON s.id=l.segment_id JOIN channels c ON c.id=s.channel_id WHERE c.channel_no IN (1,2) AND s.state='ready' ORDER BY c.id,s.ready_at DESC`)
	if err != nil {
		t.Fatal("published files unavailable")
	}
	files := 0
	for rows.Next() {
		files++
		var base, relative string
		var size int64
		if rows.Scan(&base, &relative, &size) != nil {
			t.Fatal("invalid file receipt")
		}
		path := filepath.Join(base, relative)
		info, err := os.Stat(path)
		if err != nil || info.Size() != size {
			t.Fatal("indexed media file lost")
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal("indexed media unreadable")
		}
		_, err = (probe.Runner{FFprobe: "/usr/bin/ffprobe"}).InspectMP4(context.Background(), file)
		file.Close()
		if err != nil {
			t.Fatal("indexed MP4 structure invalid")
		}
	}
	rows.Close()
	if rows.Err() != nil || files != 2 {
		t.Fatal("published file verification incomplete")
	}
	jointWrite(t, "/results/joint-"+phase+".json", map[string]any{"phase": phase, "actual_media": true, "upstream_recorders": 2, "new_ready_segments": true, "history_files_verified": true})
}
func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	names, err := filepath.Glob(pattern)
	if err != nil || len(names) == 0 {
		t.Fatal("completion spool absent")
	}
	return names
}

// Explicit public-route checks use no cookie or component token. Their status
// is evidence; component startup logs are never a permission check.
func TestGatewayMediaRuntimeBoundaries(t *testing.T) {
	if os.Getenv("ONE_NVR_GATEWAY_RUNTIME") != "isolated" {
		t.Fatal("requires isolated runtime")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := os.Getenv("ONE_NVR_PUBLIC_URL")
	home, err := client.Get(base + "/")
	if err != nil {
		t.Fatal("gateway SPA reference unavailable")
	}
	reference, err := io.ReadAll(io.LimitReader(home.Body, 65537))
	home.Body.Close()
	if err != nil || home.StatusCode != 200 || len(reference) > 65536 || !bytes.Contains(reference, []byte(`<div id="root">`)) {
		t.Fatal("invalid gateway SPA reference")
	}
	for _, path := range []string{"/on_record_mp4", "/on_play", "/index/api/getMediaList", "/index/api/webrtc?app=one_nvr&stream=fixture&type=play", "/record/one_nvr/fixture.mp4"} {
		r, err := client.Get(base + path)
		if err != nil {
			t.Fatal("gateway boundary check unavailable")
		}
		body, readErr := io.ReadAll(io.LimitReader(r.Body, 65537))
		r.Body.Close()
		spa := readErr == nil && len(body) <= 65536 && r.StatusCode == 200 && strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") && bytes.Equal(body, reference)
		if !spa && r.StatusCode != 401 && r.StatusCode != 403 && r.StatusCode != 404 {
			t.Fatalf("private media public route exposed: %s status %d", strings.Split(path, "?")[0], r.StatusCode)
		}
	}
	receipt := "/results/joint-boundaries.json"
	if root := os.Getenv("ONE_NVR_JOINT_RECEIPT_DIR"); root != "" {
		receipt = filepath.Join(root, "joint-boundaries.json")
	}
	jointWrite(t, receipt, map[string]bool{"private_media_routes_denied": true})
}

// The candidate was actually tested while both source pairs were publishing.
// Stop both old/new publishers only after proof; production must fail rollback
// visibly and release its guard, then reconnect the original source on recovery.
func jointRollbackFailure(t *testing.T, db *database.DB) {
	a := jointLogin(t)
	ctx := context.Background()
	var fixture struct {
		IP    string   `json:"camera_ip"`
		Paths []string `json:"paths"`
	}
	jointRead(t, "/results/media-fixture.json", &fixture)
	if len(fixture.Paths) != 4 {
		t.Fatal("incomplete physical source fixture")
	}
	var ch, old id.ID
	var version int64
	if err := db.Pool.QueryRow(ctx, "SELECT id,current_revision_id,version FROM channels WHERE channel_no=2").Scan(&ch, &old, &version); err != nil {
		t.Fatal("old source receipt unavailable")
	}
	var rev struct {
		ID id.ID `json:"id"`
	}
	a.call("POST", "channels/"+string(ch)+"/source-revisions", map[string]any{"identity_intent": "replace", "config": map[string]any{"ip": fixture.IP, "rtsp_port": 554, "main_path": fixture.Paths[2], "sub_path": fixture.Paths[3], "transport": "tcp"}, "credentials": map[string]string{"password_action": "clear"}}, version, &rev)
	var tested struct {
		ID id.ID `json:"test_id"`
	}
	a.call("POST", "channels/"+string(ch)+"/source-revisions/"+string(rev.ID)+"/test", map[string]any{}, 0, &tested)
	jointWait(t, 70, "candidate proof unavailable before both-source failure", func() bool {
		var state struct {
			State string `json:"state"`
		}
		a.call("GET", "channels/"+string(ch)+"/source-tests/"+string(tested.ID), nil, 0, &state)
		if state.State == "failed" {
			t.Fatal("candidate failed before fault injection")
		}
		return state.State == "succeeded"
	})
	response, err := (&http.Client{Timeout: 5 * time.Second}).Post("http://fixture:8557/stop-all", "application/json", nil)
	if err != nil {
		t.Fatal("physical publisher fault control unavailable")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("publishers not stopped")
	}
	camera, err := zlm.New("http://camera", "isolated-media-fixture-only", nil)
	if err != nil {
		t.Fatal("camera verifier unavailable")
	}
	jointWait(t, 5, "both upstream sources still available", func() bool {
		for _, path := range fixture.Paths {
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			_, err := camera.Inspect(bounded, zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: strings.TrimPrefix(path, "/one_nvr/")})
			cancel()
			if !errors.Is(err, zlm.ErrStreamAbsent) {
				return false
			}
		}
		return true
	})
	var current struct {
		Version int64 `json:"version"`
	}
	a.call("GET", "channels/"+string(ch), nil, 0, &current)
	var job struct {
		ID id.ID `json:"job_id"`
	}
	a.call("POST", "channels/"+string(ch)+"/source/apply", map[string]any{"revision_id": rev.ID, "test_id": tested.ID}, current.Version, &job)
	jointWait(t, 95, "both-source failure did not terminate", func() bool {
		var state struct {
			State string `json:"state"`
			Code  string `json:"error_code"`
		}
		a.call("GET", "jobs/"+string(job.ID), nil, 0, &state)
		if state.State == "succeeded" {
			t.Fatal("both absent sources falsely succeeded")
		}
		if state.State != "failed" {
			return false
		}
		if state.Code != "source_rollback_failed" {
			t.Fatalf("rollback failure misclassified: %s", state.Code)
		}
		return true
	})
	var active int
	var actualOld id.ID
	var phase string
	if err := db.Pool.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", ch).Scan(&actualOld); err != nil || actualOld != old {
		t.Fatal("failed rollback rewrote source provenance")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT phase FROM source_switches WHERE job_id=$1", job.ID).Scan(&phase); err != nil || phase != "failed" {
		t.Fatal("rollback domain failure hidden")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM source_switches WHERE channel_id=$1 AND state IN ('queued','running')", ch).Scan(&active); err != nil || active != 0 {
		t.Fatal("failed switch retained configuration guard")
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM stream_sessions WHERE switch_id=(SELECT id FROM source_switches WHERE job_id=$1) AND state NOT IN ('closed','failed')", job.ID).Scan(&active); err != nil || active != 0 {
		t.Fatal("candidate/rollback physical intent leaked")
	}
	var status struct {
		Main struct {
			State string `json:"state"`
		} `json:"main"`
	}
	a.call("GET", "channels/"+string(ch)+"/source/status", nil, 0, &status)
	if status.Main.State != "unavailable" {
		t.Fatal("rollback outage falsely healthy")
	}
	rows, err := db.Pool.Query(ctx, "SELECT ss.stream FROM stream_sessions ss JOIN channels c ON c.id=ss.channel_id WHERE c.channel_no IN (1,2) AND ss.purpose='main'")
	if err != nil {
		t.Fatal("physical session receipts unavailable")
	}
	var streams []string
	for rows.Next() {
		var stream string
		if rows.Scan(&stream) != nil {
			t.Fatal("invalid physical receipt")
		}
		streams = append(streams, stream)
	}
	rows.Close()
	if rows.Err() != nil {
		t.Fatal("incomplete physical receipts")
	}
	runtime := jointMedia(t)
	for _, stream := range streams {
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		ss, err := runtime.Inspect(bounded, zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: stream})
		cancel()
		if err != nil && !errors.Is(err, zlm.ErrStreamAbsent) {
			t.Fatal("actual unavailable media state unknown")
		}
		if err == nil && ss.Recording {
			t.Fatal("absent source still has a recorder")
		}
	}
	jointWrite(t, "/results/joint-rollback-failure.json", map[string]any{"both_upstreams_absent": true, "terminal_rollback_failure": true, "original_revision_retained": true, "no_live_change_guard": true, "no_live_candidate_sessions": true})
}
