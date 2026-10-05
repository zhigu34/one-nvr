package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

func TestSourceStatusExpiresEvidenceAndIgnoresDraftTests(t *testing.T) {
	f, _, _, draft := testedSource(t)
	ctx := context.Background()
	for _, revision := range []id.ID{f.Revision, draft} {
		observation, _ := id.New()
		expiry := "clock_timestamp()-interval '1 second'"
		observed := "clock_timestamp()-interval '1 minute'"
		if revision == draft {
			expiry = "clock_timestamp()+interval '30 seconds'"
			observed = "clock_timestamp()"
		}
		if _, err := f.DB.Pool.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,kind,state,reason_code,observed_at,expires_at) VALUES($1,$2,$3,'main','healthy','controlled',`+observed+`,`+expiry+`)`, observation, f.Channel, revision); err != nil {
			t.Fatal(err)
		}
	}
	status, err := f.Service.Sources.GetStatus(ctx, f.Admin, f.Channel)
	if err != nil || status.Main.State != "unknown" || status.CurrentRevisionID == nil || *status.CurrentRevisionID != f.Revision {
		t.Fatal("stale/current source truth incorrect", status, err)
	}
	policy, err := f.Service.Sources.GetPolicy(ctx, f.Admin, f.Channel)
	if err != nil || policy.Version != status.Version {
		t.Fatal("policy exposes a different optimistic channel version", policy.Version, status.Version, err)
	}
	if err := f.Auth.SetGrants(ctx, f.Admin, f.Admin.UserID, 1, []auth.Grant{{ChannelID: f.Channel, Actions: []auth.Action{auth.Playback}}}); err != nil {
		t.Fatal(err)
	}
	login, err := f.Auth.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Sources.GetStatus(ctx, login.Principal, f.Channel); err != nil {
		t.Fatal("status denied to playback-only grant", err)
	}
	var other id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=2").Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Service.Sources.GetStatus(ctx, login.Principal, other); !errors.Is(err, auth.ErrNotFound) {
		t.Fatal("ungranted channel status exposed", err)
	}
}
