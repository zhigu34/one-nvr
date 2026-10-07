package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/site"
	"github.com/zhigu34/one-nvr/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
)

type publishFixture struct {
	Site                          *site.Service
	Session                       string
	DB                            *database.DB
	Service                       *recording.Service
	Auth                          *auth.Service
	Admin                         auth.Principal
	Pool                          storage.Pool
	Channel, Revision, Run, Inbox id.ID
	Completion                    recording.Completion
	Info                          os.FileInfo
}

func newPublicationFixture(t *testing.T) publishFixture {
	t.Helper()
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ffprobe := mediaTestTool(t, "ffprobe")
	dir := t.TempDir()
	db, accounts, sites, admin := authFixture(t, dir)
	ctx := context.Background()
	base := t.TempDir()
	pools := storage.New(db, accounts, []string{base})
	pool, err := pools.Register(ctx, admin.Principal, storage.RegisterInput{Name: "Publication pool", Path: base})
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
	work := ".work/zlm/" + string(run)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state) VALUES($1,$2,$3,1,'one_nvr',$1::uuid::text,'main','closed')`, session, ch, rev.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,'continuous','stopped',TIMESTAMPTZ '2026-10-03T16:04:39Z')`, run, sites.Secrets.SiteID, ch, rev.ID, session, pool.ID, work); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pool.Path, work, "record/one_nvr/"+string(session), "2026-10-04/2026-10-04-00-04-39-0.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=5", "-t", "2", "-c:v", "libx264", "-threads", "1", path).Run(); err != nil {
		t.Fatal("synthetic MP4 creation failed", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	checker := probe.Runner{FFprobe: ffprobe}
	service := recording.New(db, nil, checker, []string{base}, dir)
	service.Auth = accounts
	completion := recording.Completion{Key: zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(session)}, MediaServerID: "one-nvr-" + string(sites.Secrets.SiteID), FilePath: path, Size: info.Size(), StartTime: time.Date(2026, 10, 3, 16, 4, 39, 0, time.UTC), Duration: 2 * time.Second}
	if err := service.Accept(ctx, completion); err != nil {
		t.Fatal(err)
	}
	var inbox id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM hook_inbox WHERE run_id=$1", run).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	return publishFixture{Site: sites, Session: admin.RawSession, DB: db, Service: service, Auth: accounts, Admin: admin.Principal, Pool: pool, Channel: ch, Revision: rev.ID, Run: run, Inbox: inbox, Completion: completion, Info: info}
}
func TestRecordingPublishDuplicateAndHistory(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	if segment.State != "ready" || segment.ChannelID != fixture.Channel || segment.SourceRevisionID != fixture.Revision || segment.RunID != fixture.Run {
		t.Fatal("published provenance incorrect", segment)
	}
	relative, _ := filepath.Rel(fixture.Pool.Path, fixture.Completion.FilePath)
	expectedID, _ := recording.StableID(fixture.Pool.SiteID, fixture.Pool.ID, fixture.Run, relative)
	frozen, _ := recording.FreezePath(1, fixture.Completion.StartTime, "Asia/Shanghai", expectedID)
	target := filepath.Join(fixture.Pool.Path, frozen.RelativePath)
	info, err := os.Stat(target)
	if err != nil || !os.SameFile(fixture.Info, info) {
		t.Fatal("canonical media missing or copied", err)
	}
	if _, err := os.Stat(fixture.Completion.FilePath); !os.IsNotExist(err) {
		t.Fatal("original was not moved")
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE sites SET timezone='UTC'"); err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil || replay.ID != segment.ID {
		t.Fatal("published callback cannot be replayed after move", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("timezone update renamed history")
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_locations").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate business media location", count, err)
	}
	page, err := fixture.Service.List(ctx, fixture.Admin, recording.Query{ChannelID: fixture.Channel, Start: fixture.Completion.StartTime.Add(time.Second), End: fixture.Completion.StartTime.Add(3 * time.Second), Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != segment.ID {
		t.Fatal("partial overlap/history without current source lost", err)
	}
	if _, err := fixture.Service.List(ctx, fixture.Admin, recording.Query{ChannelID: fixture.Channel, Start: fixture.Completion.StartTime, End: fixture.Completion.StartTime.Add(32 * 24 * time.Hour), Limit: 10}); err == nil {
		t.Fatal("unbounded time range accepted")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	page, err = fixture.Service.List(ctx, fixture.Admin, recording.Query{ChannelID: fixture.Channel, Start: fixture.Completion.StartTime, End: fixture.Completion.StartTime.Add(time.Minute), Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "missing" {
		t.Fatal("missing indexed media reported ready", page, err)
	}
}

type afterMoveFailure struct{ F func() }

func (m afterMoveFailure) Move(ctx context.Context, root *os.Root, original, target string, expected os.FileInfo) error {
	if err := (recording.NativePublisher{}).Move(ctx, root, original, target, expected); err != nil {
		return err
	}
	if m.F != nil {
		m.F()
	}
	return fmt.Errorf("synthetic interruption after atomic move")
}
func TestRecordingPublishCrashRecoversFrozenIdentity(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	fixture.Service.Files = afterMoveFailure{}
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err == nil {
		t.Fatal("post-move interruption reported ready")
	}
	var state, target string
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT state,target_relative_path FROM recording_segments WHERE run_id=$1", fixture.Run).Scan(&state, &target); err != nil || state != "finalizing" {
		t.Fatal("interruption lost durable intent", state, err)
	}
	info, err := os.Stat(filepath.Join(fixture.Pool.Path, target))
	if err != nil || !os.SameFile(info, fixture.Info) {
		t.Fatal("interruption lost original media inode")
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE sites SET timezone='UTC'"); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE hook_inbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1", fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	fixture.Service.Files = nil
	if err := fixture.Service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var ready, path string
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT state,target_relative_path FROM recording_segments WHERE run_id=$1", fixture.Run).Scan(&ready, &path); err != nil || ready != "ready" || path != target {
		t.Fatal("recovery changed frozen publication", ready, path, err)
	}
}
func TestRecordingPublishTargetConflictPreservesBothFiles(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	original, _ := filepath.Rel(fixture.Pool.Path, fixture.Completion.FilePath)
	recordingID, _ := recording.StableID(fixture.Pool.SiteID, fixture.Pool.ID, fixture.Run, original)
	frozen, _ := recording.FreezePath(1, fixture.Completion.StartTime, "Asia/Shanghai", recordingID)
	target := filepath.Join(fixture.Pool.Path, frozen.RelativePath)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("existing unrelated media"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err == nil {
		t.Fatal("target collision overwrote existing media")
	}
	raw, err := os.ReadFile(target)
	if err != nil || string(raw) != "existing unrelated media" {
		t.Fatal("target collision changed other file")
	}
	actual, err := os.Stat(fixture.Completion.FilePath)
	if err != nil || !os.SameFile(actual, fixture.Info) {
		t.Fatal("target collision deleted original")
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_locations").Scan(&count); err != nil || count != 0 {
		t.Fatal("collision recorded ready location", err)
	}
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_segments WHERE run_id=$1", fixture.Run).Scan(&state); err != nil || state != "conflict" {
		t.Fatal("conflicting publication not visible as conflict", state, err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE hook_inbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1", fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil || segment.State != "ready" {
		t.Fatal("resolved collision failed to resume publication", segment, err)
	}
}

func TestRecordingQueryHTTPDoesNotExposeRawMedia(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewHandler(httpapi.Dependencies{Auth: fixture.Auth, Site: fixture.Site, Recordings: fixture.Service, PublicURL: "http://one-nvr.test:8080"})
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://one-nvr.test:8080"+path, nil)
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: fixture.Session})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/recordings?channel_id=" + string(fixture.Channel) + "&start=2026-10-03T16:04:39Z&end=2026-10-03T16:05:39Z"
	response := request(path)
	if response.Code != 200 {
		t.Fatal("metadata route unavailable", response.Code, response.Body.String())
	}
	for _, private := range []string{fixture.Pool.Path, "original_relative_path", "target_relative_path", "FilePath", "rtsp://", ".work/"} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatal("private media path exposed")
		}
	}
	response = request("/api/v1/recordings/" + string(segment.ID) + "/content")
	if response.Code != 200 || response.Header().Get("Content-Type") != "video/mp4" {
		t.Fatal("authorized content unavailable", response.Code, response.Body.String())
	}
	for _, private := range []string{fixture.Pool.Path, "original_relative_path", "target_relative_path", ".work/", "rtsp://"} {
		if strings.Contains(response.Body.String(), private) || strings.Contains(response.Header().Get("Content-Range"), private) {
			t.Fatal("private media path exposed by content response")
		}
	}
	unknown, _ := id.New()
	if response := request("/api/v1/recordings/" + string(unknown) + "/content"); response.Code != 404 {
		t.Fatal("unknown record content disclosed", response.Code)
	}
	if response := request("/api/v1/recordings?channel_id=" + string(fixture.Channel)); response.Code != 422 {
		t.Fatal("unbounded metadata query accepted", response.Code)
	}
}

func TestRecordingPublishRecoveryRequiresRegisteredRunReceipt(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, "DELETE FROM hook_inbox WHERE run_id=$1", fixture.Run); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_segments").Scan(&count); err != nil || count != 0 {
		t.Fatal("unreceipted mounted file taken over", err)
	}
	if err := fixture.Service.DescribeRun(ctx, fixture.Run); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(fixture.Pool.Path, "unknown.mp4")
	if err := os.WriteFile(unknown, []byte("unknown unrelated content"), 0600); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(filepath.Dir(fixture.Completion.FilePath), ".in-progress.mp4")
	if err := os.WriteFile(partial, []byte("open media bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var state, evidence string
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT state,time_evidence FROM recording_segments WHERE run_id=$1", fixture.Run).Scan(&state, &evidence); err != nil || state != "ready" || evidence != "recovered" {
		t.Fatal("registered closed native file was not recovered", state, evidence, err)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("unrelated file removed")
	}
	if _, err := os.Stat(partial); err != nil {
		t.Fatal("unfinished temporary file removed")
	}
}

