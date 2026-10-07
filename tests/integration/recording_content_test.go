package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/recording"
)

type contentFixture struct {
	publishFixture
	Segment   recording.Segment
	Handler   http.Handler
	Published []byte
	Target    string
}

func newContentFixture(t *testing.T) contentFixture {
	t.Helper()
	base := newPublicationFixture(t)
	ctx := context.Background()
	segment, err := base.Service.Publish(ctx, base.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	var target string
	if err := base.DB.Pool.QueryRow(ctx, "SELECT target_relative_path FROM recording_segments WHERE id=$1", segment.ID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(filepath.Join(base.Pool.Path, target))
	if err != nil {
		t.Fatal(err)
	}
	if len(published) < 64 {
		t.Fatalf("fixture segment too small for range assertions: %d bytes", len(published))
	}
	handler := httpapi.NewHandler(httpapi.Dependencies{Auth: base.Auth, Site: base.Site, Recordings: base.Service, PublicURL: "http://one-nvr.test:8080"})
	return contentFixture{publishFixture: base, Segment: segment, Handler: handler, Published: published, Target: target}
}

func (f contentFixture) path() string {
	return "/api/v1/recordings/" + string(f.Segment.ID) + "/content"
}

func (f contentFixture) request(t *testing.T, session, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://one-nvr.test:8080"+path, nil)
	r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: session})
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.Handler.ServeHTTP(w, r)
	return w
}

// get serves one authorized media request. Every media assertion goes through
// here so that a status/body mismatch is reported with the same detail.
func (f contentFixture) get(t *testing.T, session string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	return f.request(t, session, "GET", f.path(), headers)
}

