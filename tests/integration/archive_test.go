package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/recording"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
)

// webdavTestServer is a minimal in-process WebDAV: PUT/GET/DELETE/MKCOL over
// one in-memory map, with basic auth. The probe's full capability chain runs
// against it unchanged.
func webdavTestServer(t *testing.T, username, password string) *httptest.Server {
	t.Helper()
	store := map[string][]byte{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != username || pass != password {
			w.WriteHeader(401)
			return
		}
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			store[r.URL.Path] = body
			w.WriteHeader(201)
		case http.MethodGet:
			data, ok := store[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
				// A minimal single-range parser: bytes=start-end with both ends
				// present is all the archive client ever sends.
				var from, to int
				if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &from, &to); err != nil || from >= len(data) {
					w.WriteHeader(416)
					return
				}
				if to >= len(data) {
					to = len(data) - 1
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to, len(data)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(data[from : to+1])
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(store, r.URL.Path)
			w.WriteHeader(204)
		case "MKCOL":
			w.WriteHeader(201)
		default:
			w.WriteHeader(405)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func runUploadJob(t *testing.T, fixture publishFixture, service *archive.Service, ctx context.Context) {
	t.Helper()
	repo := jobs.Repository{DB: fixture.DB}
	lease, err := repo.Claim(ctx, "recording.archive_upload")
	if err != nil {
		t.Fatal("upload job not claimable", err)
	}
	if err := jobs.Execute(ctx, repo, lease, service.HandleUpload); err != nil {
		t.Fatal("upload job execution failed", err)
	}
}

func TestArchiveTargetLifecycle(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	dav := webdavTestServer(t, "archiver", "dav-secret")
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)

	input := archive.TargetInput{
		Name: "主归档", BaseURL: dav.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 365, Enabled: true,
	}
	// Only an administrator configures targets.
	viewer := auth.Principal{Role: "viewer"}
	if _, err := service.ListTargets(ctx, viewer); err == nil {
		t.Fatal("targets are admin-only")
	}
	target, err := service.RegisterTarget(ctx, fixture.Admin, input)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID == "" || target.LastCheck != nil {
		t.Fatal("registered target starts unchecked", target)
	}
	// The draft probe verifies write/read/range/delete against the real server.
	caps, err := service.TestTarget(ctx, fixture.Admin, "", &input)
	if err != nil || !caps.Write || !caps.Read || !caps.Range || !caps.Delete {
		t.Fatal("probe must verify the full chain", caps, err)
	}
	// A probe against the stored target lands in last_check.
	if _, err := service.TestTarget(ctx, fixture.Admin, target.ID, nil); err != nil {
		t.Fatal(err)
	}
	checked, err := service.ListTargets(ctx, fixture.Admin)
	if err != nil || len(checked) != 1 || checked[0].LastCheck == nil {
		t.Fatal("the probe result must be recorded", checked, err)
	}
	// The password never leaves the service.
	if _, err := service.UpdateTarget(ctx, fixture.Admin, target.ID, archive.TargetInput{
		Name: "主归档", BaseURL: dav.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "", CloudRetentionDays: 365, Enabled: true,
	}); err != nil {
		t.Fatal("an empty password must keep the stored one", err)
	}
	// A wrong password fails the probe and marks the target unavailable.
	if _, err := service.TestTarget(ctx, fixture.Admin, "", &archive.TargetInput{
		Name: "x", BaseURL: dav.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "wrong", CloudRetentionDays: 365, Enabled: true,
	}); err == nil {
		t.Fatal("bad credentials must fail the probe")
	}
	if err := service.DeleteTarget(ctx, fixture.Admin, target.ID); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveUploadFlow(t *testing.T) {
	fixture := newPublicationFixture(t)
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ctx := context.Background()
	dav := webdavTestServer(t, "archiver", "dav-secret")
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: mediaTestTool(t, "ffprobe")}
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "主归档", BaseURL: dav.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A viewer without the archive grant is silently skipped (partial success,
	// the per-row gate of ARC-08).
	viewerUser, err := fixture.Auth.CreateUser(ctx, fixture.Admin, auth.CreateUserInput{Username: "archive_viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := fixture.Auth.Login(ctx, "archive_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.CreateUploads(ctx, login.Principal, target.ID, []id.ID{segment.ID})
	if err != nil || len(queued) != 0 {
		t.Fatal("without the archive grant nothing is queued", queued, err)
	}
	// The grant authorizes the same request.
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO channel_grants(user_id,channel_id,archive) VALUES($1,$2,true) ON CONFLICT (user_id,channel_id) DO UPDATE SET archive=true`, viewerUser.ID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	queued, err = service.CreateUploads(ctx, login.Principal, target.ID, []id.ID{segment.ID})
	if err != nil || len(queued) != 1 {
		t.Fatal("the archive grant authorizes the upload", queued, err)
	}
	// An admin request for the same segment reuses the task (idempotency).
	again, err := service.CreateUploads(ctx, fixture.Admin, target.ID, []id.ID{segment.ID})
	if err != nil || len(again) != 1 {
		t.Fatal("repeated selection reuses the task", again, err)
	}
	runUploadJob(t, fixture, service, ctx)

	var diagState, diagErr string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,coalesce(error_code,'') FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&diagState, &diagErr); err != nil {
		t.Fatal(err)
	}
	t.Logf("copy after job: state=%s error=%s", diagState, diagErr)

	page, err := fixture.Service.List(ctx, fixture.Admin, recording.Query{ChannelID: fixture.Channel, Start: fixture.Completion.StartTime.Add(-time.Second), End: fixture.Completion.StartTime.Add(3 * time.Second), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Items[0].Archived {
		t.Fatal("a verified copy must surface as archived", page.Items)
	}
	var state, sha, objectKey string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,sha256,object_key FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&state, &sha, &objectKey); err != nil {
		t.Fatal(err)
	}
	if state != "verified" || len(sha) != 64 || sha == strings.Repeat("0", 64) {
		t.Fatal("copy must be verified with a real hash", state, sha)
	}
	// ARC-03: the remote key mirrors the frozen local layout under the site
	// namespace.
	if !strings.HasPrefix(objectKey, string(fixture.Site.Secrets.SiteID)+"/recordings/") {
		t.Fatal("object key must mirror the local layout under the site namespace", objectKey)
	}
	// The uploaded object verifies against the local file's hash.
	local, err := os.ReadFile(filepath.Join(fixture.Pool.Path, localTargetOf(t, fixture, segment.ID)))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(local)
	if hex.EncodeToString(digest[:]) != sha {
		t.Fatal("uploaded bytes must match the local file", sha)
	}

	// An in-flight upload pins the source against deletion, with its own reason.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_segments SET state='ready' WHERE id=$1`, segment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET state='uploading' WHERE segment_id=$1`, segment.ID); err != nil {
		t.Fatal(err)
	}
	outcomes, err := fixture.Service.DeleteLocal(ctx, fixture.Admin, []id.ID{segment.ID}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Reason != "archiving" {
		t.Fatal("an in-flight upload pins the source", outcomes)
	}
	// Terminal again: deletion proceeds (verified copy survives locally).
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET state='verified' WHERE segment_id=$1`, segment.ID); err != nil {
		t.Fatal(err)
	}
	outcomes, err = fixture.Service.DeleteLocal(ctx, fixture.Admin, []id.ID{segment.ID}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].State != "deleted" {
		t.Fatal("a finished upload no longer pins the source", outcomes)
	}
}

func localTargetOf(t *testing.T, fixture publishFixture, segmentID id.ID) string {
	t.Helper()
	var target string
	if err := fixture.DB.Pool.QueryRow(context.Background(), `SELECT target_relative_path FROM recording_segments WHERE id=$1`, segmentID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestArchiveUploadRetryAndFailure(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "主归档", BaseURL: "http://127.0.0.1:1/dav", RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.CreateUploads(ctx, fixture.Admin, target.ID, []id.ID{segment.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatal("the upload request must queue the ready segment", queued)
	}
	// An unreachable target is environmental: the copy stays uploading with the
	// reason recorded, and the job retries rather than terminating.
	repo := jobs.Repository{DB: fixture.DB}
	lease, err := repo.Claim(ctx, "recording.archive_upload")
	if err != nil {
		t.Fatal(err)
	}
	executeErr := jobs.Execute(ctx, repo, lease, service.HandleUpload)
	var permanent *jobs.PermanentFailure
	if errors.As(executeErr, &permanent) {
		t.Fatal("an unreachable target must stay retryable", executeErr)
	}
	var state, errorCode string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,error_code FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&state, &errorCode); err != nil {
		t.Fatal(err)
	}
	if state != "uploading" || errorCode != "upload_failed" {
		t.Fatal("retryable failure keeps the task alive with a reason", state, errorCode)
	}
	// An unknown target is not found.
	if _, err := service.CreateUploads(ctx, fixture.Admin, "00000000-0000-4000-8000-000000000000", []id.ID{segment.ID}); err == nil {
		t.Fatal("an unknown target is refused")
	}
}
