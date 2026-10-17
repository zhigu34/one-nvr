package integration

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/reliability"
)

// PRD REL-05's thresholds and export, on real database state.
//
// The unit-level definitions are simple; what these check is the part a unit
// test cannot: that a breach is detected from the facts the site actually
// produced, that an ongoing breach is one alert rather than one per evaluation,
// and that recovery leaves the record behind.

// alertFixture folds a state and returns the service, with recording left off
// unless a test asks for it.
func alertFixture(t *testing.T, base time.Time, states ...struct {
	State  string
	Reason string
	At     time.Time
}) (publishFixture, *reliability.Service) {
	t.Helper()
	fixture := newPublicationFixture(t)
	service := reliability.New(fixture.DB, fixture.Auth)
	ctx := context.Background()
	for _, sample := range states {
		if err := service.Fold(ctx, reliability.Observation{
			ChannelID: fixture.Channel, Kind: "main",
			State: sample.State, Reason: sample.Reason, ObservedAt: sample.At,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return fixture, service
}

type alertSample = struct {
	State  string
	Reason string
	At     time.Time
}

func TestAlertsAreNotFaultsAndDoNotRewriteHistory(t *testing.T) {
	// The whole reason alerts live in their own table: editing a threshold
	// changes future behaviour and nothing else. If alerts were fault rows, a
	// rule created today would appear to explain last month.
	fixture, service := alertFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	if _, err := service.SetAlertRule(ctx, fixture.Admin, reliability.AlertRuleInput{
		ChannelID:   &fixture.Channel,
		Kind:        reliability.AlertMainDownStreak,
		ThresholdMS: ptrInt64(int64(5 * time.Minute / time.Millisecond)),
	}); err != nil {
		t.Fatal(err)
	}
	var faultsBefore int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM fault_events WHERE channel_id=$1", fixture.Channel).Scan(&faultsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EvaluateAlerts(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var faultsAfter int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM fault_events WHERE channel_id=$1", fixture.Channel).Scan(&faultsAfter); err != nil {
		t.Fatal(err)
	}
	if faultsAfter != faultsBefore {
		t.Fatalf("evaluating thresholds changed the fault table from %d to %d rows; a "+
			"threshold is configuration and must not manufacture faults", faultsBefore, faultsAfter)
	}
}

func TestMainDownStreakFiresAndRecordsItsWindow(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	fixture, service := alertFixture(t, base,
		alertSample{"healthy", "frame_progress_observed", base},
		alertSample{"unavailable", "source_unavailable", base.Add(time.Minute)},
	)
	ctx := context.Background()
	if _, err := service.SetAlertRule(ctx, fixture.Admin, reliability.AlertRuleInput{
		ChannelID:   &fixture.Channel,
		Kind:        reliability.AlertMainDownStreak,
		ThresholdMS: ptrInt64(int64(10 * time.Minute / time.Millisecond)),
	}); err != nil {
		t.Fatal(err)
	}
	fired, err := service.EvaluateAlerts(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(fired) != 1 {
		t.Fatalf("got %d firing alerts, want 1; the stream has been down for an hour "+
			"against a ten-minute threshold", len(fired))
	}
	alert := fired[0]
	// REL-05 requires the window, the threshold and the observed value.
	if alert.Threshold <= 0 || alert.Observed <= 0 || alert.WindowStart.IsZero() {
		t.Fatalf("alert is missing its evidence: %+v", alert)
	}
	if alert.Observed < alert.Threshold {
		t.Fatalf("alert fired at %v against threshold %v", alert.Observed, alert.Threshold)
	}
	if alert.Notified {
		t.Fatal("a freshly recorded alert must not report itself as notified; only the " +
			"delivery path may claim that")
	}
}

// An ongoing breach is one alert, refreshed — not one per evaluation pass. This
// is REL-05's "deduplicate ongoing faults".
func TestRepeatedEvaluationKeepsOneFiringAlert(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	fixture, service := alertFixture(t, base,
		alertSample{"healthy", "frame_progress_observed", base},
		alertSample{"unavailable", "source_unavailable", base.Add(time.Minute)},
	)
	ctx := context.Background()
	if _, err := service.SetAlertRule(ctx, fixture.Admin, reliability.AlertRuleInput{
		ChannelID:   &fixture.Channel,
		Kind:        reliability.AlertMainDownStreak,
		ThresholdMS: ptrInt64(int64(10 * time.Minute / time.Millisecond)),
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := service.EvaluateAlerts(ctx, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT count(*) FROM reliability_alerts WHERE channel_id=$1 AND state='firing'", fixture.Channel).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("five evaluations produced %d firing alerts, want 1; a persistent "+
			"condition must not create a new alert every pass", rows)
	}
}

// Recovery closes the alert and keeps it, so a resolved incident is still
// readable afterwards.
func TestRecoveryResolvesAndKeepsTheAlert(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	fixture, service := alertFixture(t, base,
		alertSample{"healthy", "frame_progress_observed", base},
		alertSample{"unavailable", "source_unavailable", base.Add(time.Minute)},
	)
	ctx := context.Background()
	if _, err := service.SetAlertRule(ctx, fixture.Admin, reliability.AlertRuleInput{
		ChannelID:   &fixture.Channel,
		Kind:        reliability.AlertMainDownStreak,
		ThresholdMS: ptrInt64(int64(10 * time.Minute / time.Millisecond)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EvaluateAlerts(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// The stream comes back.
	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "main", State: "healthy",
		Reason: "frame_progress_observed", ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EvaluateAlerts(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var state string
	var ended *time.Time
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT state,ended_at FROM reliability_alerts WHERE channel_id=$1 ORDER BY started_at DESC LIMIT 1`,
		fixture.Channel).Scan(&state, &ended); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" || ended == nil {
		t.Fatalf("alert state = %q ended = %v, want resolved with an end time", state, ended)
	}
	var total int
	if err := fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM reliability_alerts WHERE channel_id=$1", fixture.Channel).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("recovery left %d rows; the resolved alert must stay as history", total)
	}
	// A later recurrence is a NEW alert, because the previous one is closed.
	//
	// The observation is backdated past the threshold: a streak that has lasted
	// one minute has not crossed a ten-minute threshold, and the evaluator is
	// right not to fire on it. Asserting otherwise would demand a false positive.
	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "main", State: "unavailable",
		Reason: "source_unavailable", ObservedAt: time.Now().UTC().Add(-20 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EvaluateAlerts(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var firing int
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT count(*) FROM reliability_alerts WHERE channel_id=$1 AND state='firing'", fixture.Channel).Scan(&firing); err != nil {
		t.Fatal(err)
	}
	if firing != 1 {
		t.Fatalf("recurrence produced %d firing alerts, want a new one", firing)
	}
}

// A channel that is not required to record cannot be late. Alarming on it would
// make the alarm useless on any site that leaves channels unrecorded.
func TestRecordingTimeoutIgnoresChannelsNotOwed(t *testing.T) {
	base := time.Now().UTC().Add(-time.Hour)
	fixture, service := alertFixture(t, base, alertSample{"healthy", "frame_progress_observed", base})
	ctx := context.Background()
	// No obligation exists for this channel: recording is not required.
	if _, err := service.SetAlertRule(ctx, fixture.Admin, reliability.AlertRuleInput{
		ChannelID:   &fixture.Channel,
		Kind:        reliability.AlertRecordingTimeout,
		ThresholdMS: ptrInt64(int64(time.Minute / time.Millisecond)),
	}); err != nil {
		t.Fatal(err)
	}
	fired, err := service.EvaluateAlerts(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(fired) != 0 {
		t.Fatalf("got %d alerts for a channel with no recording obligation; "+
			"'recording is off' is not 'recording is late'", len(fired))
	}
}

// Coverage is a floor: it fires below the threshold, the opposite direction
// from every duration rule.
func TestCoverageDropFiresBelowItsThreshold(t *testing.T) {
	base := time.Now().UTC().Add(-48 * time.Hour)
	fixture, service := alertFixture(t, base, alertSample{"healthy", "frame_progress_observed", base})
	ctx := context.Background()
	// A settled day where recording was owed for an hour and covered for six
	// minutes: 10% coverage against a 90% floor.
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	if _, err := fixture.DB.Pool.Exec(ctx, `
		INSERT INTO reliability_daily(channel_id,kind,day,owed_ms,covered_ms,fault_count,computed_at,input_watermark,metric_version)
		VALUES($1,'recording',$2::date,3600000,360000,0,clock_timestamp(),clock_timestamp(),1)`,
		fixture.Channel, yesterday.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetAlertRule(ctx, fixture.Admin, reliability.AlertRuleInput{
		ChannelID:        &fixture.Channel,
		Kind:             reliability.AlertDailyCoverageDrop,
		ThresholdPercent: ptrFloat(90),
	}); err != nil {
		t.Fatal(err)
	}
	fired, err := service.EvaluateAlerts(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(fired) != 1 {
		t.Fatalf("got %d alerts, want 1: 10%% coverage is below a 90%% floor", len(fired))
	}
	if fired[0].Observed >= fired[0].Threshold {
		t.Fatalf("observed %v is not below threshold %v", fired[0].Observed, fired[0].Threshold)
	}
	// And a day that met the floor does not fire.
	if _, err := fixture.DB.Pool.Exec(ctx, `
		UPDATE reliability_daily SET covered_ms=3500000 WHERE channel_id=$1 AND kind='recording'`, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EvaluateAlerts(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var firing int
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT count(*) FROM reliability_alerts WHERE channel_id=$1 AND state='firing'", fixture.Channel).Scan(&firing); err != nil {
		t.Fatal(err)
	}
	if firing != 0 {
		t.Fatalf("coverage rose above the floor but %d alerts stayed firing", firing)
	}
}

// A rule this build cannot evaluate must not silently become an alarm.
func TestUnknownThresholdsAreRejected(t *testing.T) {
	fixture, service := alertFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	// A percentage on a duration rule is unevaluatable.
	if _, err := service.SetAlertRule(ctx, fixture.Admin, reliability.AlertRuleInput{
		ChannelID:        &fixture.Channel,
		Kind:             reliability.AlertMainDownStreak,
		ThresholdPercent: ptrFloat(50),
	}); err == nil {
		t.Fatal("a duration rule accepted a percentage threshold")
	}
	// An unknown kind is not a rule.
	if _, err := service.SetAlertRule(ctx, fixture.Admin, reliability.AlertRuleInput{
		ChannelID:   &fixture.Channel,
		Kind:        "something_else",
		ThresholdMS: ptrInt64(1000),
	}); err == nil {
		t.Fatal("an unknown rule kind was accepted")
	}
}

// Thresholds are configuration, so they are administrator-only in both
// directions.
func TestAlertRulesRequireAdmin(t *testing.T) {
	fixture, service := alertFixture(t, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()
	// RequireAdmin resolves the principal against the database, so a viewer has
	// to actually be one: mutating a copy of the admin principal proves nothing.
	if _, err := fixture.DB.Pool.Exec(ctx,
		"UPDATE users SET role='viewer' WHERE id=$1", fixture.Admin.UserID); err != nil {
		t.Fatal(err)
	}
	viewer := fixture.Admin
	if _, err := service.SetAlertRule(ctx, viewer, reliability.AlertRuleInput{
		ChannelID:   &fixture.Channel,
		Kind:        reliability.AlertMainDownStreak,
		ThresholdMS: ptrInt64(1000),
	}); err == nil {
		t.Fatal("a viewer set an alert rule")
	}
	if _, err := service.ListAlertRules(ctx, viewer); err == nil {
		t.Fatal("a viewer read the alert rules")
	}
}

// The export must carry what REL-05 names, or a reader can quote a percentage
// with nothing behind it.
func TestComplianceCSVCarriesItsEvidence(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	service := reliability.New(fixture.DB, fixture.Auth)
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	if _, err := fixture.DB.Pool.Exec(ctx, `
		INSERT INTO reliability_daily(channel_id,kind,day,healthy_ms,unavailable_ms,unknown_ms,excluded_ms,owed_ms,covered_ms,unverified_ms,fault_count,longest_fault_ms,computed_at,input_watermark,metric_version)
		VALUES($1,'recording',$2::date,600000,300000,120000,60000,3600000,3000000,300000,2,180000,clock_timestamp(),clock_timestamp(),1)`,
		fixture.Channel, yesterday.Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	now := time.Now().UTC()
	if err := service.WriteComplianceCSV(ctx, fixture.Admin, fixture.Channel, now.Add(-48*time.Hour), now, "Asia/Shanghai", &buf); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	for _, required := range []string{"指标版本", "时区", "Asia/Shanghai", "应录时长", "待核验时长", "排除时长", "缺口时长", "数据精度"} {
		if !strings.Contains(text, required) {
			t.Fatalf("the export is missing %q; REL-05 requires the denominator, the "+
				"excluded and unverified amounts, the metric version and the time zone", required)
		}
	}
	// The rows must actually contain the numbers, not just the headers.
	// The fixture inserted 3600000ms owed, 3000000ms covered, 60000ms excluded.
	if !strings.Contains(text, "3600.0") || !strings.Contains(text, "3000.0") || !strings.Contains(text, "60.0") {
		t.Fatalf("the export does not contain the owed, covered and excluded durations:\n%s", text)
	}
	// Sanitised: nothing here may name an internal path or address.
	for _, secret := range []string{"/storage", "192.168", "pool", "password"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(secret)) {
			t.Fatalf("the export leaked %q; REL-05 requires a sanitised file", secret)
		}
	}
}

func ptrInt64(v int64) *int64     { return &v }
func ptrFloat(v float64) *float64 { return &v }