func TestRecordingPublishRecoveryWithoutReliableTimeIsProvisional(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := fixture.DB.Pool.Exec(ctx, "DELETE FROM hook_inbox WHERE run_id=$1", fixture.Run); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(filepath.Dir(fixture.Completion.FilePath), "unverified-time.mp4")
	if err := os.Rename(fixture.Completion.FilePath, renamed); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.DescribeRun(ctx, fixture.Run); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	var state, evidence string
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT state,time_evidence FROM recording_segments WHERE run_id=$1", fixture.Run).Scan(&state, &evidence); err != nil || state != "provisional" || evidence != "provisional" {
		t.Fatal("mtime treated as reliable absolute recording time", state, evidence, err)
	}
	if _, err := os.Stat(renamed); err != nil {
		t.Fatal("provisional original moved or removed")
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_locations").Scan(&count); err != nil || count != 0 {
		t.Fatal("provisional media created ready local location", err)
	}
}

func TestRecordingPublishPoolUsageCountsVerifiedLocalOnly(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	pools := storage.New(fixture.DB, fixture.Auth, []string{filepath.Dir(fixture.Pool.Path)})
	page, err := pools.List(ctx, fixture.Admin)
	if err != nil || len(page.Items) != 1 || page.Items[0].UsedBytes == nil || *page.Items[0].UsedBytes != fixture.Info.Size() {
		t.Fatal("ready local media usage not accounted", page, err)
	}
	var target string
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT target_relative_path FROM recording_segments WHERE run_id=$1", fixture.Run).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fixture.Pool.Path, target)); err != nil {
		t.Fatal(err)
	}
	page, err = pools.List(ctx, fixture.Admin)
	if err != nil || page.Items[0].UsedBytes == nil || *page.Items[0].UsedBytes != 0 {
		t.Fatal("missing media counted as present local footage", page, err)
	}
}

