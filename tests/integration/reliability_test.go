package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/reliability"
)

func newReliabilityFixture(t *testing.T) (publishFixture, *reliability.Service) {
	t.Helper()
	fixture := newPublicationFixture(t)
	return fixture, reliability.New(fixture.DB, fixture.Auth)
}

func countIntervals(t *testing.T, f publishFixture, query string, args ...any) int {
	t.Helper()
	var count int
	if err := f.DB.Pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// The same state sampled repeatedly is one interval, not one per sample. This is
// what keeps a month of ten-second polling from becoming a month of rows.
func TestRepeatedHealthySamplesExtendOneInterval(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	for i := 0; i < 6; i++ {
		if err := service.Fold(ctx, reliability.Observation{
			ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed",
			ObservedAt: base.Add(time.Duration(i) * 10 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if got := countIntervals(t, fixture, `SELECT count(*) FROM reliability_intervals WHERE channel_id=$1`, fixture.Channel); got != 1 {
		t.Fatalf("six samples of one state are one interval, got %d", got)
	}
	var observed, closed int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT observed_count,(ended_at IS NOT NULL)::int FROM reliability_intervals WHERE channel_id=$1`, fixture.Channel).Scan(&observed, &closed); err != nil {
		t.Fatal(err)
	}
	if observed != 6 {
		t.Fatalf("the interval must count its samples, got %d", observed)
	}
	if closed != 0 {
		t.Fatal("an uninterrupted state must stay open")
	}
}

// A state change closes the old interval and opens a new one. The closing time
// is the last observation that still supported the old state, not the sample
// that contradicted it — otherwise every outage would be reported one sampling
// period shorter than it was.
func TestStateChangeClosesAtTheLastSupportingObservation(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	healthyAt := base
	unhealthyAt := base.Add(30 * time.Second)
	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: healthyAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed",
		ObservedAt: base.Add(10 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "main", State: "unavailable", Reason: "source_unavailable", ObservedAt: unhealthyAt,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := fixture.DB.Pool.Query(ctx, `SELECT state,started_at,ended_at FROM reliability_intervals WHERE channel_id=$1 ORDER BY started_at`, fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type span struct {
		state   string
		started time.Time
		ended   *time.Time
	}
	var spans []span
	for rows.Next() {
		var s span
		if err := rows.Scan(&s.state, &s.started, &s.ended); err != nil {
			t.Fatal(err)
		}
		spans = append(spans, s)
	}
	if len(spans) != 2 {
		t.Fatalf("a state change opens a second interval, got %d", len(spans))
	}
	if spans[0].state != "healthy" || spans[1].state != "unavailable" {
		t.Fatalf("intervals must keep the states in order: %+v", spans)
	}
	// The health interval ends at its LAST supporting sample (base+10s), not at
	// the sample that found the failure (base+30s).
	want := base.Add(10 * time.Second).UTC()
	if spans[0].ended == nil || !spans[0].ended.Equal(want) {
		t.Fatalf("the healthy interval must end at its last supporting observation %v, got %v", want, spans[0].ended)
	}
	// And the failure interval starts at the sample that confirmed it, so the
	// two intervals tile the timeline without overlap or hole.
	if !spans[1].started.Equal(unhealthyAt.UTC()) {
		t.Fatalf("the failure interval starts at the confirming sample, got %v", spans[1].started)
	}
}

// A different reason in the same state is a different interval: two failures can
// share a state, and merging them would lose which one the operator is reading.
func TestADifferentReasonStartsANewInterval(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	for _, entry := range []struct {
		state  string
		reason string
	}{
		{"unavailable", "source_unavailable"},
		{"unavailable", "sub_source_unavailable"},
	} {
		if err := service.Fold(ctx, reliability.Observation{
			ChannelID: fixture.Channel, Kind: "main", State: entry.state, Reason: entry.reason, ObservedAt: base,
		}); err != nil {
			t.Fatal(err)
		}
		base = base.Add(10 * time.Second)
	}
	if got := countIntervals(t, fixture, `SELECT count(*) FROM reliability_intervals WHERE channel_id=$1`, fixture.Channel); got != 2 {
		t.Fatalf("a new reason is a new interval, got %d", got)
	}
}

// An exclusion is a different state of affairs from a fault: the channel was
// deliberately stopped. It must not be folded into availability.
func TestExcludedIntervalsAreSeparateFromFaults(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "recording", State: "excluded", Reason: "recording_disabled",
		Exclusion: "disabled", ObservedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	var exclusion *string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT exclusion FROM reliability_intervals WHERE channel_id=$1`, fixture.Channel).Scan(&exclusion); err != nil {
		t.Fatal(err)
	}
	if exclusion == nil || *exclusion != "disabled" {
		t.Fatalf("a deliberate stop carries its exclusion, got %v", exclusion)
	}
}

// A restart must not leave the last state standing as though it were current.
// PRD REL-01: the downtime is monitoring-unknown, which is a fact about our
// evidence, and it is recorded as its own interval rather than filled in.
func TestStartupClosesOpenIntervalsAndRecordsTheGap(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	// A healthy interval whose last observation is a minute ago: the process
	// was down since.
	lastSeen := time.Now().UTC().Add(-time.Minute)
	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: lastSeen,
	}); err != nil {
		t.Fatal(err)
	}

	if err := service.CloseOpenOnStart(ctx); err != nil {
		t.Fatal(err)
	}

	var closedAt *time.Time
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT ended_at FROM reliability_intervals WHERE channel_id=$1 AND state='healthy'`, fixture.Channel).Scan(&closedAt); err != nil {
		t.Fatal(err)
	}
	if closedAt == nil {
		t.Fatal("the healthy interval must be closed at startup")
	}
	if !closedAt.Equal(lastSeen.UTC()) {
		t.Fatalf("it closes at the last real observation %v, got %v", lastSeen, closedAt)
	}
	// And the downtime is recorded as unknown, not silently absent.
	if got := countIntervals(t, fixture, `SELECT count(*) FROM reliability_intervals WHERE channel_id=$1 AND state='unknown' AND reason_code='monitoring_gap'`, fixture.Channel); got != 1 {
		t.Fatalf("the downtime must be recorded as a monitoring gap, got %d", got)
	}
}

// A restart with no downtime must not invent a zero-length gap: that would add
// noise to every clean restart and make "monitoring unknown" meaningless.
func TestStartupWithoutDowntimeRecordsNoGap(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed",
		ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.CloseOpenOnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countIntervals(t, fixture, `SELECT count(*) FROM reliability_intervals WHERE channel_id=$1 AND reason_code='monitoring_gap'`, fixture.Channel); got != 0 {
		t.Fatalf("a restart in the same instant has no gap, got %d", got)
	}
}

var _ = id.ID("")

func countFaults(t *testing.T, f publishFixture, query string, args ...any) int {
	t.Helper()
	var count int
	if err := f.DB.Pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A fault opens when a stream goes unavailable, and it carries the automatic
// attribution. The attribution never says "the camera is broken": PRD REL-02
// allows a dead stream to prove only that the source or its link is
// unavailable, and refining that needs evidence or a human.
func TestUnavailableOpensAFaultWithConservativeAttribution(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	steps := []reliability.Observation{
		{ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base},
		{ChannelID: fixture.Channel, Kind: "main", State: "unavailable", Reason: "source_unavailable", ObservedAt: base.Add(10 * time.Second)},
	}
	for _, in := range steps {
		if err := service.Fold(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	var category, initial string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT category,initial_reason FROM fault_events WHERE channel_id=$1 AND state='open'`, fixture.Channel).Scan(&category, &initial); err != nil {
		t.Fatal(err)
	}
	if category != reliability.CategoryUpstream {
		t.Fatalf("a dead stream proves the upstream is unavailable, got category %q", category)
	}
	if initial != "source_unavailable" {
		t.Fatalf("the automatic reason must be kept verbatim, got %q", initial)
	}
}

// The same continuous fault updates one row rather than filing one per sample.
// PRD REL-02 requires this, and repeated callbacks must be idempotent.
func TestAContinuousFaultUpdatesOneRow(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	if err := service.Fold(ctx, reliability.Observation{
		ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		if err := service.Fold(ctx, reliability.Observation{
			ChannelID: fixture.Channel, Kind: "main", State: "unavailable", Reason: "source_unavailable",
			ObservedAt: base.Add(time.Duration(i) * 10 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	if got := countFaults(t, fixture, `SELECT count(*) FROM fault_events WHERE channel_id=$1`, fixture.Channel); got != 1 {
		t.Fatalf("eight samples of one failure are one fault, got %d", got)
	}
}

// Recovery closes the fault and records that it ended on its own. The duration
// is measured from the fault's start to the confirming healthy sample.
func TestRecoveryResolvesTheFaultAutomatically(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-2 * time.Minute)

	steps := []reliability.Observation{
		{ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base},
		{ChannelID: fixture.Channel, Kind: "main", State: "unavailable", Reason: "source_unavailable", ObservedAt: base.Add(30 * time.Second)},
		{ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base.Add(90 * time.Second)},
	}
	for _, in := range steps {
		if err := service.Fold(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	var state, recovery string
	var duration *int64
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT state,recovery,duration_ms FROM fault_events WHERE channel_id=$1`, fixture.Channel).Scan(&state, &recovery, &duration); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" || recovery != "auto" {
		t.Fatalf("a healthy sample resolves the fault on its own, got %s/%s", state, recovery)
	}
	if duration == nil || *duration < 59000 || *duration > 61000 {
		t.Fatalf("the duration must span start to recovery (60s), got %v", duration)
	}
}

// Going merely unknown is not an outage. Recording it as one would invent a
// failure out of an observation gap — the distinction PRD REL-01 insists on.
func TestUnknownDoesNotOpenAFault(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	steps := []reliability.Observation{
		{ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base},
		{ChannelID: fixture.Channel, Kind: "main", State: "unknown", Reason: "source_observation_failed", ObservedAt: base.Add(10 * time.Second)},
	}
	for _, in := range steps {
		if err := service.Fold(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if got := countFaults(t, fixture, `SELECT count(*) FROM fault_events WHERE channel_id=$1`, fixture.Channel); got != 0 {
		t.Fatalf("an observation gap is not an outage, got %d faults", got)
	}
}

// A monitoring gap is a fault of a different kind: it is our evidence that
// failed, and the operator must see it without it being blamed on the camera.
func TestMonitoringGapOpensAMonitoringFault(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	steps := []reliability.Observation{
		{ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base},
		{ChannelID: fixture.Channel, Kind: "main", State: "unknown", Reason: "monitoring_gap", ObservedAt: base.Add(30 * time.Second)},
	}
	for _, in := range steps {
		if err := service.Fold(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	var category string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT category FROM fault_events WHERE channel_id=$1 AND state='open'`, fixture.Channel).Scan(&category); err != nil {
		t.Fatal(err)
	}
	if category != reliability.CategoryMonitoring {
		t.Fatalf("an evidence gap belongs to monitoring, got %q", category)
	}
}

// A deliberate stop is not a fault: the channel was meant to be down.
func TestExcludedStateOpensNoFault(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	steps := []reliability.Observation{
		{ChannelID: fixture.Channel, Kind: "recording", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base},
		{ChannelID: fixture.Channel, Kind: "recording", State: "excluded", Reason: "recording_disabled", Exclusion: "disabled", ObservedAt: base.Add(10 * time.Second)},
	}
	for _, in := range steps {
		if err := service.Fold(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	if got := countFaults(t, fixture, `SELECT count(*) FROM fault_events WHERE channel_id=$1`, fixture.Channel); got != 0 {
		t.Fatalf("a planned stop is not a fault, got %d", got)
	}
}

// Confirming a cause adds an interpretation beside the automatic one and keeps
// a revision. PRD REL-02 forbids rewriting the original classification, because
// that is how a bad record gets quietly improved.
func TestConfirmingAFaultKeepsTheAutomaticClassification(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	steps := []reliability.Observation{
		{ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base},
		{ChannelID: fixture.Channel, Kind: "main", State: "unavailable", Reason: "source_unavailable", ObservedAt: base.Add(10 * time.Second)},
	}
	for _, in := range steps {
		if err := service.Fold(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	var faultID id.ID
	var initialCategory string
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id,category FROM fault_events WHERE channel_id=$1`, fixture.Channel).Scan(&faultID, &initialCategory); err != nil {
		t.Fatal(err)
	}

	if err := service.ConfirmFault(ctx, fixture.Admin, faultID, "switch_port_down", reliability.CategoryAuthConfig, "交换机关闭了端口"); err != nil {
		t.Fatal(err)
	}

	var category, confirmed, note string
	var revisions []byte
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT category,COALESCE(confirmed_category,''),note,revisions FROM fault_events WHERE id=$1`, faultID).Scan(&category, &confirmed, &note, &revisions); err != nil {
		t.Fatal(err)
	}
	if category != initialCategory {
		t.Fatalf("the original classification must not move: %q → %q", initialCategory, category)
	}
	if confirmed != reliability.CategoryAuthConfig || note == "" {
		t.Fatalf("the confirmation must be recorded beside it, got %q / %q", confirmed, note)
	}
	if len(revisions) <= 2 {
		t.Fatalf("the revision must be kept, got %s", revisions)
	}
	if got := reliability.EffectiveCategory(category, confirmed); got != reliability.CategoryAuthConfig {
		t.Fatalf("reports count by the confirmed category, got %q", got)
	}
}

// Registering a planned window reclassifies faults inside it without deleting
// them: the outage happened, it was just expected.
func TestMaintenanceWindowReclassifiesWithoutDeleting(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	window := reliability.MaintenanceWindow{
		ChannelID: &fixture.Channel,
		StartsAt:  base,
		EndsAt:    base.Add(30 * time.Minute),
		Note:      "更换交换机",
	}
	if _, err := service.RegisterMaintenance(ctx, fixture.Admin, window); err != nil {
		t.Fatal(err)
	}

	// A fault inside the window.
	steps := []reliability.Observation{
		{ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base.Add(5 * time.Minute)},
		{ChannelID: fixture.Channel, Kind: "main", State: "unavailable", Reason: "source_unavailable", ObservedAt: base.Add(10 * time.Minute)},
	}
	for _, in := range steps {
		if err := service.Fold(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	var maintenance bool
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT maintenance FROM fault_events WHERE channel_id=$1`, fixture.Channel).Scan(&maintenance); err != nil {
		t.Fatal(err)
	}
	if maintenance {
		t.Fatal("the fault began after the registered window ended; it is not maintenance")
	}
	if got := countFaults(t, fixture, `SELECT count(*) FROM fault_events WHERE channel_id=$1`, fixture.Channel); got != 1 {
		t.Fatalf("the fault is still recorded, got %d", got)
	}
}

// The impact of a component failure is the union of its children's intervals,
// not their sum. Eight channels down for five minutes is five minutes of site
// impact.
func TestRootImpactIsTheUnionNotTheSum(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	// Two channels of the same fixture fail in the same window with the same
	// cause, and a component fault explains both.
	var channel2 id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id FROM channels WHERE channel_no=2`).Scan(&channel2); err != nil {
		t.Fatal(err)
	}

	rootID, _ := id.New()
	childA, _ := id.New()
	childB, _ := id.New()
	start := base
	end := base.Add(5 * time.Minute)
	// The root comes first: children reference it, and the schema enforces that
	// a root event is a real row rather than a dangling label.
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO fault_events
			(id,channel_id,kind,state,started_at,initial_reason,category)
			VALUES($1,$2,'media_server','open',$3,'source_unavailable','upstream_unavailable')`,
		rootID, fixture.Channel, start); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id      id.ID
		channel id.ID
		root    any
	}{
		{childA, fixture.Channel, rootID},
		{childB, channel2, rootID},
	} {
		if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO fault_events
				(id,channel_id,kind,state,started_at,ended_at,duration_ms,initial_reason,category,root_event_id,recovery)
				VALUES($1,$2,'main','resolved',$3,$4,300000,'source_unavailable','upstream_unavailable',$5,'auto')`,
			row.id, row.channel, start, end, row.root); err != nil {
			t.Fatal(err)
		}
	}

	impact, err := service.ImpactUnion(ctx, rootID)
	if err != nil {
		t.Fatal(err)
	}
	// Two overlapping five-minute spans are five minutes of impact.
	if impact < 4*time.Minute+50*time.Second || impact > 5*time.Minute+10*time.Second {
		t.Fatalf("overlapping faults count once (5m), got %v", impact)
	}
}

// Retention is tiered and never touches an open row: an ongoing outage is
// current fact, not expired history, however long ago it started.
func TestRetentionKeepsOpenRowsAndDropsExpiredOnes(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-400 * 24 * time.Hour)

	// A long-closed fault and an interval, both well past the 365-day window.
	closedFault, _ := id.New()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO fault_events
			(id,channel_id,kind,state,started_at,ended_at,duration_ms,initial_reason,category,recovery)
			VALUES($1,$2,'main','resolved',$3,$4,1000,'source_unavailable','upstream_unavailable','auto')`,
		closedFault, fixture.Channel, old, old.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// An open fault that began at the same ancient instant — still running.
	openFault, _ := id.New()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO fault_events
			(id,channel_id,kind,state,started_at,initial_reason,category)
			VALUES($1,$2,'sub','open',$3,'sub_source_unavailable','upstream_unavailable')`,
		openFault, fixture.Channel, old); err != nil {
		t.Fatal(err)
	}
	// And an ancient raw observation, past the 7-day window.
	oldObservation, _ := id.New()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO source_observations
			(id,channel_id,kind,state,reason_code,observed_at,expires_at)
			VALUES($1,$2,'main','healthy','frame_progress_observed',$3::timestamptz,$3::timestamptz + interval '30 seconds')`,
		oldObservation, fixture.Channel, old); err != nil {
		t.Fatal(err)
	}

	service.Sweep(ctx)

	if got := countFaults(t, fixture, `SELECT count(*) FROM fault_events WHERE id=$1`, closedFault); got != 0 {
		t.Fatal("a fault resolved a year ago is past its window")
	}
	if got := countFaults(t, fixture, `SELECT count(*) FROM fault_events WHERE id=$1`, openFault); got != 1 {
		t.Fatal("an ongoing fault is current fact: it must survive the sweep however old it is")
	}
	if got := countIntervals(t, fixture, `SELECT count(*) FROM source_observations WHERE id=$1`, oldObservation); got != 0 {
		t.Fatal("raw observations are kept for a week, not a year")
	}
}

// A component fault opens one root and attaches the channels it took down, so
// the site reports one incident rather than one per channel.
func TestCorrelationAttachesChannelsToOneRoot(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Minute)

	var channel2 id.ID
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT id FROM channels WHERE channel_no=2`).Scan(&channel2); err != nil {
		t.Fatal(err)
	}
	rootID, _ := id.New()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO fault_events
			(id,channel_id,kind,state,started_at,initial_reason,category)
			VALUES($1,$2,'storage','open',$3,'pool_unavailable','storage_directory')`,
		rootID, fixture.Channel, now); err != nil {
		t.Fatal(err)
	}
	// Two channels failed at the same instant with the same cause.
	for _, channel := range []id.ID{fixture.Channel, channel2} {
		child, _ := id.New()
		if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO fault_events
				(id,channel_id,kind,state,started_at,initial_reason,category)
				VALUES($1,$2,'recording','open',$3,'pool_unavailable','storage_directory')`,
			child, channel, now.Add(5*time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	if err := service.CorrelateRoot(ctx); err != nil {
		t.Fatal(err)
	}

	var attached int
	if err := fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM fault_events WHERE root_event_id=$1`, rootID).Scan(&attached); err != nil {
		t.Fatal(err)
	}
	if attached != 2 {
		t.Fatalf("both channels belong to the component incident, got %d attached", attached)
	}
	// The children keep their own rows: correlation aggregates, it never
	// rewrites what happened on each channel.
	if got := countFaults(t, fixture, `SELECT count(*) FROM fault_events WHERE channel_id=$1 AND kind='recording'`, channel2); got != 1 {
		t.Fatal("a correlated child keeps its own record")
	}
}

// The API surfaces what the fold recorded: intervals for a channel, faults
// across channels for an administrator, and the category vocabulary so a client
// renders what this build classifies.
func TestReliabilityEndpointsServeTheRecordedHistory(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	steps := []reliability.Observation{
		{ChannelID: fixture.Channel, Kind: "main", State: "healthy", Reason: "frame_progress_observed", ObservedAt: base},
		{ChannelID: fixture.Channel, Kind: "main", State: "unavailable", Reason: "source_unavailable", ObservedAt: base.Add(time.Minute)},
	}
	for _, in := range steps {
		if err := service.Fold(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	h := httpapi.NewHandler(httpapi.Dependencies{Auth: fixture.Auth, Site: fixture.Site, PublicURL: "http://nvr.example.com", Reliability: service})

	// The window must contain the intervals, and an interval that ended at its
	// own start (two folds with no sample between them) covers no time, so the
	// window opens before the first observation rather than at it.
	from := base.Add(-2 * time.Minute).Format(time.RFC3339)
	to := base.Add(time.Hour).Format(time.RFC3339)

	// Intervals for the channel, behind the playback grant.
	r := httptest.NewRequest("GET", "http://nvr.example.com/api/v1/channels/"+string(fixture.Channel)+"/reliability/intervals?from="+from+"&to="+to, nil)
	r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: fixture.Session})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("intervals must be readable with playback: %d %s", w.Code, w.Body.String())
	}
	var page struct {
		Data struct {
			Items []reliability.IntervalView `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	intervals := page.Data.Items
	if len(intervals) != 2 {
		t.Fatalf("both intervals must be reported, got %d", len(intervals))
	}
	// The uncertainty band travels with the interval: the change happened
	// between the last supporting observation and the first contradicting one.
	for _, item := range intervals {
		if item.FirstObserved.IsZero() || item.LastObserved.IsZero() {
			t.Fatalf("an interval must carry its observation bounds: %+v", item)
		}
	}

	// Faults across channels are an administrator's view.
	r = httptest.NewRequest("GET", "http://nvr.example.com/api/v1/faults?from="+from+"&to="+to, nil)
	r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: fixture.Session})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("an administrator must be able to compare channels: %d %s", w.Code, w.Body.String())
	}
	var faults struct {
		Data struct {
			Items      []reliability.FaultView `json:"items"`
			Categories []string                `json:"categories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &faults); err != nil {
		t.Fatal(err)
	}
	items, categories := faults.Data.Items, faults.Data.Categories
	if len(items) != 1 {
		t.Fatalf("the outage must be listed, got %d", len(items))
	}
	if items[0].EffectiveCategory != reliability.CategoryUpstream {
		t.Fatalf("the fault reports its category, got %q", items[0].EffectiveCategory)
	}
	// The fault page renders "CH<number>". The contract promised channel_no from
	// the start but the implementation never sent it, so every row read
	// "CHundefined" — the API has to carry the number, not just the UUID.
	if items[0].ChannelNo < 1 || items[0].ChannelNo > 32 {
		t.Fatalf("fault channel_no = %d, want the operator-facing channel number; "+
			"without it the UI cannot name the channel", items[0].ChannelNo)
	}
	if len(categories) != 8 {
		t.Fatalf("the eight PRD categories travel with the list, got %d", len(categories))
	}
}

// Without a channel, comparing across channels is an administrator's view: a
// viewer must not learn about channels they cannot see.
func TestFaultListWithoutChannelRequiresAdmin(t *testing.T) {
	fixture, service := newReliabilityFixture(t)
	ctx := context.Background()
	viewer, err := fixture.Auth.CreateUser(ctx, fixture.Admin, auth.CreateUserInput{Username: "reliability_viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := fixture.Auth.Login(ctx, "reliability_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	_ = viewer

	h := httpapi.NewHandler(httpapi.Dependencies{Auth: fixture.Auth, Site: fixture.Site, PublicURL: "http://nvr.example.com", Reliability: service})
	from := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	to := time.Now().UTC().Format(time.RFC3339)
	r := httptest.NewRequest("GET", "http://nvr.example.com/api/v1/faults?from="+from+"&to="+to, nil)
	r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: login.RawSession})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatalf("a viewer must not compare channels, got %d %s", w.Code, w.Body.String())
	}
}
