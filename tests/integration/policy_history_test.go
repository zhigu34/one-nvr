package integration

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/reliability"
)

// Policy history and the reconciler that turns it into obligations.
//
// The unit tests cover the derivation rules. These cover the wiring that a unit
// test cannot see: that a real policy change through the real job loop writes a
// version row, and that the reconciler reads the facts back into an obligation
// timeline — including for the disabled and unconfigured channels the sampler
// never visits.

// configureChannel activates the fixture's draft revision, which the publication
// fixture deliberately leaves inactive.
func configureChannel(t *testing.T, f publishFixture) {
	t.Helper()
	if _, err := f.DB.Pool.Exec(context.Background(),
		"UPDATE channels SET current_revision_id=$2,enabled=true WHERE id=$1", f.Channel, f.Revision); err != nil {
		t.Fatal(err)
	}
}

// policyFixture wires the sources service the policy-change path needs. The
// publication fixture builds a recording service without one, because publishing
// a completed segment and switching a source are different concerns.
func policyFixture(t *testing.T) publishFixture {
	t.Helper()
	fixture := newPublicationFixture(t)
	network, err := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Service.Sources = channel.NewSources(fixture.DB, fixture.Auth, fixture.Site.Secrets, network)
	return fixture
}

// seedPoolEvidence gives the fixture's pool fresh health checks, which the
// policy path requires before it will start continuous recording. Without this
// the change is refused before it reaches the code under test.
func seedPoolEvidence(t *testing.T, f publishFixture) {
	t.Helper()
	for _, peer := range []string{"api", "worker", "zlm"} {
		if _, err := f.DB.Pool.Exec(context.Background(), `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,free_bytes,total_bytes,filesystem_id) VALUES($1,$2,'healthy','controlled',clock_timestamp(),clock_timestamp()+interval '3 minutes',1099511627776,2199023255552,'controlled-filesystem') ON CONFLICT(pool_id,service) DO UPDATE SET state='healthy',expires_at=EXCLUDED.expires_at,free_bytes=EXCLUDED.free_bytes,filesystem_id=EXCLUDED.filesystem_id`, f.Pool.ID, peer); err != nil {
			t.Fatal(err)
		}
	}
}

