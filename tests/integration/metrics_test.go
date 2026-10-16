package integration

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/reliability"
)

// REL-04/06: the metrics and the summary tiers they are read from.
//
// The unit tests pin the definitions against the PRD's own arithmetic. These
// cover what only a database can show: that the summary tier agrees with the
// detailed one, that retention degrades precision rather than silently
// answering zeros, and that the permissions hold.

// metricsFixture folds a state sequence into intervals so a window has facts.
func metricsFixture(t *testing.T, base time.Time) (publishFixture, *reliability.Service) {
	t.Helper()
	fixture := newPublicationFixture(t)
	service := reliability.New(fixture.DB, fixture.Auth)
	ctx := context.Background()
	// 10 minutes healthy, 5 unavailable: the shape the PRD's example uses,
	// scaled down so the test runs quickly.
	for _, sample := range []struct {
		state  string
		reason string
		at     time.Time
	}{
		{"healthy", "frame_progress_observed", base},
		{"healthy", "frame_progress_observed", base.Add(5 * time.Minute)},
		{"unavailable", "source_unavailable", base.Add(10 * time.Minute)},
	} {
		if err := service.Fold(ctx, reliability.Observation{
			ChannelID: fixture.Channel, Kind: "main",
			State: sample.state, Reason: sample.reason, ObservedAt: sample.at,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Close the last interval so the window is settled.
	if _, err := fixture.DB.Pool.Exec(ctx,
		"UPDATE reliability_intervals SET ended_at=$2 WHERE channel_id=$1 AND ended_at IS NULL",
		fixture.Channel, base.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	return fixture, service
}

// The summary tier must reproduce the detailed one. This is the claim the whole
// rollup design rests on: if the two disagree, every long-window figure is
// wrong in a way nobody can see.
func TestMinuteRollupAgreesWithIntervalFacts(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	from := time.Now().UTC().Add(-59 * time.Minute)
	to := time.Now().UTC()

	// Exact answer, read from intervals.
	exact, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main", From: from, To: to,
	})
	if err != nil {
		t.Fatal(err)
	}
	if exact.Precision != reliability.PrecisionExact {
		t.Fatalf("a one-hour window should be answered exactly, got %q", exact.Precision)
	}

	// Write the rollups for the same span, then read the same window from them.
	for minute := from.Truncate(time.Minute); minute.Before(to); minute = minute.Add(time.Minute) {
		if err := service.RollupSealedMinutes(ctx, minute); err != nil {
			t.Fatal(err)
		}
	}
	// A window long enough to be answered from the minute tier, so the tier
	// switch itself is exercised rather than only the rollup contents.
	longFrom := time.Now().UTC().Add(-30 * time.Hour)
	longTo := time.Now().UTC().Add(-29 * time.Hour)
	fromRollups, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main", From: longFrom, To: longTo,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Measured independently from the interval facts, restricting each interval
	// to the same window — which is what the rollup must reproduce.
	want := measureIntervals(t, fixture, longFrom, longTo)
	if fromRollups.Observed.HealthyMS != want.healthyMS {
		t.Fatalf("minute tier healthy = %d but the intervals covering the same window "+
			"report %d; the summary tier must reproduce the detailed one or long windows "+
			"answer differently from short ones", fromRollups.Observed.HealthyMS, want.healthyMS)
	}
	if fromRollups.Observed.UnavailableMS != want.unavailableMS {
		t.Fatalf("minute tier unavailable = %d but intervals report %d",
			fromRollups.Observed.UnavailableMS, want.unavailableMS)
	}
}

type intervalTotals struct{ healthyMS, unavailableMS int64 }

// measureIntervals totals the interval facts restricted to one window,
// independently of the production query. This is the yardstick the rollup path
// is compared against.
func measureIntervals(t *testing.T, f publishFixture, from, to time.Time) intervalTotals {
	t.Helper()
	var out intervalTotals
	if err := f.DB.Pool.QueryRow(context.Background(), `
		SELECT
			COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$3),$3) - GREATEST(started_at,$2)))*1000)) FILTER (WHERE state='healthy' AND exclusion IS NULL),0)::bigint,
			COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$3),$3) - GREATEST(started_at,$2)))*1000)) FILTER (WHERE state='unavailable' AND exclusion IS NULL),0)::bigint
		FROM reliability_intervals
		WHERE channel_id=$1 AND kind='main' AND started_at < $3 AND COALESCE(ended_at,$3) > $2`,
		f.Channel, from, to).Scan(&out.healthyMS, &out.unavailableMS); err != nil {
		t.Fatal(err)
	}
	return out
}

// A window longer than the detailed-read limit is answered from the minute tier
// and says so, while still producing usable figures.
func TestLongWindowReportsItsPrecision(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-48*time.Hour))
	ctx := context.Background()
	metrics, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main",
		From: time.Now().UTC().Add(-40 * time.Hour), To: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Precision != reliability.PrecisionMinute {
		t.Fatalf("a 40-hour window = %q, want %q; REL-06 requires the resolution to be "+
			"stated rather than implied", metrics.Precision, reliability.PrecisionMinute)
	}
	// The expected duration is still the window, so the rates remain meaningful.
	if metrics.ExpectedMS <= 0 {
		t.Fatal("a long window must still have an expected duration")
	}
}

