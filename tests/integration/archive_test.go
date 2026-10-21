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
	"github.com/zhigu34/one-nvr/internal/operations"
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
	// "Retryable" has to mean the job actually goes back to the queue. Returning
	// nil from the handler would mark the job succeeded while the copy still
	// says uploading, and nothing would ever pick it up again.
	var jobState, jobError string
	var available bool
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,coalesce(error_code,''),available_at<=clock_timestamp()+interval '31 seconds' FROM jobs WHERE kind='recording.archive_upload'`).
		Scan(&jobState, &jobError, &available); err != nil {
		t.Fatal(err)
	}
	if jobState != "queued" {
		t.Fatal("a retryable upload must return to the queue", jobState, jobError)
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

// The run status answers a question the target list cannot: whether uploads
// are actually working. It has to keep the three states apart, because "idle"
// (nothing tried) reading as "healthy" is how an archive stays broken for a
// week with a green page.
func TestArchiveTargetStatusSeparatesIdleFromFailing(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	// An unreachable target: an upload against it fails for a real reason.
	target, err := service.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "主归档", BaseURL: "http://127.0.0.1:1/dav", RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := service.TargetStatuses(ctx, fixture.Admin)
	if err != nil || len(statuses) != 1 {
		t.Fatal("every target has a status row, even with no copies", statuses, err)
	}
	if statuses[0].Verified != 0 || statuses[0].Uploading != 0 || statuses[0].Failed != 0 {
		t.Fatal("a fresh target reports nothing attempted", statuses[0])
	}

	if _, err := service.CreateUploads(ctx, fixture.Admin, target.ID, []id.ID{segment.ID}); err != nil {
		t.Fatal(err)
	}
	statuses, err = service.TargetStatuses(ctx, fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].Uploading != 1 || statuses[0].OldestPendingAt == nil {
		t.Fatal("a queued copy is visible as pending, with when it was queued", statuses[0])
	}

	// Drive the job to a terminal failure: the target is unreachable and the
	// retry budget runs out.
	repo := jobs.Repository{DB: fixture.DB}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE jobs SET max_attempts=1 WHERE kind='recording.archive_upload'`); err != nil {
		t.Fatal(err)
	}
	lease, err := repo.Claim(ctx, "recording.archive_upload")
	if err != nil {
		t.Fatal(err)
	}
	_ = jobs.Execute(ctx, repo, lease, service.HandleUpload)
	statuses, err = service.TargetStatuses(ctx, fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].Uploading != 0 {
		t.Fatal("an exhausted upload is no longer in flight", statuses[0])
	}
	// The copy stays 'uploading' with a reason while the job retries; once the
	// job terminalizes, the row is still 'uploading' — which is exactly the
	// shape the retry path exists for. Either way the failure has to be named.
	if statuses[0].Failed == 0 && statuses[0].LastError == "" {
		t.Fatalf("a failed upload must be visible as a failure: %+v", statuses[0])
	}
}

