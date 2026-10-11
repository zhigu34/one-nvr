package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
)

// pgxTx is the transaction handle the publication hook receives.
type pgxTx = pgx.Tx

func TestArchiveImmediatePolicy(t *testing.T) {
	fixture := newPublicationFixture(t)
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ctx := context.Background()
	dav := webdavTestServer(t, "archiver", "dav-secret")
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: mediaTestTool(t, "ffprobe")}
	// Mirror run.go: the hook is wired before any publishing happens.
	fixture.Service.OnPublished = func(ctx context.Context, tx pgxTx, channelID, segmentID id.ID) error {
		return service.EnqueueForPublication(ctx, tx, channelID, segmentID)
	}
	first, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	var copies int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies WHERE segment_id=$1`, first.ID).Scan(&copies); err != nil || copies != 0 {
		t.Fatal("without a policy nothing is enqueued", copies, err)
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
	// The next publication carries its upload task in the same transaction.
	second := publishContiguousSegment(t, fixture, ffmpeg, "320x180", 2*time.Second)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies WHERE segment_id=$1 AND state='uploading'`, second.ID).Scan(&copies); err != nil || copies != 1 {
		t.Fatal("immediate policy must pin a copy at publication", copies, err)
	}
	runUploadJob(t, fixture, service, ctx)
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_archive_copies WHERE segment_id=$1`, second.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "verified" {
		t.Fatal("the auto-enqueued upload must verify", state)
	}
	// Switching back to manual stops auto-enqueueing for later segments; the
	// submitted tasks are untouched (ARC-08).
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: target.ID, Mode: archive.ModeManual,
	}); err != nil {
		t.Fatal(err)
	}
	var policies int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies`).Scan(&policies); err != nil || policies != 1 {
		t.Fatal("mode switch must not touch submitted tasks", policies, err)
	}
}

func TestArchiveScheduledSweep(t *testing.T) {
	fixture := newPublicationFixture(t)
	ffmpeg := mediaTestTool(t, "ffmpeg")
	ctx := context.Background()
	dav := webdavTestServer(t, "archiver", "dav-secret")
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	fixture.Service.Probe = probe.Runner{FFmpeg: ffmpeg, FFprobe: mediaTestTool(t, "ffprobe")}
	fixture.Service.OnPublished = func(ctx context.Context, tx pgxTx, channelID, segmentID id.ID) error {
		return service.EnqueueForPublication(ctx, tx, channelID, segmentID)
	}
	// Two ready segments exist before the policy: the sweep must catch up.
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	publishContiguousSegment(t, fixture, ffmpeg, "320x180", 2*time.Second)
	target, err := service.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "主归档", BaseURL: dav.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 00:00 is always due, so the sweep fires regardless of the wall clock.
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: target.ID, Mode: archive.ModeScheduled, Schedule: "00:00",
	}); err != nil {
		t.Fatal(err)
	}
	service.SweepSchedules(ctx)
	var uploading int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies WHERE state='uploading'`).Scan(&uploading); err != nil || uploading != 2 {
		t.Fatal("the sweep must enqueue both unarchived segments", uploading, err)
	}
	var ledger int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM archive_schedule_runs`).Scan(&ledger); err != nil || ledger != 1 {
		t.Fatal("one ledger row per fire", ledger, err)
	}
	// A repeat sweep is fenced by the UNIQUE ledger row.
	service.SweepSchedules(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM archive_schedule_runs`).Scan(&ledger); err != nil || ledger != 1 {
		t.Fatal("the same fire must not enqueue twice", ledger, err)
	}
	// Drain the queue: both uploads verify.
	runUploadJob(t, fixture, service, ctx)
	runUploadJob(t, fixture, service, ctx)
	var verified int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies WHERE state='verified'`).Scan(&verified); err != nil || verified != 2 {
		t.Fatal("both uploads verify", verified, err)
	}
}
