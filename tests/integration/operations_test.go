package integration

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/capability"
	"github.com/zhigu34/one-nvr/internal/operations"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// A module's switch, its release scope, how far it is configured and whether
// its dependencies answer are four different facts. Conflating them is what let
// a shipped module read as "尚未实现" (and, before that, let an enabled module
// read as available just because an env var said yes). Each transition is
// pinned here.
func TestCapabilitiesReportIntentConfigurationAndHealth(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()

	// Frigate off: detection does not exist, so it reads disabled and its
	// actions stay gated. Cloud archive is unaffected — see below.
	off := capability.Service{Operations: operations.New(db, accounts, false, false)}
	snapshot, err := off.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := snapshot.Intelligence; c.Enabled || !c.Implemented || c.State != capability.StateDisabled || c.Reason != "feature_disabled" {
		t.Fatalf("with Frigate off detection must read disabled and implemented: %+v", c)
	}
	if err := off.Require("intelligence"); err == nil {
		t.Fatal("a disabled module must refuse its actions")
	}
	// The OpenList switch is a module switch, not a capability switch: it
	// decides whether the bundled container ships, not whether archiving works.
	// A site reaching its own NAS over WebDAV archives with the switch off, so
	// the module must not claim to be disabled.
	if c := snapshot.Archive; !c.Enabled || c.State != capability.StateNotConfigured || c.Reason != "archive_target_missing" {
		t.Fatalf("archive without a target asks for configuration regardless of the bundled container: %+v", c)
	}
	if err := off.Require("archive"); err != nil {
		t.Fatal("cloud archive is not gated on the bundled OpenList container", err)
	}

	// Switch on but nothing configured: the operator is asked to configure it,
	// not told the feature does not exist.
	ops := operations.New(db, accounts, true, true)
	caps := capability.Service{Operations: ops, FrigateEnabled: true}
	snapshot, err = caps.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := snapshot.Intelligence; !c.Enabled || !c.Implemented || c.State != capability.StateNotConfigured || c.Reason != "detection_not_configured" {
		t.Fatalf("detection without a configured channel must ask for configuration: %+v", c)
	}
	if c := snapshot.Archive; !c.Enabled || !c.Implemented || c.State != capability.StateNotConfigured || c.Reason != "archive_target_missing" {
		t.Fatalf("archive without a target must ask for configuration: %+v", c)
	}
	if err := caps.Require("intelligence"); err != nil {
		t.Fatal("an enabled module must allow its actions", err)
	}

	// Configured, with healthy dependencies: the module is genuinely working.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO detection_configs(channel_id,enabled) SELECT id,true FROM channels LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	// A real registration seals its credential; writing ciphertext by hand would
	// test the fixture rather than the module.
	targets := archive.New(db, accounts, sites.Secrets, storage.New(db, accounts, []string{t.TempDir()}))
	first, err := targets.RegisterTarget(ctx, admin.Principal, archive.TargetInput{
		Name: "cloud", BaseURL: "https://dav.example.com", RemoteRoot: "/archive",
		Username: "u", Password: "isolated-test-only", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE archive_targets SET last_check=jsonb_build_object('state','healthy','observed_at',now()) WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"frigate", "mqtt"} {
		if err := ops.Observe(ctx, operations.Observation{Name: name, State: "healthy", Reason: "probe_succeeded", ObservedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = caps.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	intelligence, archiveState := snapshot.Intelligence, snapshot.Archive
	if intelligence.State != capability.StateHealthy || archiveState.State != capability.StateHealthy {
		t.Fatalf("configured modules with answering dependencies are healthy: %+v %+v", intelligence, archiveState)
	}

	// A dependency going down must surface as running-abnormally, never as
	// "not implemented" and never as healthy.
	if err := ops.Observe(ctx, operations.Observation{Name: "mqtt", State: "unavailable", Reason: "probe_failed", ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = caps.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := snapshot.Intelligence; c.State != capability.StateUnavailable || c.Reason != "probe_failed" || !c.Implemented {
		t.Fatalf("a dead dependency must read unavailable while staying implemented: %+v", c)
	}

	// One unreachable target must not take the archive module down: other
	// WebDAV endpoints still work, which is exactly what the PRD requires.
	if _, err := db.Pool.Exec(ctx, `UPDATE archive_targets SET last_check=jsonb_build_object('state','unavailable','observed_at',now())`); err != nil {
		t.Fatal(err)
	}
	if c := mustSnapshot(t, caps).Archive; c.State != capability.StateUnavailable || c.Reason != "archive_target_unreachable" {
		t.Fatalf("the only target being unreachable is a module fault: %+v", c)
	}
	second, err := targets.RegisterTarget(ctx, admin.Principal, archive.TargetInput{
		Name: "second", BaseURL: "https://dav2.example.com", RemoteRoot: "/archive",
		Username: "u", Password: "isolated-test-only", CloudRetentionDays: 365, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE archive_targets SET last_check=jsonb_build_object('state','healthy','observed_at',now()) WHERE id=$1`, second.ID); err != nil {
		t.Fatal(err)
	}
	if c := mustSnapshot(t, caps).Archive; c.State != capability.StateHealthy {
		t.Fatalf("one working target keeps the module healthy: %+v", c)
	}

	// An unobserved dependency is not a working one: absence of evidence must
	// not be reported as availability.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM component_observations WHERE name IN ('frigate','mqtt')`); err != nil {
		t.Fatal(err)
	}
	if c := mustSnapshot(t, caps).Intelligence; c.State != capability.StateUnavailable || c.Reason != "not_observed" {
		t.Fatalf("an unobserved dependency must not read healthy: %+v", c)
	}
}

func mustSnapshot(t *testing.T, caps capability.Service) capability.Snapshot {
	t.Helper()
	snapshot, err := caps.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
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
	if _, err := ops.Audits(ctx, viewer.Principal, "", "", 10); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer read audit", err)
	}
	if _, err := ops.Components(ctx, viewer.Principal); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer read diagnostics", err)
	}
	first, err := ops.Audits(ctx, admin.Principal, "", "", 1)
	if err != nil || len(first.Items) != 1 || first.NextCursor == nil {
		t.Fatalf("audit page: %v %v", first, err)
	}
	second, err := ops.Audits(ctx, admin.Principal, "", *first.NextCursor, 1)
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

// DASH-02: the host resource endpoint must report figures that came from a real
// read, and must answer unknown rather than zero when it could not read.
func TestOperationsResourcesReadsHost(t *testing.T) {
	if _, err := os.Stat("/proc/meminfo"); err != nil {
		// The collector reads Linux /proc. On a macOS development host there is
		// nothing to read, so the behaviour under test does not exist here; the
		// real check runs on the Linux CI image and the deployed NAS.
		t.Skip("no /proc on this platform")
	}
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	ops := operations.New(db, accounts, false, false)

	// The first sample has no previous CPU counters, so CPU is unknown; memory
	// and load come from single reads and are available immediately.
	first, err := ops.Resources(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUKnown {
		t.Fatal("the first sample reported CPU although it had no interval to " +
			"divide the counters by")
	}
	if !first.MemoryKnown || first.MemoryTotal == 0 {
		t.Fatalf("memory was not read from the host: total=%d known=%v",
			first.MemoryTotal, first.MemoryKnown)
	}
	if first.MemoryUsed > first.MemoryTotal {
		t.Fatalf("used memory exceeds total: %d > %d", first.MemoryUsed, first.MemoryTotal)
	}
	if first.CPUCores < 1 {
		t.Fatalf("cpu cores = %d", first.CPUCores)
	}

	// A second sample has an interval and must produce a percentage.
	second, err := ops.Resources(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if !second.CPUKnown {
		t.Fatal("the second sample still reported CPU unknown")
	}
	if second.CPUPercent < 0 || second.CPUPercent > 100 {
		t.Fatalf("cpu percent out of range: %v", second.CPUPercent)
	}
}

// The host figures describe the machine one-nvr runs on, which is site
// infrastructure rather than business data.
func TestOperationsResourcesRequiresAdmin(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	ops := operations.New(db, accounts, false, false)
	if _, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "resources_viewer", Password: testPassword, Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	login, err := accounts.Login(ctx, "resources_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Resources(ctx, login.Principal); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer read host resources: %v", err)
	}
}

// A target that was saved and never tested is unverified, not broken.
//
// Measured on the live site: five copies were being uploaded successfully through
// a target whose capability card read 运行异常, because last_check was empty and
// "no healthy target" was read as "no working target". Saying 运行异常 sends the
// operator after a fault that has not happened.
func TestUntestedArchiveTargetIsUnverifiedNotUnavailable(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	targets := archive.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Service.Pools)
	// openlist must be enabled for Observe to accept that component name.
	ops := operations.New(fixture.DB, nil, true, true)
	caps := capability.Service{Operations: ops, FrigateEnabled: true}

	// Register a target but never test it: last_check stays null.
	if _, err := targets.RegisterTarget(ctx, fixture.Admin, archive.TargetInput{
		Name: "untested", BaseURL: "https://dav.example.com", RemoteRoot: "/archive",
		Username: "u", Password: "isolated-test-only", CloudRetentionDays: 365, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// The archive module needs a healthy OpenList/detector to be considered at
	// all; this test is about the target verdict, so make those healthy.
	for _, name := range []string{"frigate", "openlist", "mqtt"} {
		if err := ops.Observe(ctx, operations.Observation{Name: name, State: "healthy", Reason: "probe_succeeded", ObservedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if c := mustSnapshot(t, caps).Archive; c.State != capability.StateUnverified {
		t.Fatalf("an untested target reads %q (%s), want unverified", c.State, c.Reason)
	}
	// Once it has been measured and refused, the same target IS a fault.
	if _, err := fixture.DB.Pool.Exec(ctx, `UPDATE archive_targets SET last_check=jsonb_build_object('state','unavailable','observed_at',now())`); err != nil {
		t.Fatal(err)
	}
	if c := mustSnapshot(t, caps).Archive; c.State != capability.StateUnavailable {
		t.Fatalf("a tested-and-refused target reads %q, want unavailable", c.State)
	}
}