// A failed copy used to be a dead end: the job reached a terminal state, and
// re-selecting the segment read the same terminal job back. The retry path is
// what makes the failure state non-final.
func TestArchiveRetryRevivesFailedCopies(t *testing.T) {
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
	if _, err := service.CreateUploads(ctx, fixture.Admin, target.ID, []id.ID{segment.ID}); err != nil {
		t.Fatal(err)
	}
	// Force the terminal state the retry path exists for.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET state='failed',error_code='upload_failed' WHERE segment_id=$1`, segment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE jobs SET state='failed',attempt=max_attempts,error_code='handler_failed' WHERE kind='recording.archive_upload'`); err != nil {
		t.Fatal(err)
	}

	revived, err := service.RetryFailures(ctx, fixture.Admin, target.ID)
	if err != nil || revived != 1 {
		t.Fatal("the failed copy must be queued again", revived, err)
	}
	var state string
	var attempt int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "uploading" {
		t.Fatal("a revived copy goes back to uploading", state)
	}
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT attempt FROM jobs WHERE kind='recording.archive_upload'`).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	// The retry budget belongs to one attempt sequence; asking again starts a
	// new one, otherwise the very first revival would exhaust immediately.
	if attempt != 0 {
		t.Fatal("a manual retry restarts the attempt budget", attempt)
	}
	// The job must be claimable again, which is the whole point.
	repo := jobs.Repository{DB: fixture.DB}
	if _, err := repo.Claim(ctx, "recording.archive_upload"); err != nil {
		t.Fatal("the revived job must be claimable", err)
	}

	// A second call with nothing failed reports zero rather than lying.
	again, err := service.RetryFailures(ctx, fixture.Admin, target.ID)
	if err != nil || again != 0 {
		t.Fatal("an in-flight copy is not retried again", again, err)
	}
}

// The channel overview counts against the copies that reached THIS target, so
// "how much of this channel is archived" is one subtraction.
func TestChannelArchiveStatusCountsAgainstItsOwnTarget(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	dav := webdavTestServer(t, "archiver", "dav-secret")
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
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
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: target.ID, Mode: archive.ModeImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateUploads(ctx, fixture.Admin, target.ID, []id.ID{segment.ID}); err != nil {
		t.Fatal(err)
	}
	runUploadJob(t, fixture, service, ctx)

	items, err := service.ChannelArchiveStatuses(ctx, fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	// Every channel is listed, bound or not: the page shows a complete table and
	// renders an unbound channel as "未归档" rather than leaving a gap.
	if len(items) == 0 {
		t.Fatal("no channels listed")
	}
	var item archive.ChannelArchiveStatus
	for _, candidate := range items {
		if candidate.ChannelNo == 1 {
			item = candidate
		}
	}
	if item.ChannelID == "" {
		t.Fatalf("the bound channel must be listed, got %+v", items)
	}
	if item.Name == "" {
		t.Fatal("the row names the channel", item)
	}
	if item.Target != "主归档" || !item.TargetEnabled || item.Mode != archive.ModeImmediate {
		t.Fatal("the row carries the binding", item)
	}
	if item.Segments != 1 || item.Copied != 1 {
		t.Fatal("the verified copy counts against the channel", item)
	}
	// The local retention window is what a verified copy unlocks; reporting it
	// next to the count keeps the consequence visible.
	if item.LocalRetentionHours == 0 {
		t.Fatal("the site's local archive window must be reported", item)
	}

	// Removing the policy keeps the channel in the list, now with no binding:
	// the row survives so the table stays one line per channel.
	if _, err := fixture.DB.Pool.Exec(ctx, `DELETE FROM archive_policies WHERE channel_id=$1`, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	items, err = service.ChannelArchiveStatuses(ctx, fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	var unbound *archive.ChannelArchiveStatus
	for i, candidate := range items {
		if candidate.ChannelNo == 1 {
			unbound = &items[i]
		}
	}
	if unbound == nil {
		t.Fatal("an unbound channel must still be listed")
	}
	if unbound.Mode != "none" || unbound.Target != "" || unbound.TargetEnabled {
		t.Fatalf("an unbound channel carries no binding: %+v", *unbound)
	}
}

// The archive page offers a link into the bundled console, whose port and state
// have to come from the deployment. The rule that matters: an expired
// observation is not evidence, so the link must not claim the console is up.
func TestBundledOpenListReportsPortAndHonestState(t *testing.T) {
	_, accounts, _, admin := authFixture(t)
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := operations.New(fixture.DB, accounts, false, false)
	if _, err := service.BundledOpenList(ctx, auth.Principal{Role: "viewer"}); err == nil {
		t.Fatal("the bundled console is site infrastructure: admin only")
	}

	// Module off: the console does not exist, and no port is published.
	got, err := service.BundledOpenList(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.State != "disabled" || got.UIPort != 0 {
		t.Fatalf("a deployment without the module has no console: %+v", got)
	}

	// Module on, port published, no observation yet.
	service = operations.New(fixture.DB, accounts, false, true)
	service.OpenListUIPort = 5245
	got, err = service.BundledOpenList(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.UIPort != 5245 {
		t.Fatalf("the port must reach the page so it can build the link: %+v", got)
	}
	if got.State != "unknown" {
		t.Fatalf("without an observation the console is not proven up, got %q", got.State)
	}

	// A fresh observation says it answers.
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO component_observations(name,state,reason_code,observed_at,expires_at)
		VALUES('openlist','healthy','probe_succeeded',clock_timestamp(),clock_timestamp()+interval '30 seconds')
		ON CONFLICT (name) DO UPDATE SET state='healthy',observed_at=clock_timestamp(),expires_at=clock_timestamp()+interval '30 seconds'`); err != nil {
		t.Fatal(err)
	}
	got, err = service.BundledOpenList(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "healthy" {
		t.Fatalf("a fresh healthy observation must be reported, got %q", got.State)
	}

	// The same row, expired, is not evidence — the page must not link to a
	// console on the strength of a stale sample.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE component_observations SET expires_at=clock_timestamp()-interval '1 second' WHERE name='openlist'`); err != nil {
		t.Fatal(err)
	}
	got, err = service.BundledOpenList(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "unknown" || got.Reason != "observation_expired" {
		t.Fatalf("an expired observation is unknown, not healthy: %+v", got)
	}
}

// A segment whose local copy vanished must reach the cloud fallback, not stop at
// "not published yet".
//
// OpenSegment answered ErrContentNotReady for every non-ready state. missing and
// damaged are not that: the segment was published and its bytes are gone. Saying
// "wait" meant the HTTP layer never asked the archive, so a recording with a
// verified remote copy was unplayable as soon as the local file disappeared.
func TestMissingLocalCopyReportsMissingNotNotReady(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	dav := webdavTestServer(t, "archiver", "dav-secret")
	archiveService := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	fixture.Service.Probe = probe.Runner{FFmpeg: mediaTestTool(t, "ffmpeg"), FFprobe: mediaTestTool(t, "ffprobe")}
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	target, err := archiveService.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "主归档", BaseURL: dav.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiveService.CreateUploads(ctx, fixture.Admin, target.ID, []id.ID{segment.ID}); err != nil {
		t.Fatal(err)
	}
	runUploadJob(t, fixture, archiveService, ctx)

	// The reconciler marks a vanished file 'missing'; simulate that verdict.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_segments SET state='missing' WHERE id=$1`, segment.ID); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Service.OpenSegment(ctx, fixture.Admin, segment.ID)
	if !errors.Is(err, recording.ErrContentMissing) {
		t.Fatalf("a missing local copy must read as missing, got %v", err)
	}
	// Which is what lets the HTTP layer serve the verified cloud copy.
	if errors.Is(err, recording.ErrContentNotReady) {
		t.Fatal("a missing copy must never read as not-yet-published")
	}
	// And the cloud copy is reachable for that segment.
	cloud, cloudErr := archiveService.OpenCloudSegment(ctx, fixture.Admin, segment.ID)
	if cloudErr != nil {
		t.Fatalf("the verified cloud copy must open: %v", cloudErr)
	}
	if cloud.Size != segment.Bytes {
		t.Fatalf("cloud size %d must match the published %d", cloud.Size, segment.Bytes)
	}
}