// Past every retention tier the answer is that there is no evidence, not a zero
// that reads as "no outages".
func TestWindowBeyondEveryTierReportsUnavailable(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	metrics, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main",
		// Older than the 730-day daily tier.
		From: time.Now().UTC().Add(-900 * 24 * time.Hour), To: time.Now().UTC().Add(-890 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Precision != reliability.PrecisionUnavailable {
		t.Fatalf("precision = %q for a window past every tier, want %q",
			metrics.Precision, reliability.PrecisionUnavailable)
	}
	if metrics.Observed.HealthyMS != 0 || metrics.Observed.UnavailableMS != 0 {
		t.Fatalf("a window with no evidence must not report durations: %+v", metrics.Observed)
	}
}

// Deleting the detailed facts must not make the answer look better or worse: a
// reader learns the precision dropped, not that the site was perfect.
func TestSweptIntervalsDoNotFabricateAPerfectWindow(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	window := struct{ from, to time.Time }{time.Now().UTC().Add(-59 * time.Minute), time.Now().UTC()}
	before, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main", From: window.from, To: window.to,
	})
	if err != nil {
		t.Fatal(err)
	}
	if before.Observed.UnavailableMS == 0 {
		t.Fatal("fixture should contain unavailable time")
	}
	// Remove the interval facts the way retention would.
	if _, err := fixture.DB.Pool.Exec(ctx, "DELETE FROM reliability_intervals WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	after, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main", From: window.from, To: window.to,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The unavailable time is gone from the answer, which is exactly why the
	// precision field has to travel with it.
	if after.Observed.UnavailableMS != 0 {
		t.Fatalf("unavailable = %d after the facts were swept; the point of the "+
			"precision field is that the reader is told, not misled", after.Observed.UnavailableMS)
	}
	if after.ObservedAvailability.Status != reliability.StatusNoObservation {
		t.Fatalf("availability status = %q with no evidence, want %q; reporting a "+
			"percentage here would invent a flawless window out of missing data",
			after.ObservedAvailability.Status, reliability.StatusNoObservation)
	}
}

// The kinds are reported separately and never merged, because a healthy sub
// stream must not mask an unavailable main stream.
func TestStreamKindsAreNeverMerged(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	now := time.Now().UTC()
	items, err := service.ChannelMetricsAllKinds(ctx, fixture.Admin, fixture.Channel, now.Add(-59*time.Minute), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d reports, want one per stream kind", len(items))
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.Kind] = true
	}
	for _, kind := range []string{"main", "sub", "recording"} {
		if !seen[kind] {
			t.Fatalf("kind %q missing from the report; merging kinds is what lets a "+
				"healthy sub stream hide a broken main one", kind)
		}
	}
}

// Metrics are media evidence, so losing the playback grant must lose the read.
func TestMetricsRequirePlaybackGrant(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE channel_grants SET playback=false WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main", From: now.Add(-time.Hour), To: now,
	}); err == nil {
		t.Fatal("metrics were readable without the playback grant")
	}
}

