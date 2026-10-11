package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// lifecycleFixture wires the batch-3 services exactly like run.go does: the
// immediate-upload hook and the local-cleanup gate both belong to one archive
// service bound to the fixture's database and storage.
func lifecycleFixture(t *testing.T) (publishFixture, *archive.Service, *httptest.Server) {
	t.Helper()
	fixture := newPublicationFixture(t)
	ffmpeg := mediaTestTool(t, "ffmpeg")
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: mediaTestTool(t, "ffprobe")}
	dav := webdavTestServer(t, "archiver", "dav-secret")
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	fixture.Service.OnPublished = func(ctx context.Context, tx pgx.Tx, channelID, segmentID id.ID) error {
		return service.EnqueueForPublication(ctx, tx, channelID, segmentID)
	}
	fixture.Service.ArchiveLocalGate = service.ConfirmRemoteAvailable
	return fixture, service, dav
}

func registerDefaultTarget(t *testing.T, fixture publishFixture, service *archive.Service, dav *httptest.Server) id.ID {
	t.Helper()
	target, err := service.RegisterTarget(context.Background(), fixture.Admin, archive.TargetInput{
		Name: "主归档", BaseURL: dav.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return target.ID
}

func TestArchiveLocalSafeCleanup(t *testing.T) {
	fixture, service, dav := lifecycleFixture(t)
	ctx := context.Background()
	targetID := registerDefaultTarget(t, fixture, service, dav)
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: targetID, Mode: archive.ModeImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	runUploadJob(t, fixture, service, ctx)
	// The candidate conditions are what the sweep reads, so the test forces the
	// window and the segment age instead of waiting a real hour.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE sites SET archive_local_retention_hours=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_segments SET end_at=clock_timestamp()-interval '48 hours' WHERE id=$1`, segment.ID); err != nil {
		t.Fatal(err)
	}
	// Cloud unreachable: the round refuses to clean anything (ARC-05 — the safe
	// copy must be provable right now, not assumed from a success row).
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE archive_targets SET base_url='http://127.0.0.1:1/dav' WHERE id=$1`, targetID); err != nil {
		t.Fatal(err)
	}
	fixture.Service.SweepArchiveLocal(ctx)
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, segment.ID).Scan(&state); err != nil || state != "ready" {
		t.Fatal("an unconfirmable cloud copy keeps the local file", state, err)
	}
	// Reachable again: the gate confirms the copy and the local file goes.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE archive_targets SET base_url=$2 WHERE id=$1`, targetID, dav.URL); err != nil {
		t.Fatal(err)
	}
	fixture.Service.SweepArchiveLocal(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, segment.ID).Scan(&state); err != nil || state != "deleted" {
		t.Fatal("past the window with a confirmed copy the local source is cleaned", state, err)
	}
	// M2.1 exit condition: the same recording ID stays playable from the cloud.
	if _, err := fixture.Service.OpenSegment(ctx, fixture.Admin, segment.ID); err == nil {
		t.Fatal("the local file is gone; OpenSegment must not pretend otherwise")
	}
	cloud, err := service.OpenCloudSegment(ctx, fixture.Admin, segment.ID)
	if err != nil {
		t.Fatal("the cleaned segment must stay playable from the cloud copy", err)
	}
	body, status, err := cloud.Read(ctx, 0, 99)
	if err != nil || status != 206 {
		t.Fatal("the cloud range read works", status, err)
	}
	data, _ := io.ReadAll(io.LimitReader(body, 100))
	body.Close()
	if len(data) != 100 {
		t.Fatal("the requested window length arrives", len(data))
	}
	// FILE-01: the management summary reports the segment as cloud-only.
	stats, err := fixture.Service.Summarize(ctx, fixture.Admin, recording.Query{ChannelID: fixture.Channel, Start: fixture.Completion.StartTime.Add(-time.Second), End: fixture.Completion.StartTime.Add(3 * time.Second), Limit: 10})
	if err != nil || stats.CloudOnly != 1 {
		t.Fatal("the segment is cloud-only in the summary", stats.CloudOnly, err)
	}
}