func (f contentFixture) viewer(t *testing.T, name string) auth.LoginResult {
	t.Helper()
	ctx := context.Background()
	user, err := f.Auth.CreateUser(ctx, f.Admin, auth.CreateUserInput{Username: name, Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Auth.SetGrants(ctx, f.Admin, user.ID, 1, []auth.Grant{{ChannelID: f.Channel, Actions: []auth.Action{auth.Playback}}}); err != nil {
		t.Fatal(err)
	}
	login, err := f.Auth.Login(ctx, name, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return login
}

func (f contentFixture) setSegmentState(t *testing.T, state string) {
	t.Helper()
	if _, err := f.DB.Pool.Exec(context.Background(), "UPDATE recording_segments SET state=$2 WHERE id=$1", f.Segment.ID, state); err != nil {
		t.Fatal(err)
	}
}

func (f contentFixture) auditCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.DB.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_logs WHERE action='recording.streamed'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestRecordingContentHidesUnauthorizedSegments proves the information-hiding
// rule: an ungranted caller and a caller holding a genuinely unknown recording
// ID must be indistinguishable, and neither may receive a media byte.
func TestRecordingContentHidesUnauthorizedSegments(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	user, err := f.Auth.CreateUser(ctx, f.Admin, auth.CreateUserInput{Username: "content_ungranted", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := f.Auth.Login(ctx, "content_ungranted", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	ungranted := f.get(t, login.RawSession, nil)
	unknownID, _ := id.New()
	absent := f.request(t, login.RawSession, "GET", "/api/v1/recordings/"+string(unknownID)+"/content", nil)
	for name, response := range map[string]*httptest.ResponseRecorder{"ungranted": ungranted, "absent": absent} {
		if response.Code != 404 || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
			t.Fatalf("%s segment answered %d %s", name, response.Code, response.Body.String())
		}
		if bytes.Contains(response.Body.Bytes(), f.Published[:64]) {
			t.Fatalf("%s response carried media bytes", name)
		}
		if response.Header().Get("Content-Length") == strconv.Itoa(len(f.Published)) || response.Header().Get("Content-Range") != "" {
			t.Fatalf("%s response disclosed media length", name)
		}
	}
	if ungranted.Code != absent.Code {
		t.Fatal("unauthorized and unknown segments are distinguishable by status")
	}
	if f.auditCount(t) != 0 {
		t.Fatal("an unauthorized request was audited as a media read")
	}
	if err := f.Auth.SetGrants(ctx, f.Admin, user.ID, 1, []auth.Grant{{ChannelID: f.Channel, Actions: []auth.Action{auth.Playback}}}); err != nil {
		t.Fatal(err)
	}
	login, err = f.Auth.Login(ctx, "content_ungranted", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	granted := f.get(t, login.RawSession, nil)
	if granted.Code != 200 || !bytes.Equal(granted.Body.Bytes(), f.Published) {
		t.Fatalf("granted playback failed: %d %s", granted.Code, granted.Body.String())
	}
	if granted.Header().Get("Content-Type") != "video/mp4" || granted.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatal("content response is not advertized as seekable media", granted.Header())
	}
	if granted.Header().Get("ETag") == "" || granted.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("content response is missing its validator or no-store policy", granted.Header())
	}
	for _, private := range []string{f.Pool.Path, f.Target, "original_relative_path", ".work/"} {
		if strings.Contains(granted.Header().Get("ETag"), private) || strings.Contains(granted.Header().Get("Content-Range"), private) {
			t.Fatal("media response leaked an internal path")
		}
	}
}

// TestRecordingContentRefusesUnpublishedOrUnverifiableMedia proves that a
// segment whose publication no longer matches its bytes yields an explicit
// error and never a partial or truncated media stream.
func TestRecordingContentRefusesUnpublishedOrUnverifiableMedia(t *testing.T) {
	f := newContentFixture(t)
	media := filepath.Join(f.Pool.Path, f.Target)
	for _, state := range []string{"finalizing", "provisional", "missing", "damaged", "conflict"} {
		f.setSegmentState(t, state)
		response := f.get(t, f.Session, nil)
		if response.Code != 409 || !strings.Contains(response.Body.String(), "recording_not_ready") {
			t.Fatalf("state %s answered %d %s", state, response.Code, response.Body.String())
		}
		if bytes.Contains(response.Body.Bytes(), f.Published[:64]) || response.Header().Get("Content-Range") != "" {
			t.Fatalf("state %s produced media bytes", state)
		}
	}
	f.setSegmentState(t, "ready")
	aside := media + ".aside"
	if err := os.Rename(media, aside); err != nil {
		t.Fatal(err)
	}
	response := f.get(t, f.Session, nil)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "recording_missing") {
		t.Fatalf("removed file answered %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), f.Published[:64]) {
		t.Fatal("removed file produced media bytes")
	}
	// A rename preserves device, inode and mtime, so the published identity is
	// intact again and playback must resume without any repair step.
	if err := os.Rename(aside, media); err != nil {
		t.Fatal(err)
	}
	if recovered := f.get(t, f.Session, nil); recovered.Code != 200 || !bytes.Equal(recovered.Body.Bytes(), f.Published) {
		t.Fatalf("restored file answered %d", recovered.Code)
	}
	file, err := os.OpenFile(media, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{0x00}); err != nil {
		t.Fatal(err)
	}
	file.Close()
	response = f.get(t, f.Session, nil)
	if response.Code != 409 || !strings.Contains(response.Body.String(), "recording_damaged") {
		t.Fatalf("altered file answered %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), f.Published[:64]) {
		t.Fatal("altered file produced media bytes")
	}
}

// TestRecordingContentServesExactlyOneValidatedRange covers the byte-range
// contract: single range only, clamped ends, suffix form, 416 with the resource
// length, malformed headers degraded to a full read, and If-Range semantics.
func TestRecordingContentServesExactlyOneValidatedRange(t *testing.T) {
	f := newContentFixture(t)
	size := int64(len(f.Published))
	full := f.get(t, f.Session, nil)
	etag := full.Header().Get("ETag")
	if full.Code != 200 || full.Header().Get("Content-Length") != strconv.FormatInt(size, 10) || full.Header().Get("Content-Range") != "" {
		t.Fatalf("full read answered %d %v", full.Code, full.Header())
	}
	cases := []struct {
		name        string
		header      string
		ifRange     string
		status      int
		contentType string
		contentRng  string
		body        []byte
		code        string
	}{
		{name: "open ended", header: "bytes=0-", status: 206, contentRng: fmt.Sprintf("bytes 0-%d/%d", size-1, size), body: f.Published},
		{name: "middle window", header: "bytes=10-19", status: 206, contentRng: fmt.Sprintf("bytes 10-19/%d", size), body: f.Published[10:20]},
		{name: "single byte", header: "bytes=0-0", status: 206, contentRng: fmt.Sprintf("bytes 0-0/%d", size), body: f.Published[:1]},
		{name: "suffix", header: "bytes=-16", status: 206, contentRng: fmt.Sprintf("bytes %d-%d/%d", size-16, size-1, size), body: f.Published[size-16:]},
		{name: "end beyond length", header: fmt.Sprintf("bytes=%d-99999999", size-8), status: 206, contentRng: fmt.Sprintf("bytes %d-%d/%d", size-8, size-1, size), body: f.Published[size-8:]},
		{name: "start at length", header: fmt.Sprintf("bytes=%d-", size), status: 416, contentRng: fmt.Sprintf("bytes */%d", size), code: "range_not_satisfiable"},
		{name: "suffix zero", header: "bytes=-0", status: 416, contentRng: fmt.Sprintf("bytes */%d", size), code: "range_not_satisfiable"},
		{name: "multiple ranges", header: "bytes=0-1,4-5", status: 416, contentRng: fmt.Sprintf("bytes */%d", size), code: "range_not_satisfiable"},
		{name: "non bytes unit", header: "items=0-1", status: 200, body: f.Published},
		{name: "malformed", header: "bytes=abc", status: 200, body: f.Published},
		{name: "reversed bounds", header: "bytes=19-10", status: 200, body: f.Published},
		{name: "stale validator", header: "bytes=10-19", ifRange: `"stale"`, status: 200, body: f.Published},
		{name: "matching validator", header: "bytes=10-19", ifRange: etag, status: 206, contentRng: fmt.Sprintf("bytes 10-19/%d", size), body: f.Published[10:20]},
	}
	for _, c := range cases {
		headers := map[string]string{"Range": c.header}
		if c.ifRange != "" {
			headers["If-Range"] = c.ifRange
		}
		response := f.get(t, f.Session, headers)
		if response.Code != c.status {
			t.Fatalf("%s: status %d want %d (%s)", c.name, response.Code, c.status, response.Body.String())
		}
		if response.Header().Get("Content-Range") != c.contentRng {
			t.Fatalf("%s: Content-Range %q want %q", c.name, response.Header().Get("Content-Range"), c.contentRng)
		}
		if c.status == 416 {
			if !strings.Contains(response.Body.String(), c.code) || bytes.Contains(response.Body.Bytes(), f.Published[:64]) {
				t.Fatalf("%s: unsatisfiable range did not fail cleanly: %s", c.name, response.Body.String())
			}
			continue
		}
		if !bytes.Equal(response.Body.Bytes(), c.body) {
			t.Fatalf("%s: body %d bytes does not match the expected window %d", c.name, response.Body.Len(), len(c.body))
		}
		if response.Header().Get("Content-Length") != strconv.Itoa(len(c.body)) {
			t.Fatalf("%s: Content-Length %q want %d", c.name, response.Header().Get("Content-Length"), len(c.body))
		}
	}
	// HEAD never reaches the read path: the CSRF guard rejects every non-GET
	// method, so a client cannot probe range metadata without reading bytes.
	head := f.request(t, f.Session, "HEAD", f.path(), map[string]string{"Range": "bytes=10-19"})
	if head.Code != 403 || head.Header().Get("Content-Range") != "" {
		t.Fatalf("HEAD answered %d %v", head.Code, head.Header())
	}
}

// TestRecordingContentIsAuditedAndAuditFailureBlocks proves the media read is
// recorded before a byte leaves, and that an unauditable read is refused.
func TestRecordingContentIsAuditedAndAuditFailureBlocks(t *testing.T) {
	f := newContentFixture(t)
	ctx := context.Background()
	size := int64(len(f.Published))
	before := f.auditCount(t)
	response := f.get(t, f.Session, map[string]string{"Range": "bytes=4-11"})
	if response.Code != 206 {
		t.Fatalf("range read answered %d %s", response.Code, response.Body.String())
	}
	if after := f.auditCount(t); after != before+1 {
		t.Fatalf("range read wrote %d audit rows, want 1", after-before)
	}
	var actor id.ID
	var details []byte
	if err := f.DB.Pool.QueryRow(ctx, "SELECT actor_id,details FROM audit_logs WHERE action='recording.streamed' ORDER BY created_at DESC,id DESC LIMIT 1").Scan(&actor, &details); err != nil {
		t.Fatal(err)
	}
	var record struct {
		ChannelID string `json:"channel_id"`
		From      int64  `json:"byte_from"`
		To        int64  `json:"byte_to"`
		Segment   int64  `json:"segment_bytes"`
		Partial   bool   `json:"partial"`
	}
	if err := json.Unmarshal(details, &record); err != nil {
		t.Fatal(err)
	}
	if actor != f.Admin.UserID || record.ChannelID != string(f.Channel) || record.From != 4 || record.To != 11 || !record.Partial || record.Segment != size {
		t.Fatalf("media audit row is incomplete: %s", details)
	}
	for _, private := range []string{f.Pool.Path, f.Target, "://"} {
		if strings.Contains(string(details), private) {
			t.Fatal("media audit row leaked an internal path")
		}
	}
	if _, err := f.DB.Pool.Exec(ctx, "DROP TABLE audit_logs"); err != nil {
		t.Fatal(err)
	}
	blocked := f.get(t, f.Session, nil)
	if blocked.Code != 503 || bytes.Contains(blocked.Body.Bytes(), f.Published[:64]) {
		t.Fatalf("unauditable read answered %d %q", blocked.Code, blocked.Body.String())
	}
}

// TestRecordingContentConcurrentReadsAreIndependent proves the authorized read
// path holds no long-lived exclusive lock: parallel playbacks of the same
// segment and metadata queries all succeed with identical bytes.
func TestRecordingContentConcurrentReadsAreIndependent(t *testing.T) {
	f := newContentFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for index := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			response := f.get(t, f.Session, map[string]string{"Range": "bytes=8-23"})
			if response.Code != 206 || !bytes.Equal(response.Body.Bytes(), f.Published[8:24]) {
				errs <- fmt.Errorf("parallel read %d answered %d", index, response.Code)
			}
		}()
		go func() {
			defer wg.Done()
			page, err := f.Service.List(context.Background(), f.Admin, recording.Query{ChannelID: f.Channel, Start: f.Completion.StartTime, End: f.Completion.StartTime.Add(time.Minute), Limit: 10})
			if err != nil || len(page.Items) != 1 {
				errs <- fmt.Errorf("parallel metadata query %d failed: %v", index, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
