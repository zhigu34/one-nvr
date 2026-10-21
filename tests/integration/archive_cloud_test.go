package integration

// The cloud deadline and the manual cloud delete. ARC-09.

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/archive"
)

// The deadline must be stamped when the copy is created, from the segment's real
// end rather than the upload moment.
//
// It was never stamped at all: the expiry sweep selects `cloud_expires_at <
// now()`, which is unknown for every NULL, so "cloud retention: 365 days" never
// removed anything. Measured before the fix: 1049 verified copies, zero with a
// deadline.
func TestArchiveCopyGetsACloudDeadline(t *testing.T) {
	fixture, service, dav := lifecycleFixture(t)
	ctx := context.Background()
	davServer := dav
	target, err := service.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "主归档", BaseURL: davServer.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 30, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: target.ID, Mode: archive.ModeImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	// A segment that ended two hours ago: its deadline must derive from that end,
	// not from now, so a late upload cannot gain extra retention.
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	var expires *time.Time
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT cloud_expires_at FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires == nil {
		t.Fatal("a new copy has no cloud deadline; the retention setting can never expire it")
	}
	want := segment.End.Add(30 * 24 * time.Hour)
	if diff := expires.Sub(want); diff > time.Minute || diff < -time.Minute {
		t.Fatalf("deadline %v is not the segment end + retention (%v)", expires, want)
	}
}

// 0 days means keep forever, and must stay NULL rather than becoming an instant.
func TestArchiveZeroRetentionKeepsTheCopyForever(t *testing.T) {
	fixture, service, dav := lifecycleFixture(t)
	ctx := context.Background()
	target, err := service.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "永久", BaseURL: dav.URL, RemoteRoot: "/one-nvr",
		Username: "archiver", Password: "dav-secret", CloudRetentionDays: 0, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpsertPolicy(ctx, fixture.Admin, archive.Policy{
		ChannelID: fixture.Channel, TargetID: target.ID, Mode: archive.ModeImmediate,
	}); err != nil {
		t.Fatal(err)
	}
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	var expires *time.Time
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT cloud_expires_at FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires != nil {
		t.Fatalf("0 days became a deadline (%v); it means keep forever", expires)
	}
}

// The manual delete removes the remote object and leaves the local file alone.
func TestDeleteCloudCopyRemovesTheRemoteObjectOnly(t *testing.T) {
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

	var objectKey string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT object_key FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&objectKey); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteCloudCopy(ctx, fixture.Admin, segment.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "expired" {
		t.Fatalf("the copy row still claims %q after the object was deleted", state)
	}
	// The local file is the whole point of the distinction: it must still be there.
	var local string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_segments WHERE id=$1`, segment.ID).Scan(&local); err != nil {
		t.Fatal(err)
	}
	if local != "ready" {
		t.Fatalf("deleting the cloud copy changed the local segment to %q", local)
	}
	// And the object is really gone from the target, not just marked. The test
	// server answers 204 to any DELETE, so a second delete would prove nothing;
	// a range GET is what actually asks "is there anything at this key".
	client, err := archive.NewClient(dav.URL, "/one-nvr", "archiver", "dav-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := client.GetRange(ctx, objectKey, "bytes=0-0"); err == nil {
		t.Fatal("the remote object is still readable; only the index row was updated")
	}
}

// A recording with no cloud copy reports that plainly instead of succeeding.
func TestDeleteCloudCopyWithoutACopyIsNotFound(t *testing.T) {
	fixture, service, dav := lifecycleFixture(t)
	ctx := context.Background()
	_ = registerDefaultTarget(t, fixture, service, dav)
	segment, err := fixture.Service.Publish(ctx, fixture.Inbox)
	if err != nil {
		t.Fatal(err)
	}
	err = service.DeleteCloudCopy(ctx, fixture.Admin, segment.ID)
	if err != archive.ErrNoCloudCopy {
		t.Fatalf("deleting a cloud copy that does not exist returned %v", err)
	}
}

// An unreachable target must leave the row verified: marking it expired first
// would claim the bytes are gone while they are still there.
func TestDeleteCloudCopyKeepsTheRowWhenTheTargetIsUnreachable(t *testing.T) {
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
	dav.Close()

	if err := service.DeleteCloudCopy(ctx, fixture.Admin, segment.ID); err == nil {
		t.Fatal("a delete against an unreachable target reported success")
	}
	var state string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM recording_archive_copies WHERE segment_id=$1`, segment.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "verified" {
		t.Fatalf("the copy row became %q although the object was never proven deleted", state)
	}
}