func TestArchiveCleanupGuards(t *testing.T) {
	fixture, service, dav := lifecycleFixture(t)
	ctx := context.Background()
	targetID := registerDefaultTarget(t, fixture, service, dav)
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: targetID, Mode: archive.ModeImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	runUploadJob(t, fixture, service, ctx)
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE sites SET archive_local_retention_hours=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_segments SET end_at=clock_timestamp()-interval '48 hours' WHERE id=$1`, segment.ID); err != nil {
		t.Fatal(err)
	}
	// An active playback lease outranks both the sweep and an explicit delete.
	if err := fixture.Service.RenewLease(ctx, fixture.Admin, segment.ID); err != nil {
		t.Fatal(err)
	}
	fixture.Service.SweepArchiveLocal(ctx)
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, segment.ID).Scan(&state); err != nil || state != "ready" {
		t.Fatal("a leased segment is not cleaned", state, err)
	}
	outcomes, err := fixture.Service.DeleteLocal(ctx, fixture.Admin, []id.ID{segment.ID}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Reason != "lease_held" {
		t.Fatal("the delete reports the lease", outcomes)
	}
	// Lease released through the API surface, protection engaged: protection
	// still wins over cleanup.
	if err := fixture.Service.ReleaseLease(ctx, fixture.Admin, segment.ID); err != nil {
		t.Fatal(err)
	}
	if held, err := fixture.Service.LeaseHeld(ctx, segment.ID); err != nil || held {
		t.Fatal("the release drops the lease", held, err)
	}
	if _, err := fixture.Service.SetProtection(ctx, fixture.Admin, segment.ID, true); err != nil {
		t.Fatal(err)
	}
	fixture.Service.SweepArchiveLocal(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, segment.ID).Scan(&state); err != nil || state != "ready" {
		t.Fatal("a protected segment is not cleaned", state, err)
	}
	// The unbound channel keeps its own days-based semantics (excluded here).
}

func TestArchiveCloudExpiry(t *testing.T) {
	fixture, service, dav := lifecycleFixture(t)
	ctx := context.Background()
	targetID := registerDefaultTarget(t, fixture, service, dav)
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: targetID, Mode: archive.ModeImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	runUploadJob(t, fixture, service, ctx)
	var objectKey, copyID string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT object_key,id FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&objectKey, &copyID); err != nil {
		t.Fatal(err)
	}
	// Protection: the deadline must not delete this copy.
	if _, err := fixture.Service.SetProtection(ctx, fixture.Admin, segment.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET cloud_expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, copyID); err != nil {
		t.Fatal(err)
	}
	service.SweepCloudExpiry(ctx)
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_archive_copies WHERE id=$1`, copyID).Scan(&state); err != nil || state != "verified" {
		t.Fatal("protection outranks the cloud deadline", state, err)
	}
	// Unprotected: the sweep deletes the remote object and the row expires.
	if _, err := fixture.Service.SetProtection(ctx, fixture.Admin, segment.ID, false); err != nil {
		t.Fatal(err)
	}
	service.SweepCloudExpiry(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_archive_copies WHERE id=$1`, copyID).Scan(&state); err != nil || state != "expired" {
		t.Fatal("past the deadline the remote copy is reclaimed", state, err)
	}
	// A queued upload whose deadline passed while waiting is skipped, never
	// uploaded late (ARC-09) — unless protected. The expired copy row returns
	// to uploading with a fresh deadline violation to model that wait.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE recording_archive_copies SET state='uploading',cloud_expires_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, copyID); err != nil {
		t.Fatal(err)
	}
	// The original job already finished; a fresh wait needs a fresh job row.
	if err := fixture.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		payload, _ := json.Marshal(map[string]any{"copy_id": copyID})
		_, err := jobs.Enqueue(ctx, tx, jobs.Input{Kind: "recording.archive_upload", ObjectID: id.ID(copyID), IdempotencyKey: "expiry-reskip-" + string(copyID), Payload: payload})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: fixture.DB}
	lease, err := repo.Claim(ctx, "recording.archive_upload")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, service.HandleUpload); err != nil {
		t.Fatal(err)
	}
	// The refusal is recorded on the job (terminal) and on the copy row.
	var jobState, jobCode string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,error_code FROM jobs WHERE id=$1`, lease.ID).Scan(&jobState, &jobCode); err != nil {
		t.Fatal(err)
	}
	if jobState != "failed" || jobCode != "archive_expired_skip" {
		t.Fatal("the expired upload must terminalize with its reason", jobState, jobCode)
	}
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_archive_copies WHERE id=$1`, copyID).Scan(&state); err != nil || state != "expired" {
		t.Fatal("the skipped upload expires its row", state, err)
	}
}