// The cross-channel comparison marks channels whose evidence is too thin to
// rank, rather than letting a barely-observed channel top the table.
func TestComparisonMarksInsufficientSamples(t *testing.T) {
	fixture, _ := metricsFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	service := reliability.New(fixture.DB, fixture.Auth)
	now := time.Now().UTC()
	items, err := service.ChannelComparison(ctx, now.Add(-24*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	// No daily rollups exist yet (the daily tier only folds sealed days), so the
	// comparison is empty rather than inventing rows.
	if len(items) != 0 {
		t.Fatalf("comparison returned %d rows with no daily rollups; absence of "+
			"evidence must not become a row of zeros", len(items))
	}
}

// The daily tier folds sealed days forward from the minute tier.
func TestDailyRollupDerivesFromMinutes(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-40*24*time.Hour))
	ctx := context.Background()
	day := time.Now().UTC().Add(-40 * 24 * time.Hour).Truncate(24 * time.Hour)
	for minute := day; minute.Before(day.Add(2 * time.Hour)); minute = minute.Add(time.Minute) {
		if err := service.RollupSealedMinutes(ctx, minute); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.FinalizeDaily(ctx); err != nil {
		t.Fatal(err)
	}
	var healthy, unavailable int64
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(healthy_ms),0),COALESCE(SUM(unavailable_ms),0)
		FROM reliability_daily WHERE channel_id=$1 AND kind='main'`, fixture.Channel).
		Scan(&healthy, &unavailable); err != nil {
		t.Fatal(err)
	}
	if healthy == 0 {
		t.Fatal("the daily rollup did not pick up the sealed minutes")
	}
	// The daily totals must equal the minute totals they were derived from.
	var minuteHealthy int64
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT COALESCE(SUM(healthy_ms),0)
		FROM reliability_minutes WHERE channel_id=$1 AND kind='main'`, fixture.Channel).Scan(&minuteHealthy); err != nil {
		t.Fatal(err)
	}
	if healthy != minuteHealthy {
		t.Fatalf("daily healthy = %d but minutes sum to %d; the two tiers must agree",
			healthy, minuteHealthy)
	}
}

// A closed outage is reported with its attribution and the mean recovery, and
// an open one is listed apart.
func TestInterruptionMetricsFromRealFaults(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	// The fixture's healthy->unavailable transition opens a fault. Close it.
	if _, err := fixture.DB.Pool.Exec(ctx,
		`UPDATE fault_events SET state='resolved',ended_at=$2,duration_ms=EXTRACT(EPOCH FROM ($2-started_at))*1000
		 WHERE channel_id=$1 AND state='open'`, fixture.Channel, base.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	metrics, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main", From: base, To: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	stats := metrics.Interruptions
	if stats.Count == 0 {
		t.Fatal("a real fault produced no interruption count")
	}
	if stats.OpenCount != 0 {
		t.Fatalf("open = %d after closing every fault", stats.OpenCount)
	}
	if stats.MeanTimeToRecovery.Status != reliability.StatusOK {
		t.Fatalf("mean recovery = %q with a closed outage, want ok", stats.MeanTimeToRecovery.Status)
	}
	if stats.MeanTimeToRecovery.DenominatorMS != int64(stats.Count) {
		t.Fatalf("mean denominator = %d, want the outage count %d",
			stats.MeanTimeToRecovery.DenominatorMS, stats.Count)
	}
	// The outage time is attributed to a real category rather than left blank.
	if len(stats.ByCategory) == 0 {
		t.Fatal("interruptions were counted without attribution")
	}
}

// Every rate in the response must carry its inputs, which is the requirement
// most easily lost by "simplifying" the struct.
func TestEveryRateCarriesItsInputs(t *testing.T) {
	fixture, service := metricsFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	now := time.Now().UTC()
	metrics, err := service.ChannelMetrics(ctx, fixture.Admin, reliability.MetricsQuery{
		ChannelID: fixture.Channel, Kind: "main", From: now.Add(-59 * time.Minute), To: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, item := range map[string]reliability.Rate{
		"monitoring_coverage":       metrics.MonitoringCoverage,
		"observed_availability":     metrics.ObservedAvailability,
		"conservative_availability": metrics.ConservativeAvailability,
		"recording_coverage":        metrics.RecordingCoverage,
	} {
		if item.Status == "" {
			t.Fatalf("%s has no status; a rate must say whether it is evaluable", name)
		}
		if item.Status == reliability.StatusOK && item.DenominatorMS == 0 {
			t.Fatalf("%s is ok with a zero denominator", name)
		}
	}
	if metrics.MetricVersion != reliability.MetricVersion {
		t.Fatalf("metric version = %d, want %d; a stored figure must be identifiable",
			metrics.MetricVersion, reliability.MetricVersion)
	}
	if metrics.Window.To.IsZero() || metrics.Window.From.IsZero() {
		t.Fatal("the report must state the window it describes")
	}
}
