package integration

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/site"
)

func TestExpansionPreservesIDsAndGrants(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	sites.Auth = accounts
	ctx := context.Background()
	slots := &channel.Service{DB: db, Auth: accounts}
	before, err := slots.List(ctx, admin.Principal, "", 100)
	if err != nil || len(before.Items) != 16 {
		t.Fatalf("initial slots: %v %v", before, err)
	}
	other, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "otheradmin", Password: testPassword, Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := sites.Expand(ctx, admin.Principal, 1); results <- err }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, auth.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("expansion commits=%d conflicts=%d", success, conflict)
	}
	after, err := slots.List(ctx, admin.Principal, "", 100)
	if err != nil || len(after.Items) != 32 {
		t.Fatalf("expanded slots: %v %v", after, err)
	}
	for i, c := range before.Items {
		if after.Items[i].ID != c.ID || after.Items[i].ChannelNo != c.ChannelNo {
			t.Fatal("existing channel identity changed")
		}
	}
	var grants, policies int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM channel_grants WHERE user_id=$1", other.ID).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("implicit grants: %d %v", grants, err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_policies WHERE NOT event_recording AND mode='none'").Scan(&policies); err != nil || policies != 32 {
		t.Fatalf("new slots recording defaults: %d %v", policies, err)
	}
	if _, err := sites.Expand(ctx, admin.Principal, 2); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("repeated expansion accepted", err)
	}
}

func TestSiteTimezoneAndVersion(t *testing.T) {
	_, accounts, sites, admin := authFixture(t)
	sites.Auth = accounts
	ctx := context.Background()
	s, err := sites.Current(ctx, admin.Principal)
	if err != nil || s.Timezone != "Asia/Shanghai" {
		t.Fatalf("initial site: %v %v", s, err)
	}
	z := "Asia/Tokyo"
	next, err := sites.Update(ctx, admin.Principal, 1, site.UpdateInput{Timezone: &z})
	if err != nil || next.Timezone != z || next.Version != 2 {
		t.Fatalf("timezone update: %v %v", next, err)
	}
	if _, err := sites.Update(ctx, admin.Principal, 1, site.UpdateInput{Timezone: &z}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	for _, bad := range []string{"Local", "../etc/passwd", "Invalid/Zone"} {
		if _, err := sites.Update(ctx, admin.Principal, 2, site.UpdateInput{Timezone: &bad}); !errors.Is(err, auth.ErrInvalid) {
			t.Fatal("bad timezone accepted", bad, err)
		}
	}
	if _, err := sites.Update(ctx, admin.Principal, 2, site.UpdateInput{}); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("empty update accepted", err)
	}
	current, err := sites.Current(ctx, admin.Principal)
	if err != nil || current.Version != 2 || current.Timezone != z {
		t.Fatal("failed update mutated state", err)
	}
	seen := false
	for _, zone := range site.Timezones() {
		if zone.ID == z && zone.CurrentTime != "" && zone.Label != "" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("timezone choices missing preview")
	}
}

func TestChannelScopeRenameAndRevocation(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	slots := &channel.Service{DB: db, Auth: accounts}
	all, err := slots.List(ctx, admin.Principal, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	operator, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "operator", Password: testPassword, Role: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if err := accounts.SetGrants(ctx, admin.Principal, operator.ID, 1, []auth.Grant{{ChannelID: all.Items[0].ID, Actions: []auth.Action{auth.Live, auth.Configure}}}); err != nil {
		t.Fatal(err)
	}
	login, err := accounts.Login(ctx, "operator", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := slots.List(ctx, login.Principal, "", 100)
	if err != nil || len(visible.Items) != 1 {
		t.Fatal("channel scope leaked", visible, err)
	}
	updated, err := slots.Update(ctx, login.Principal, all.Items[0].ID, 1, channel.UpdateInput{ChannelName: "门口"})
	if err != nil || updated.ID != all.Items[0].ID || updated.ChannelNo != 1 || updated.Version != 2 {
		t.Fatalf("rename changed slot: %v %v", updated, err)
	}
	if _, err := slots.Update(ctx, login.Principal, all.Items[1].ID, 1, channel.UpdateInput{ChannelName: "other"}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("invisible channel leaked", err)
	}
	unknown, _ := id.New()
	if _, err := slots.List(ctx, login.Principal, unknown, 10); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("unknown cursor accepted", err)
	}
	if err := accounts.SetGrants(ctx, admin.Principal, operator.ID, 2, []auth.Grant{{ChannelID: all.Items[0].ID, Actions: []auth.Action{auth.Live}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := slots.Update(ctx, login.Principal, all.Items[0].ID, 2, channel.UpdateInput{ChannelName: "revoked"}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("revoked session updated slot", err)
	}
}