// A publisher that loses ownership after the filesystem move must not commit
// ready, and the new owner can recover the same frozen inode.
type moveThenFenceLoss struct{ F func() }

func (m moveThenFenceLoss) Move(ctx context.Context, root *os.Root, original, target string, expected os.FileInfo) error {
	if err := (recording.NativePublisher{}).Move(ctx, root, original, target, expected); err != nil {
		return err
	}
	m.F()
	return nil
}
func TestRecordingPublishStaleFenceCannotCommitReady(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	f.Service.Files = moveThenFenceLoss{F: func() {
		token, _ := id.New()
		if _, err := f.DB.Pool.Exec(ctx, "UPDATE hook_inbox SET fencing_token=$2,lease_expires_at=clock_timestamp()+interval '30 seconds' WHERE id=$1", f.Inbox, token); err != nil {
			t.Error(err)
		}
	}}
	if _, err := f.Service.Publish(ctx, f.Inbox); !errors.Is(err, recording.ErrPublicationUnavailable) {
		t.Fatal("stale owner reported ready", err)
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_segments WHERE run_id=$1", f.Run).Scan(&state); err != nil || state != "finalizing" {
		t.Fatal("stale fence changed state", state, err)
	}
	if _, err := f.DB.Pool.Exec(ctx, "UPDATE hook_inbox SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", f.Inbox); err != nil {
		t.Fatal(err)
	}
	f.Service.Files = nil
	segment, err := f.Service.Publish(ctx, f.Inbox)
	if err != nil || segment.State != "ready" {
		t.Fatal("new owner could not recover moved media", segment, err)
	}
}
func TestRecordingPublishDamagedCompletionRemainsIndexed(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	// Preserve the callback's size while replacing the contents with invalid MP4.
	if err := os.WriteFile(f.Completion.FilePath, make([]byte, f.Info.Size()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Publish(ctx, f.Inbox); err == nil {
		t.Fatal("damaged MP4 published ready")
	}
	var state, inbox string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT s.state,h.state FROM recording_segments s JOIN hook_inbox h ON h.run_id=s.run_id WHERE s.run_id=$1", f.Run).Scan(&state, &inbox); err != nil || state != "damaged" || inbox != "diagnostic" {
		t.Fatal("damaged callback lacks reviewable evidence", state, inbox, err)
	}
	if _, err := os.Stat(f.Completion.FilePath); err != nil {
		t.Fatal("damaged original lost", err)
	}
	var count int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_locations").Scan(&count); err != nil || count != 0 {
		t.Fatal("damaged file has ready location", count, err)
	}
}
func TestRecordingPublishRecoveryEventuallyScansOlderRuns(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM hook_inbox WHERE run_id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.DescribeRun(ctx, f.Run); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 65; n++ {
		session, _ := id.New()
		run, _ := id.New()
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,state) VALUES($1,$2,$3,$4,'one_nvr',$1::uuid::text,'main','closed')`, session, f.Channel, f.Revision, n+2); err != nil {
			t.Fatal(err)
		}
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO recording_runs(id,site_id,channel_id,source_revision_id,stream_session_id,pool_id,work_relative_path,purpose,state) VALUES($1,$2,$3,$4,$5,$6,$7,'continuous','stopped')`, run, f.Pool.SiteID, f.Channel, f.Revision, session, f.Pool.ID, ".work/zlm/"+string(run)); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 3; n++ {
		if err := f.Service.Recover(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_segments WHERE run_id=$1", f.Run).Scan(&state); err != nil || state != "ready" {
		t.Fatal("older registered run permanently starved", state, err)
	}
}

