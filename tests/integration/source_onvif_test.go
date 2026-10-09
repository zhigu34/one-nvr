package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

func onvifFixture(t *testing.T, allowed string) (*database.DB, *channel.SourceService, *auth.Service, auth.Principal, id.ID) {
	t.Helper()
	db, accounts, sites, admin := authFixture(t)
	policy, err := channel.ParseNetworkPolicy(allowed, nil)
	if err != nil {
		t.Fatal(err)
	}
	var channelID id.ID
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM channels WHERE channel_no=1").Scan(&channelID); err != nil {
		t.Fatal(err)
	}
	return db, channel.NewSources(db, accounts, sites.Secrets, policy), accounts, admin.Principal, channelID
}

func probeFault(t *testing.T, err error) *fault.Error {
	t.Helper()
	var failure *fault.Error
	if !errors.As(err, &failure) {
		t.Fatalf("expected a public fault, got %v", err)
	}
	return failure
}

// Probing one camera is an outbound connection to an operator-supplied address,
// so it must obey the same boundary and permission rules as saving a source.
func TestOnvifProbeEnforcesPermissionAndAddressBoundary(t *testing.T) {
	db, svc, accounts, admin, channelID := onvifFixture(t, "192.168.33.0/24")
	ctx := context.Background()
	port := 8000
	attempt := func(ip string) error {
		t.Helper()
		_, err := svc.ProbeONVIF(ctx, admin, channelID, channel.ProbeInput{IP: ip, ONVIFPort: &port, Username: "operator", Password: "fixture-password"})
		return err
	}
	for _, test := range []struct {
		name   string
		ip     string
		status int
		code   string
	}{
		{"outside every allowed range", "10.1.2.3", 422, "camera_address_denied"},
		{"loopback", "127.0.0.1", 422, "camera_address_denied"},
		{"network address itself", "192.168.33.0", 422, "camera_address_denied"},
		{"broadcast address", "192.168.33.255", 422, "camera_address_denied"},
		{"link local", "169.254.10.10", 422, "camera_address_denied"},
		{"not an address", "camera.example.com", 422, "source_config_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := probeFault(t, attempt(test.ip))
			if failure.Status != test.status || failure.Code != test.code {
				t.Fatalf("got %d %s, want %d %s", failure.Status, failure.Code, test.status, test.code)
			}
		})
	}
	t.Run("invalid input never reaches the network", func(t *testing.T) {
		bad := 0
		if _, err := svc.ProbeONVIF(ctx, admin, channelID, channel.ProbeInput{IP: "192.168.33.20", ONVIFPort: &bad}); !errors.Is(err, auth.ErrInvalid) {
			t.Fatal("an out-of-range ONVIF port was accepted", err)
		}
		if _, err := svc.ProbeONVIF(ctx, admin, channelID, channel.ProbeInput{IP: "192.168.33.20", Username: "user\nname"}); !errors.Is(err, auth.ErrInvalid) {
			t.Fatal("a control character in the username was accepted", err)
		}
	})
	// A refusal is not a camera event: nothing was contacted, so nothing is
	// recorded against the channel.
	var recorded int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action='onvif.probed'").Scan(&recorded); err != nil || recorded != 0 {
		t.Fatalf("refusals were recorded as camera events: %d %v", recorded, err)
	}
	// A deployment without ONE_NVR_CAMERA_CIDRS refuses sources, and must refuse
	// probing for the same reason rather than dialing anywhere.
	_, unconfigured, _, unconfiguredAdmin, otherChannel := onvifFixture(t, "")
	if failure := probeFault(t, func() error {
		_, err := unconfigured.ProbeONVIF(ctx, unconfiguredAdmin, otherChannel, channel.ProbeInput{IP: "192.168.33.20"})
		return err
	}()); failure.Status != 422 || failure.Code != "camera_network_not_configured" {
		t.Fatalf("got %d %s", failure.Status, failure.Code)
	}
	// Probing needs Configure on the channel being configured, exactly like
	// saving a revision does.
	if err := accounts.SetGrants(ctx, admin, admin.UserID, 1, []auth.Grant{{ChannelID: channelID, Actions: []auth.Action{auth.Playback}}}); err != nil {
		t.Fatal(err)
	}
	login, err := accounts.Login(ctx, "admin", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	// A visible channel without Configure is denied, exactly like saving a
	// revision on it would be.
	if _, err := svc.ProbeONVIF(ctx, login.Principal, channelID, channel.ProbeInput{IP: "192.168.33.20"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("a playback-only operator probed a channel: %v", err)
	}
	// A channel the operator cannot see stays hidden rather than forbidden.
	if _, err := svc.ProbeONVIF(ctx, login.Principal, hiddenChannel(t, db), channel.ProbeInput{IP: "192.168.33.20"}); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("an invisible channel was disclosed: %v", err)
	}
}

func hiddenChannel(t *testing.T, db *database.DB) id.ID {
	t.Helper()
	var other id.ID
	if err := db.Pool.QueryRow(context.Background(), "SELECT id FROM channels WHERE channel_no=2").Scan(&other); err != nil {
		t.Fatal(err)
	}
	return other
}

// The audit trail explains a later success, so a failed attempt is recorded too —
// and neither the entry nor the returned fault may carry the credentials.
func TestOnvifProbeRecordsTheAttemptWithoutCredentials(t *testing.T) {
	db, svc, _, admin, channelID := onvifFixture(t, "198.51.100.0/24")
	ctx := context.Background()
	port := 80
	const username = "onvif-fixture-operator"
	const password = "onvif-fixture-password-marker"
	// Reserved documentation range: nothing can answer, so the probe must report
	// a transport failure rather than inventing a device.
	_, err := svc.ProbeONVIF(ctx, admin, channelID, channel.ProbeInput{IP: "198.51.100.5", ONVIFPort: &port, Username: username, Password: password})
	if err == nil {
		t.Fatal("a probe of an unused address reported success")
	}
	failure := probeFault(t, err)
	if failure.Status != 503 || !strings.HasPrefix(failure.Code, "onvif_") {
		t.Fatalf("got %d %s, want a retryable onvif_* fault", failure.Status, failure.Code)
	}
	var details string
	if err := db.Pool.QueryRow(ctx, "SELECT details::text FROM audit_logs WHERE action='onvif.probed' AND object_id=$1 ORDER BY created_at DESC LIMIT 1", channelID).Scan(&details); err != nil {
		t.Fatalf("the attempt was not recorded: %v", err)
	}
	if !strings.Contains(details, `"198.51.100.5"`) || !strings.Contains(details, failure.Code) {
		t.Fatalf("the entry does not describe the attempt: %s", details)
	}
	if strings.Contains(details, password) || strings.Contains(details, username) {
		t.Fatalf("the entry leaked credentials: %s", details)
	}
	if !strings.Contains(failure.Message, "ONVIF") && !strings.Contains(failure.Message, "摄像头") {
		t.Fatalf("the fault does not explain itself: %s", failure.Message)
	}
}
