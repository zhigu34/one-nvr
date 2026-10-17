package integration

import (
	"context"
	"testing"

	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Device metadata describes what the hardware is, not how to reach it. It has to
// survive the same revision round-trip as every other source field, including the
// empty case: "not recorded" must stay distinguishable from a brand the operator
// typed and later cleared.
func TestSourceRevisionCarriesDeviceMetadata(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, err := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := channel.NewSources(db, accounts, sites.Secrets, policy)
	var channelID id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&channelID); err != nil {
		t.Fatal(err)
	}

	want := channel.SourceConfig{
		IP:         "192.168.33.20",
		MainPath:   "/Streaming/Channels/101",
		SubPath:    "/Streaming/Channels/102",
		Brand:      "海康威视",
		Model:      "DS-2CD2T47",
		CameraType: "bullet",
	}
	rev, err := svc.CreateDraft(ctx, admin.Principal, channelID, 1, channel.DraftInput{
		Config:         want,
		Credentials:    channel.CredentialInput{PasswordAction: "clear"},
		IdentityIntent: "replace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rev.Config.Brand != want.Brand || rev.Config.Model != want.Model || rev.Config.CameraType != want.CameraType {
		t.Fatalf("metadata lost on create: %+v", rev.Config)
	}

	// Read it back through the same repository the configure page uses.
	page, err := svc.ListRevisions(ctx, admin.Principal, channelID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var loaded *channel.SourceRevision
	for i := range page.Items {
		if page.Items[i].ID == rev.ID {
			loaded = &page.Items[i]
		}
	}
	if loaded == nil {
		t.Fatal("the revision is missing from its own list")
	}
	if loaded.Config.Brand != want.Brand || loaded.Config.Model != want.Model || loaded.Config.CameraType != want.CameraType {
		t.Fatalf("metadata lost on read: %+v", loaded.Config)
	}

	// Clearing is expressible: an empty string is a value, not a missing update.
	var version int64
	if err := db.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", channelID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	cleared, err := svc.CreateDraft(ctx, admin.Principal, channelID, version, channel.DraftInput{
		Config:         channel.SourceConfig{IP: want.IP, MainPath: want.MainPath, SubPath: want.SubPath},
		Credentials:    channel.CredentialInput{PasswordAction: "clear"},
		IdentityIntent: "replace",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Config.Brand != "" || cleared.Config.Model != "" || cleared.Config.CameraType != "" {
		t.Fatalf("metadata not cleared: %+v", cleared.Config)
	}
}

// The type list is closed: an unknown value must be refused rather than stored
// and rendered as an empty cell, which would lose the distinction between "not
// recorded" and "recorded something unrecognisable".
func TestSourceRevisionRejectsUnknownCameraType(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	policy, err := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := channel.NewSources(db, accounts, sites.Secrets, policy)
	var channelID id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&channelID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateDraft(ctx, admin.Principal, channelID, 1, channel.DraftInput{
		Config:         channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main", CameraType: "spaceship"},
		Credentials:    channel.CredentialInput{PasswordAction: "clear"},
		IdentityIntent: "replace",
	})
	if err == nil {
		t.Fatal("an unknown camera type was accepted")
	}
}