func TestRecordingPublishCannotReadyAnInvalidatedSegment(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	f.Service.Files = moveThenFenceLoss{F: func() {
		if _, err := f.DB.Pool.Exec(ctx, "UPDATE recording_segments SET state='damaged' WHERE run_id=$1", f.Run); err != nil {
			t.Error(err)
		}
	}}
	if _, err := f.Service.Publish(ctx, f.Inbox); err == nil {
		t.Fatal("invalidated segment reported ready")
	}
	var state, inbox string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT s.state,h.state FROM recording_segments s JOIN hook_inbox h ON h.run_id=s.run_id WHERE s.run_id=$1", f.Run).Scan(&state, &inbox); err != nil || state != "damaged" || inbox == "processed" {
		t.Fatal("invalidated publication committed a processed inbox", state, inbox, err)
	}
	var count int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_locations").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalidated file has ready location", count, err)
	}
}

func TestRecordingPublishUnavailableInspectorIsRetryable(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	f.Service.Probe = probe.Runner{FFprobe: filepath.Join(t.TempDir(), "missing-ffprobe")}
	if _, err := f.Service.Publish(ctx, f.Inbox); err == nil {
		t.Fatal("unverified file published ready")
	}
	var count int
	if err := f.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_segments").Scan(&count); err != nil || count != 0 {
		t.Fatal("unavailable tool labeled media damaged", count, err)
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM hook_inbox WHERE id=$1", f.Inbox).Scan(&state); err != nil || state != "pending" {
		t.Fatal("unavailable inspector callback not retryable", state, err)
	}
}

