package integration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/capability"
	"github.com/zhigu34/one-nvr/internal/operations"
)

func TestCapabilitiesSeparateIntentFromImplementation(t *testing.T) {
	db, accounts, _, _ := authFixture(t)
	ctx := context.Background()
	for _, enabled := range []bool{false, true} {
		ops := operations.New(db, accounts, enabled, enabled)
		if enabled {
			for _, n := range []string{"frigate", "mqtt", "openlist"} {
				if err := ops.Observe(ctx, operations.Observation{Name: n, State: "healthy", Reason: "probe_succeeded", ObservedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
		}
		caps := capability.Service{Operations: ops, FrigateEnabled: enabled, OpenListEnabled: enabled}
		snapshot, err := caps.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []capability.Capability{snapshot.Intelligence, snapshot.Archive} {
			if c.Enabled != enabled || c.Implemented {
				t.Fatalf("invented implementation: %v", c)
			}
			if enabled && c.State != "not_available" {
				t.Fatal("healthy container invented feature", c)
			}
			if !enabled && c.State != "disabled" {
				t.Fatal("disabled deployment mislabeled", c)
			}
		}
		if err := caps.Require("intelligence"); err == nil {
			t.Fatal("future action allowed")
		}
	}
}

func TestObservationExpiresAndOptionalDoesNotFailCore(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	ops := operations.New(db, accounts, false, false)
	for _, n := range []string{"api", "worker", "postgres", "zlm", "gateway"} {
		if err := ops.Observe(ctx, operations.Observation{Name: n, State: "healthy", Reason: "probe_succeeded", ObservedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ops.Observe(ctx, operations.Observation{Name: "frigate", State: "unavailable", Reason: "timeout", ObservedAt: time.Now()}); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("disabled module observation accepted", err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE component_observations SET expires_at=clock_timestamp()-interval '1 second' WHERE name='zlm'"); err != nil {
		t.Fatal(err)
	}
	items, err := ops.Components(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range items {
		switch c.Name {
		case "zlm":
			if c.State != "unknown" || c.Reason != "observation_expired" {
				t.Fatal("stale normal health trusted", c)
			}
		case "frigate", "mqtt", "openlist":
			if c.Enabled || c.State != "disabled" {
				t.Fatal("disabled module treated as offline", c)
			}
		default:
			if c.State != "healthy" {
				t.Fatal("optional affected core", c)
			}
		}
	}
	if err := ops.Observe(ctx, operations.Observation{Name: "zlm", State: "healthy", Reason: "http://password@host", ObservedAt: time.Now()}); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("sensitive reason accepted", err)
	}
}

func TestAuditVisibilityAndPagination(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	ops := operations.New(db, accounts, false, false)
	_, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := accounts.Login(ctx, "viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Audits(ctx, viewer.Principal, "", 10); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer read audit", err)
	}
	if _, err := ops.Components(ctx, viewer.Principal); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer read diagnostics", err)
	}
	first, err := ops.Audits(ctx, admin.Principal, "", 1)
	if err != nil || len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("audit page: %v %v", first, err)
	}
	second, err := ops.Audits(ctx, admin.Principal, *first.NextCursor, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("audit cursor: %v %v", second, err)
	}
}

func TestWorkerSamplerSkipsDisabledAdapters(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer s.Close()
	ops := operations.New(db, accounts, false, false)
	p := operations.Prober{Operations: ops, Client: operations.NewProbeClient(), Targets: map[string]operations.ProbeTarget{"frigate": {URL: s.URL, Kind: "version"}, "openlist": {URL: s.URL, Kind: "openlist"}}, MQTTAddress: "invalid.invalid:1883"}
	p.Sample(context.Background())
	if calls.Load() != 0 {
		t.Fatal("disabled module HTTP connector constructed")
	}
	items, err := ops.Components(context.Background(), admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range items {
		if (c.Name == "postgres" || c.Name == "worker") && c.State != "healthy" {
			t.Fatal("sampler failed to report real heartbeat", c)
		}
	}
}
