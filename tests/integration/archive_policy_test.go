package integration

import (
	"context"
	"errors"
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
	// A window covering the whole day: the sweep acts regardless of the wall
	// clock, and it is the window's openness — not a daily ledger slot — that
	// decides. 00:00-23:59 is "all day except the last minute".
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: target.ID, Mode: archive.ModeScheduled, Schedule: "00:00-23:59",
	}); err != nil {
		t.Fatal(err)
	}
	service.SweepSchedules(ctx)
	var uploading int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies WHERE state='uploading'`).Scan(&uploading); err != nil || uploading != 2 {
		t.Fatal("an open window must enqueue both unarchived segments", uploading, err)
	}
	// A repeat sweep enqueues nothing new: the copy rows already exist, and the
	// enqueue is idempotent per (segment, target). This replaces the old ledger
	// dedupe, which existed to stop a daily trigger from firing twice.
	service.SweepSchedules(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies`).Scan(&uploading); err != nil || uploading != 2 {
		t.Fatal("a repeat sweep must not create duplicate copies", uploading, err)
	}

	// Closing the window stops it: no new segment is picked up outside its hours.
	if _, err := fixture.Service.Publish(ctx, fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	publishContiguousSegment(t, fixture, ffmpeg, "320x180", 2*time.Second)
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE archive_policies SET schedule='00:00-00:01'`); err != nil {
		t.Fatal(err)
	}
	// Pin "now" outside the window by evaluating the window directly, since the
	// sweep reads the wall clock.
	if open, err := archive.WindowOpenAt("00:00-00:01", "Asia/Shanghai", time.Now()); err != nil || open {
		if open {
			t.Skip("the test is running in the 00:00-00:01 minute; cannot assert a closed window")
		}
	}
	service.SweepSchedules(ctx)
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies`).Scan(&uploading); err != nil || uploading != 2 {
		t.Fatal("a closed window must not enqueue", uploading, err)
	}
	// Drain the queue: both uploads verify.
	runUploadJob(t, fixture, service, ctx)
	runUploadJob(t, fixture, service, ctx)
	var verified int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM recording_archive_copies WHERE state='verified'`).Scan(&verified); err != nil || verified != 2 {
		t.Fatal("both uploads verify", verified, err)
	}
}

// Saving "scheduled" a moment before filling the time box is the one validation
// failure an operator hits by accident. It used to answer "参数无效", which does
// not name the field; these two verdicts do.
func TestScheduledPolicyNamesTheMissingTime(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	dav := webdavTestServer(t, "archiver", "dav-secret")
	service := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	target, err := service.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "Openlist", BaseURL: dav.URL, RemoteRoot: "/nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := archive.Policy{ChannelID: fixture.Channel, TargetID: target.ID, Mode: archive.ModeScheduled}

	// Empty time: its own verdict, not the generic invalid input.
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, base); !errors.Is(err, archive.ErrScheduleRequired) {
		t.Fatalf("an empty time must read as schedule_required, got %v", err)
	}
	// Whitespace is empty.
	blank := base
	blank.Schedule = "   "
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, blank); !errors.Is(err, archive.ErrScheduleRequired) {
		t.Fatalf("a blank time must read as schedule_required, got %v", err)
	}
	// Malformed specs are a different verdict, with the accepted shape.
	for _, bad := range []string{
		"2:00-06:00", "02:00-6:00", "ab:cd-06:00", "0200-0600",
		"02:00", "02:00-06:00,", "25:00-06:00", "02:00-24:00",
		// Equal ends read as either zero-length or all-day; guessing which the
		// operator meant is worse than asking.
		"02:00-02:00",
		// A bare time is the pre-window format and must not be silently accepted.
		"02:00,14:30",
	} {
		policy := base
		policy.Schedule = bad
		if _, err := service.UpsertPolicy(ctx, fixture.Admin, policy); !errors.Is(err, archive.ErrScheduleFormat) {
			t.Fatalf("%q must read as schedule_invalid, got %v", bad, err)
		}
	}
	// Well-formed windows save, including several and one that crosses midnight.
	for _, good := range []string{"02:00-06:00", "22:00-02:00", "02:00-06:00,18:00-20:00"} {
		policy := base
		policy.Schedule = good
		if _, err := service.UpsertPolicy(ctx, fixture.Admin, policy); err != nil {
			t.Fatalf("%q must save: %v", good, err)
		}
	}
}