func TestRecordingPublishRecoveryContinuesPastFileScanBudget(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM hook_inbox WHERE run_id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.DescribeRun(ctx, f.Run); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2050; n++ {
		if err := os.WriteFile(filepath.Join(filepath.Dir(f.Completion.FilePath), fmt.Sprintf(".unfinished-%04d.mp4", n)), []byte("open"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 3; n++ {
		if err := f.Service.Recover(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_segments WHERE run_id=$1", f.Run).Scan(&state); err != nil || state != "ready" {
		t.Fatal("file past scan budget permanently starved", state, err)
	}
}

func TestRecordingPublishRecoveryResumesInsideBudgetBoundaryDirectory(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := f.DB.Pool.Exec(ctx, "DELETE FROM hook_inbox WHERE run_id=$1", f.Run); err != nil {
		t.Fatal(err)
	}
	if err := f.Service.DescribeRun(ctx, f.Run); err != nil {
		t.Fatal(err)
	}
	// Four ancestors consume the first four walk entries; the 2048th is
	// the date directory containing media, not a leaf file.
	for n := 0; n < 2043; n++ {
		if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(f.Completion.FilePath)), fmt.Sprintf(".temporary-%04d.mp4", n)), []byte("open"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 3; n++ {
		if err := f.Service.Recover(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := f.DB.Pool.QueryRow(ctx, "SELECT state FROM recording_segments WHERE run_id=$1", f.Run).Scan(&state); err != nil || state != "ready" {
		t.Fatal("cursor skipped its boundary directory subtree", state, err)
	}
}

func TestRecordingQueryRequiresPlaybackGrantOnEachRequest(t *testing.T) {
	f := newPublicationFixture(t)
	ctx := context.Background()
	segment, err := f.Service.Publish(ctx, f.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.Auth.CreateUser(ctx, f.Admin, auth.CreateUserInput{Username: "playback_viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := f.Auth.Login(ctx, "playback_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	q := recording.Query{ChannelID: f.Channel, Start: f.Completion.StartTime, End: f.Completion.StartTime.Add(time.Minute), Limit: 10}
	if _, err := f.Service.List(ctx, login.Principal, q); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("ungranted historical recording disclosed", err)
	}
	if _, err := f.Service.OpenSegment(ctx, login.Principal, segment.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("ungranted media ID disclosed", err)
	}
	if err := f.Auth.SetGrants(ctx, f.Admin, user.ID, 1, []auth.Grant{{ChannelID: f.Channel, Actions: []auth.Action{auth.Playback}}}); err != nil {
		t.Fatal(err)
	}
	login, err = f.Auth.Login(ctx, "playback_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := f.Service.List(ctx, login.Principal, q); err != nil || len(page.Items) != 1 {
		t.Fatal("granted history unavailable", err)
	}
	if err := f.Auth.SetGrants(ctx, f.Admin, user.ID, 2, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.List(ctx, login.Principal, q); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("revoked session remains authorized", err)
	}
	login, err = f.Auth.Login(ctx, "playback_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.List(ctx, login.Principal, q); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("revoked history remains accessible", err)
	}
	if _, err := f.Service.OpenSegment(ctx, login.Principal, segment.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("revoked recording remains accessible", err)
	}
}