// policyHistoryCount counts the versions recorded for a channel.
func policyHistoryCount(t *testing.T, f publishFixture, channelID id.ID) int {
	t.Helper()
	var count int
	if err := f.DB.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM recording_policy_versions WHERE channel_id=$1", channelID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A policy change through the real job loop records a version, so the obligation
// denominator has a timeline to be derived from.
//
// This uses the media-capable fixture because a policy change to continuous has
// to pass the pool-evidence and capacity gates before it reaches the code under
// test; against a fixture with no media it is refused earlier and proves nothing.
func TestPolicyChangeRecordsAVersion(t *testing.T) {
	fixture, _, _, _ := testedSource(t)
	ctx := context.Background()
	// Start from none so the change is a real mode transition.
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_policies SET mode='none' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	before := policyHistoryCount(t, fixture, fixture.Channel)
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET version=version+1 WHERE id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1", fixture.Channel).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Service.Sources.SetPolicy(ctx, fixture.Admin, fixture.Channel, version, "continuous", "policy-history-on"); err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: fixture.DB}
	lease, err := repo.Claim(ctx, "source.policy_apply")
	if err != nil {
		t.Fatal(err)
	}
	// The change may be rolled back by a media gate; what this test asserts is
	// the version row, which is written on the commit path either way.
	_ = jobs.Execute(ctx, repo, lease, fixture.Service.ExecuteSourceChange)

	if got := policyHistoryCount(t, fixture, fixture.Channel); got != before+1 {
		t.Fatalf("a policy change moved the version count from %d to %d, want one more; "+
			"the timeline REL-03 derives the denominator from would otherwise miss it", before, got)
	}
	var mode, reason string
	var effective time.Time
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT mode,reason,effective_at FROM recording_policy_versions WHERE channel_id=$1 ORDER BY version DESC LIMIT 1", fixture.Channel).
		Scan(&mode, &reason, &effective); err != nil {
		t.Fatal(err)
	}
	if mode != "continuous" {
		t.Fatalf("recorded mode = %q, want continuous", mode)
	}
	if reason == "backfill" {
		t.Fatalf("a change that was actually observed was labelled %q; only the "+
			"migration's guess may be called backfill", reason)
	}
	if effective.IsZero() {
		t.Fatal("the version has no effective time")
	}
}

// The reconciler derives obligations from the facts, including for a channel
// the sampler never visits.
func TestReconcilerDerivesObligationForDisabledChannel(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := reliability.New(fixture.DB, fixture.Auth)
	fixture.Service.Reliability = service

	configureChannel(t, fixture)
	// Disable the channel: the sampler will skip it entirely, so only the
	// reconciler can record that recording is not owed.
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channels SET enabled=false WHERE id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var source string
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT source,exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL", fixture.Channel).
		Scan(&source, &exclusion); err != nil {
		t.Fatal(err)
	}
	if exclusion == nil || *exclusion != "disabled" {
		t.Fatalf("a disabled channel produced exclusion %v; PRD REL-03 requires the "+
			"deliberate stop to be recorded explicitly, and the sampler never "+
			"visits this channel so nothing else will", exclusion)
	}
}

// A channel with no source owes nothing, and that verdict must also come from
// the reconciler rather than from an absence of rows.
func TestReconcilerDerivesObligationForUnconfiguredChannel(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := reliability.New(fixture.DB, fixture.Auth)
	fixture.Service.Reliability = service
	if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL", fixture.Channel).
		Scan(&exclusion); err != nil {
		t.Fatal(err)
	}
	if exclusion == nil || *exclusion != "not_configured" {
		t.Fatalf("an unconfigured channel produced exclusion %v, want not_configured", exclusion)
	}
}

// Recording owed is the verdict continuous mode produces, and it must not carry
// an exclusion even though the fixture's source may not be producing media.
func TestReconcilerDerivesOwedObligationForContinuousMode(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := reliability.New(fixture.DB, fixture.Auth)
	fixture.Service.Reliability = service
	configureChannel(t, fixture)
	if _, err := fixture.DB.Pool.Exec(ctx,
		"UPDATE recording_policies SET mode='continuous' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL", fixture.Channel).
		Scan(&exclusion); err != nil {
		t.Fatal(err)
	}
	if exclusion != nil {
		t.Fatalf("continuous mode produced exclusion %q; recording is owed regardless "+
			"of whether the recorder is currently succeeding (REL-03)", *exclusion)
	}
}

// The reconciler walks every channel, so one settle covers the whole site.
func TestReconcileAllObligationsCoversEveryChannel(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := reliability.New(fixture.DB, fixture.Auth)
	fixture.Service.Reliability = service
	if err := fixture.Service.ReconcileAllObligations(ctx); err != nil {
		t.Fatal(err)
	}
	var channels, obligations int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM channels").Scan(&channels); err != nil {
		t.Fatal(err)
	}
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_obligations WHERE ended_at IS NULL").Scan(&obligations); err != nil {
		t.Fatal(err)
	}
	if channels == 0 || obligations != channels {
		t.Fatalf("reconciled %d obligations for %d channels; every channel needs a "+
			"verdict so the absence of an obligation always means 'not owed' rather "+
			"than 'not yet measured'", obligations, channels)
	}
}

// Repeated reconciliation must not fragment the timeline, or a month of
// ten-second passes becomes a month of rows.
func TestRepeatedReconciliationExtendsOneObligation(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := reliability.New(fixture.DB, fixture.Auth)
	fixture.Service.Reliability = service
	for i := 0; i < 4; i++ {
		if err := fixture.Service.ReconcileObligation(ctx, fixture.Channel); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT count(*) FROM recording_obligations WHERE channel_id=$1", fixture.Channel).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("four reconciliations produced %d obligations, want 1", count)
	}
}

// Coverage is media evidence, so it travels with the playback grant: losing the
// grant must lose the read, not just the UI that shows it.
func TestCoverageRequiresPlaybackGrant(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := reliability.New(fixture.DB, fixture.Auth)
	now := time.Now().UTC()
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channel_grants SET playback=false WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, now.Add(-time.Hour), now); err == nil {
		t.Fatal("coverage was readable without the playback grant; the same grant that " +
			"guards the media must guard evidence about it")
	}
	if _, err := service.ListObligations(ctx, fixture.Channel, now.Add(-time.Hour), now); err != nil {
		// ListObligations is the unguarded primitive the guarded reader uses; the
		// authorization happens at the service boundary above.
		t.Logf("primitive read: %v", err)
	}
}
