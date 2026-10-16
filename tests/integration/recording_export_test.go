package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// publishContiguousSegment adds one two-second segment from the fixture's run,
// starting exactly where the first one ends, so a real stream-copy
// concatenation spans two published files.
func publishContiguousSegment(t *testing.T, fixture publishFixture, ffmpeg string, width string, offset time.Duration) recording.Segment {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(fixture.Pool.Path, ".work/zlm", string(fixture.Run), "record/one_nvr", string(fixture.Session), "2026-10-04", "2026-10-04-00-04-41-0.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size="+width+":rate=5", "-t", "2", "-c:v", "libx264", "-threads", "1", path).Run(); err != nil {
		t.Fatal("synthetic MP4 creation failed", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	completion := fixture.Completion
	completion.FilePath = path
	completion.Size = info.Size()
	completion.StartTime = fixture.Completion.StartTime.Add(offset)
	if err := fixture.Service.Accept(ctx, completion); err != nil {
		t.Fatal(err)
	}
	var inbox id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id FROM hook_inbox WHERE run_id=$1 AND id<>$2 ORDER BY id DESC LIMIT 1`, fixture.Run, fixture.Inbox).Scan(&inbox); err != nil {
		t.Fatal(err)
	}
	segment, err := fixture.Service.Publish(ctx, inbox)
	if err != nil || segment.State != "ready" {
		t.Fatal("contiguous segment failed to publish", segment, err)
	}
	return segment
}

func runExportJob(t *testing.T, fixture publishFixture, ctx context.Context) {
	t.Helper()
	repo := jobs.Repository{DB: fixture.DB}
	lease, err := repo.Claim(ctx, "recording.export")
	if err != nil {
		t.Fatal("export job not claimable", err)
	}
	if err := jobs.Execute(ctx, repo, lease, fixture.Service.HandleExport); err != nil {
		var permanent *jobs.PermanentFailure
		if !errors.As(err, &permanent) {
			t.Fatal("export job execution failed", err)
		}
	}
}

// bindFixturePool ties the fixture channel to the fixture pool: the seeded
// channels ship unbound, and CreateExport resolves the output pool from there.
func bindFixturePool(t *testing.T, fixture publishFixture) {
	t.Helper()
	if _, err := fixture.DB.Pool.Exec(context.Background(), `UPDATE channels SET storage_pool_id=$1 WHERE id=$2`, fixture.Pool.ID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	// A generous fresh capacity for every check service, so the min() the gate
	// reads is not dragged down by a fixture-seeded probe row.
	if _, err := fixture.DB.Pool.Exec(context.Background(), `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,free_bytes,filesystem_id) SELECT $1,s.service,'healthy','',clock_timestamp(),clock_timestamp()+interval '1 hour',100000000000,'fs-export-test' FROM (VALUES ('api'),('worker'),('zlm')) AS s(service) ON CONFLICT (pool_id,service) DO UPDATE SET state='healthy',expires_at=clock_timestamp()+interval '1 hour',free_bytes=100000000000`, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
}

// exportWindow covers the fixture's two-second segment; callers widen it when a
// second segment joined the run.
func exportWindowOf(fixture publishFixture, span time.Duration) (time.Time, time.Time) {
	return fixture.Completion.StartTime, fixture.Completion.StartTime.Add(span)
}

func TestRecordingExportLifecycle(t *testing.T) {
	fixture := newPublicationFixture(t)
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ctx := context.Background()
	first, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	second := publishContiguousSegment(t, fixture, ffmpeg, "320x180", 2*time.Second)
	// run.go gives the worker the ffmpeg path; mirror that here.
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: mediaTestTool(t, "ffprobe")}

	bindFixturePool(t, fixture)
	start, end := exportWindowOf(fixture, 4*time.Second)
	export, jobID, err := fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: end, GapMode: "merge", IdempotencyKey: "export-lifecycle-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if export.State != "queued" || jobID == "" {
		t.Fatal("export must start queued with a job", export.State, jobID)
	}
	// The analysis must describe exactly what the task will produce.
	analysis, err := fixture.Service.AnalyzeExport(ctx, fixture.Admin, fixture.Channel, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Groups) != 1 || analysis.Groups[0].Segments != 2 || len(analysis.Gaps) != 0 || !analysis.MediaConsistent {
		t.Fatal("one contiguous two-segment group, no gaps", analysis)
	}
	runExportJob(t, fixture, ctx)

	view, err := fixture.Service.GetExport(ctx, fixture.Admin, export.ID)
	if err != nil || view.State != "succeeded" || view.Progress != 100 {
		t.Fatal("export must finish succeeded", view, err)
	}
	if len(view.Artifacts) != 1 || view.Artifacts[0].SegmentCount != 2 {
		t.Fatal("merge mode yields exactly one two-segment artifact", view.Artifacts)
	}
	artifact := view.Artifacts[0]
	if artifact.SHA256 == "" || artifact.SizeBytes == 0 || !artifact.Start.Equal(first.Start) || !artifact.End.Equal(second.End) {
		t.Fatal("artifact evidence incomplete", artifact)
	}
	// The published file verifies against its own recorded hash.
	file, _, err := fixture.Service.OpenExportArtifact(ctx, fixture.Admin, export.ID, artifact.ID)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if hex.EncodeToString(digest.Sum(nil)) != artifact.SHA256 {
		t.Fatal("artifact hash does not match its row")
	}
	info, err := os.Stat(filepath.Join(fixture.Pool.Path, "exports", string(export.ID), "001.mp4"))
	if err != nil || info.Size() != artifact.SizeBytes {
		t.Fatal("artifact file missing or miscounted", err)
	}

	// The bundle's manifest carries EXP-03's evidence fields.
	_, files, manifestBytes, err := fixture.Service.OpenExportBundle(ctx, fixture.Admin, export.ID)
	if err != nil || len(files) != 1 {
		t.Fatal("bundle unavailable", err)
	}
	var manifest struct {
		Format  string `json:"format"`
		TaskID  id.ID  `json:"task_id"`
		Channel struct {
			No   int    `json:"no"`
			Name string `json:"name"`
		} `json:"channel"`
		CreatedBy struct {
			Name string `json:"name"`
		} `json:"created_by"`
		Artifacts []struct {
			SHA256 string `json:"sha256"`
		} `json:"artifacts"`
		Sources []struct {
			SegmentID        id.ID `json:"segment_id"`
			SourceRevisionID id.ID `json:"source_revision_id"`
		} `json:"sources"`
		Gaps []json.RawMessage `json:"gaps"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Format != "one-nvr-export-manifest/1" || manifest.TaskID != export.ID || manifest.Channel.No != 1 || manifest.Channel.Name == "" || manifest.CreatedBy.Name == "" {
		t.Fatal("manifest header incomplete", string(manifestBytes))
	}
	if len(manifest.Artifacts) != 1 || manifest.Artifacts[0].SHA256 != artifact.SHA256 || len(manifest.Sources) != 2 {
		t.Fatal("manifest must list the artifact hash and both pinned sources")
	}
	// The ZIP contains the manifest and the artifact under its download name.
	var zipBuffer bytes.Buffer
	if err := recording.WriteExportZip(&zipBuffer, manifestBytes, files); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(zipBuffer.Bytes()), int64(zipBuffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 2 || reader.File[0].Name != "manifest.json" || reader.File[1].Name != artifact.FileName {
		names := []string{}
		for _, f := range reader.File {
			names = append(names, f.Name)
		}
		t.Fatal("zip layout wrong", names)
	}
	// A recorded download is audited with actor and artifact.
	if err := fixture.Service.AuditExportDownload(ctx, fixture.Admin.UserID, recording.ExportDownload{ExportID: export.ID, ArtifactID: artifact.ID}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='recording.export_downloaded' AND object_id=$1`, export.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("the download must be audited", count, err)
	}
	// A replayed request with the same key but different parameters is refused
	// instead of silently creating a second task.
	_, _, err = fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: start.Add(2 * time.Second), GapMode: "merge", IdempotencyKey: "export-lifecycle-1",
	})
	var rejected *fault.Error
	if !errors.As(err, &rejected) || rejected.Code != "idempotency_conflict" {
		t.Fatal("idempotency key reuse must be refused", err)
	}
}

func TestRecordingExportPinsSourceAgainstDeletion(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	first, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	bindFixturePool(t, fixture)
	start, end := exportWindowOf(fixture, 2*time.Second)
	export, _, err := fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: end, GapMode: "merge", IdempotencyKey: "export-pin-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The queued export holds the segment: deletion waits, with a reason.
	outcomes, err := fixture.Service.DeleteLocal(ctx, fixture.Admin, []id.ID{first.ID}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Reason != "pinned_by_export" {
		t.Fatal("pinned segment must be undeletable while the export is active", outcomes)
	}
	// Finishing the task releases the reference.
	fixture.Service.Probe = probe.Runner{FFmpeg: mediaTestTool(t, "ffmpeg"), FFprobe: mediaTestTool(t, "ffprobe")}
	runExportJob(t, fixture, ctx)
	view, err := fixture.Service.GetExport(ctx, fixture.Admin, export.ID)
	if err != nil || view.State != "succeeded" {
		t.Fatal("export must finish", view, err)
	}
	outcomes, err = fixture.Service.DeleteLocal(ctx, fixture.Admin, []id.ID{first.ID}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].State != "deleted" {
		t.Fatal("a terminal export no longer holds the segment", outcomes)
	}
}

func TestRecordingExportRefusesIncompatibleMedia(t *testing.T) {
	fixture := newPublicationFixture(t)
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ctx := context.Background()
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	// A contiguous segment encoded with different parameters.
	publishContiguousSegment(t, fixture, ffmpeg, "640x360", 2*time.Second)
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: mediaTestTool(t, "ffprobe")}
	bindFixturePool(t, fixture)
	start, end := exportWindowOf(fixture, 4*time.Second)
	export, _, err := fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: end, GapMode: "merge", IdempotencyKey: "export-incompatible-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	runExportJob(t, fixture, ctx)
	view, err := fixture.Service.GetExport(ctx, fixture.Admin, export.ID)
	if err != nil || view.State != "failed" || view.ErrorCode == nil || *view.ErrorCode != "media_incompatible" {
		t.Fatal("incompatible parameters must fail the task, not transcode silently", view, err)
	}
	if len(view.Artifacts) != 0 {
		t.Fatal("a failed export has no artifacts", view.Artifacts)
	}
	if _, _, _, err := fixture.Service.OpenExportBundle(ctx, fixture.Admin, export.ID); err == nil {
		t.Fatal("a failed export is not downloadable")
	}
}

func TestRecordingExportRequiresCapacityEvidence(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	// Bind the pool without the capacity seed: this test controls the checks.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE channels SET storage_pool_id=$1 WHERE id=$2`, fixture.Pool.ID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `DELETE FROM storage_pool_checks WHERE pool_id=$1`, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
	start, end := exportWindowOf(fixture, 2*time.Second)
	_, _, err := fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: end, GapMode: "merge", IdempotencyKey: "export-capacity-1",
	})
	var rejected *fault.Error
	if !errors.As(err, &rejected) || rejected.Code != "capacity_unknown" {
		t.Fatal("without fresh capacity evidence creation is refused", err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,free_bytes,filesystem_id) VALUES($1,'api','healthy','',clock_timestamp(),clock_timestamp()+interval '30 seconds',10,'fs-test') ON CONFLICT (pool_id,service) DO UPDATE SET state='healthy',expires_at=clock_timestamp()+interval '30 seconds',free_bytes=10`, fixture.Pool.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: end, GapMode: "merge", IdempotencyKey: "export-capacity-2",
	})
	if !errors.As(err, &rejected) || rejected.Code != "low_space" {
		t.Fatal("ten bytes cannot hold an export", err)
	}
}

func TestRecordingWindowProtection(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	first, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	// Protection is an administrator operation: a viewer principal is refused.
	start, end := exportWindowOf(fixture, 1*time.Second)
	if _, err := fixture.Service.ProtectWindow(ctx, auth.Principal{Role: "viewer"}, fixture.Channel, start, end, true); err == nil {
		t.Fatal("a non-admin must not protect evidence")
	}
	updated, err := fixture.Service.ProtectWindow(ctx, fixture.Admin, fixture.Channel, start, end, true)
	if err != nil || updated != 1 {
		t.Fatal("exactly the intersecting segment is protected", updated, err)
	}
	outcomes, err := fixture.Service.DeleteLocal(ctx, fixture.Admin, []id.ID{first.ID}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].State != "protected" {
		t.Fatal("a window-protected segment is undeletable", outcomes)
	}
	// Releasing the window releases exactly what it marked.
	if _, err := fixture.Service.ProtectWindow(ctx, fixture.Admin, fixture.Channel, start, end, false); err != nil {
		t.Fatal(err)
	}
	var still bool
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT protected FROM recording_segments WHERE id=$1`, first.ID).Scan(&still); err != nil || still {
		t.Fatal("window release must clear the flag", err)
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action IN ('recording.window_protected','recording.window_unprotected')`).Scan(&count); err != nil || count != 2 {
		t.Fatal("both directions are audited", count, err)
	}
}

func TestRecordingExportArtifactExpiry(t *testing.T) {
	fixture := newPublicationFixture(t)
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ctx := context.Background()
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: mediaTestTool(t, "ffprobe")}
	bindFixturePool(t, fixture)
	start, end := exportWindowOf(fixture, 2*time.Second)
	protected, _, err := fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: end, GapMode: "merge", IdempotencyKey: "export-expiry-kept",
	})
	if err != nil {
		t.Fatal(err)
	}
	plain, _, err := fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: end, GapMode: "split", IdempotencyKey: "export-expiry-gone",
	})
	if err != nil {
		t.Fatal(err)
	}
	runExportJob(t, fixture, ctx)
	runExportJob(t, fixture, ctx)
	keptView, err := fixture.Service.GetExport(ctx, fixture.Admin, protected.ID)
	if err != nil || keptView.State != "succeeded" || len(keptView.Artifacts) != 1 {
		t.Fatal("kept export must finish", keptView, err)
	}
	goneView, err := fixture.Service.GetExport(ctx, fixture.Admin, plain.ID)
	if err != nil || len(goneView.Artifacts) != 1 {
		t.Fatal("plain export must finish", goneView, err)
	}
	// Permanent protection survives expiry; the plain artifact does not.
	if _, err := fixture.Service.SetExportArtifactProtection(ctx, fixture.Admin, protected.ID, keptView.Artifacts[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_export_artifacts SET expires_at=clock_timestamp()-interval '1 second' WHERE export_id IN ($1,$2)`, protected.ID, plain.ID); err != nil {
		t.Fatal(err)
	}
	fixture.Service.SweepExportArtifacts(ctx)
	if _, err := os.Stat(filepath.Join(fixture.Pool.Path, "exports", string(plain.ID), "001.mp4")); !os.IsNotExist(err) {
		t.Fatal("expired plain artifact removed", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.Pool.Path, "exports", string(protected.ID), "001.mp4")); err != nil {
		t.Fatal("protected artifact survives the sweep", err)
	}
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_export_artifacts WHERE id=$1`, goneView.Artifacts[0].ID).Scan(&state); err != nil || state != "expired" {
		t.Fatal("expired artifact keeps its row", state, err)
	}
	if _, _, err := fixture.Service.OpenExportArtifact(ctx, fixture.Admin, plain.ID, goneView.Artifacts[0].ID); err == nil {
		t.Fatal("an expired artifact is not downloadable")
	}
}

func TestRecordingExportPermissionAndAnalysisDenials(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	// A viewer without channel grants: analyze and create are both invisible.
	user, err := fixture.Auth.CreateUser(ctx, fixture.Admin, auth.CreateUserInput{Username: "export_viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := fixture.Auth.Login(ctx, "export_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	start, end := exportWindowOf(fixture, 2*time.Second)
	if _, err := fixture.Service.AnalyzeExport(ctx, login.Principal, fixture.Channel, start, end); err == nil {
		t.Fatal("a viewer without the export grant cannot analyze")
	}
	if _, _, err := fixture.Service.CreateExport(ctx, login.Principal, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: end, GapMode: "merge", IdempotencyKey: "export-viewer-1",
	}); err == nil {
		t.Fatal("a viewer without the export grant cannot export")
	}
	// The export grant alone authorizes the whole surface.
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO channel_grants(user_id,channel_id,export) VALUES($1,$2,true) ON CONFLICT (user_id,channel_id) DO UPDATE SET export=true`, user.ID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Service.AnalyzeExport(ctx, login.Principal, fixture.Channel, start, end); err != nil {
		t.Fatal("the export grant authorizes analysis", err)
	}
	// An empty window is refused before any task exists.
	bindFixturePool(t, fixture)
	_, _, err = fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start.Add(24 * time.Hour), End: start.Add(24 * time.Hour).Add(time.Minute), GapMode: "merge", IdempotencyKey: "export-empty-1",
	})
	var rejected *fault.Error
	if !errors.As(err, &rejected) || rejected.Code != "export_no_source" {
		t.Fatal("an empty window is refused", err)
	}
	// Over the thirty-minute ceiling is refused by the same guard.
	_, _, err = fixture.Service.CreateExport(ctx, fixture.Admin, recording.ExportCreateInput{
		ChannelID: fixture.Channel, Start: start, End: start.Add(31 * time.Minute), GapMode: "merge", IdempotencyKey: "export-long-1",
	})
	if !errors.As(err, &rejected) || rejected.Code != "window_too_long" {
		t.Fatal("over the ceiling is refused", err)
	}
}
