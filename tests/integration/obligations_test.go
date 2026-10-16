package integration

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/reliability"
)

// REL-03: the recording-completeness denominator.
//
// These tests are about the two properties that make the number trustworthy:
// that it is derived from the policy that was in force at the time (never from
// the current setting), and that a failure does not shrink it.

func obligationFixture(t *testing.T) (publishFixture, *reliability.Service) {
	t.Helper()
	fixture := newPublicationFixture(t)
	service := reliability.New(fixture.DB, fixture.Auth)
	return fixture, service
}

// publishedObligationFixture additionally publishes the fixture's completion, so
// recording_segments holds a real `ready` row. Coverage tests need media to
// measure against; Accept alone only queues the callback.
func publishedObligationFixture(t *testing.T) (publishFixture, *reliability.Service) {
	t.Helper()
	fixture, service := obligationFixture(t)
	if _, err := fixture.Service.Publish(context.Background(), fixture.Inbox); err != nil {
		t.Fatal(err)
	}
	return fixture, service
}

func openObligation(t *testing.T, f publishFixture, channelID id.ID) (string, *string) {
	t.Helper()
	var source string
	var exclusion *string
	if err := f.DB.Pool.QueryRow(context.Background(),
		"SELECT source,exclusion FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL", channelID).
		Scan(&source, &exclusion); err != nil {
		t.Fatal(err)
	}
	return source, exclusion
}

func foldObligation(t *testing.T, s *reliability.Service, channelID id.ID, exclusion, source string, at time.Time) {
	t.Helper()
	err := s.FoldObligation(context.Background(), reliability.Obligation{
		ChannelID:  channelID,
		Source:     source,
		Exclusion:  exclusion,
		ObservedAt: at,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Recording being owed is the default state, and it must survive a repeated
// reconciler pass as one interval rather than one row per tick.
func TestObligationExtendsWhileRecordingIsOwed(t *testing.T) {
	fixture, service := obligationFixture(t)
	base := time.Now().UTC().Add(-time.Minute)
	for i := 0; i < 5; i++ {
		foldObligation(t, service, fixture.Channel, "", "continuous", base.Add(time.Duration(i)*10*time.Second))
	}
	var count int
	if err := fixture.DB.Pool.QueryRow(context.Background(),
		"SELECT count(*) FROM recording_obligations WHERE channel_id=$1", fixture.Channel).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("five identical verdicts are one obligation, got %d", count)
	}
	if _, exclusion := openObligation(t, fixture, fixture.Channel); exclusion != nil {
		t.Fatalf("recording owed must have no exclusion, got %q", *exclusion)
	}
}

// A channel being disabled stops the requirement, and the switch is recorded
// explicitly rather than by dropping the obligation: PRD REL-03 wants a
// deliberate stop shown as its own reason, not as an absence of data.
func TestDisabledChannelClosesOwedTimeWithAnExclusion(t *testing.T) {
	fixture, service := obligationFixture(t)
	base := time.Now().UTC().Add(-time.Minute)
	changeAt := base.Add(30 * time.Second)
	foldObligation(t, service, fixture.Channel, "", "continuous", base)
	foldObligation(t, service, fixture.Channel, "disabled", "continuous", changeAt)

	var owed, excluded int
	if err := fixture.DB.Pool.QueryRow(context.Background(),
		`SELECT count(*) FILTER (WHERE exclusion IS NULL),count(*) FILTER (WHERE exclusion='disabled')
		 FROM recording_obligations WHERE channel_id=$1`, fixture.Channel).Scan(&owed, &excluded); err != nil {
		t.Fatal(err)
	}
	if owed != 1 || excluded != 1 {
		t.Fatalf("owed=%d excluded=%d, want one of each", owed, excluded)
	}
	// Unlike an interval fold — which closes at the last supporting observation
	// so an outage is not silently shortened — an obligation closes at the
	// reconciling observation, because obligations must tile the timeline: an
	// instant belonging to neither an owed stretch nor an exclusion is time the
	// completeness denominator cannot account for at all.
	var ended time.Time
	if err := fixture.DB.Pool.QueryRow(context.Background(),
		`SELECT ended_at FROM recording_obligations WHERE channel_id=$1 AND exclusion IS NULL`, fixture.Channel).Scan(&ended); err != nil {
		t.Fatal(err)
	}
	if !ended.Equal(changeAt) {
		t.Fatalf("owed stretch closed at %s, want the reconciling observation %s", ended, changeAt)
	}
	var nextStart time.Time
	if err := fixture.DB.Pool.QueryRow(context.Background(),
		`SELECT started_at FROM recording_obligations WHERE channel_id=$1 AND exclusion='disabled'`, fixture.Channel).Scan(&nextStart); err != nil {
		t.Fatal(err)
	}
	if !nextStart.Equal(ended) {
		t.Fatalf("stretches are not contiguous: owed ended at %s but the exclusion starts at %s; "+
			"the gap belongs to no stretch and would vanish from the denominator", ended, nextStart)
	}
}

// The exclusion reason is what separates a planned stop from an outage, so the
// vocabulary must survive the round trip rather than being flattened to a
// boolean.
func TestObligationExclusionsAreDistinguishable(t *testing.T) {
	fixture, service := obligationFixture(t)
	base := time.Now().UTC().Add(-time.Minute)
	for i, exclusion := range []string{"disabled", "not_configured", "maintenance", "cleared"} {
		foldObligation(t, service, fixture.Channel, exclusion, "continuous", base.Add(time.Duration(i)*10*time.Second))
	}
	rows, err := fixture.DB.Pool.Query(context.Background(),
		"SELECT DISTINCT exclusion FROM recording_obligations WHERE channel_id=$1 AND exclusion IS NOT NULL", fixture.Channel)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var exclusion string
		if err := rows.Scan(&exclusion); err != nil {
			t.Fatal(err)
		}
		seen[exclusion] = true
	}
	for _, want := range []string{"disabled", "not_configured", "maintenance", "cleared"} {
		if !seen[want] {
			t.Fatalf("exclusion %q was lost; seen %v", want, seen)
		}
	}
}

// A policy change from owed to not-owed and back produces three stretches. This
// is the mechanism that makes "don't reinterpret the past" possible: each
// stretch keeps the verdict that was true during it.
func TestPolicyChangeSplitsTheTimeline(t *testing.T) {
	fixture, service := obligationFixture(t)
	base := time.Now().UTC().Add(-time.Hour)
	foldObligation(t, service, fixture.Channel, "", "continuous", base)
	foldObligation(t, service, fixture.Channel, "disabled", "continuous", base.Add(20*time.Minute))
	foldObligation(t, service, fixture.Channel, "", "continuous", base.Add(40*time.Minute))

	items, err := service.ListObligations(context.Background(), fixture.Channel, base.Add(-time.Minute), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want three stretches, got %d", len(items))
	}
	if items[0].Exclusion != "" || items[1].Exclusion != "disabled" || items[2].Exclusion != "" {
		t.Fatalf("timeline lost its shape: %+v", items)
	}
	// Half-open: the middle stretch ends exactly where the third begins, so no
	// instant is counted twice.
	if items[1].Ended == nil || !items[1].Ended.Equal(items[2].Started) {
		t.Fatalf("stretches are not contiguous: %+v", items)
	}
}

// PRD REL-03's central prohibition, stated as a test: the same window must not
// produce a different denominator when the current policy changes.
func TestObligationTimelineIsNotRewrittenByLaterPolicyChanges(t *testing.T) {
	fixture, service := obligationFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	foldObligation(t, service, fixture.Channel, "", "continuous", base)
	foldObligation(t, service, fixture.Channel, "disabled", "continuous", base.Add(30*time.Minute))

	window := struct{ from, to time.Time }{base.Add(-time.Minute), base.Add(45 * time.Minute)}
	before, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, window.from, window.to)
	if err != nil {
		t.Fatal(err)
	}
	// Now the operator turns recording back on. The past must not move.
	foldObligation(t, service, fixture.Channel, "", "continuous", base.Add(45*time.Minute))

	after, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, window.from, window.to)
	if err != nil {
		t.Fatal(err)
	}
	if before.OwedMS != after.OwedMS {
		t.Fatalf("the owed window changed from %d to %d after a later policy change; "+
			"the past must not be reinterpreted with the current plan", before.OwedMS, after.OwedMS)
	}
	if before.ExcludedMS != after.ExcludedMS {
		t.Fatalf("excluded time changed from %d to %d", before.ExcludedMS, after.ExcludedMS)
	}
}

// A failure must keep the time in the denominator. This is the requirement that
// makes completeness fall when the site does worse, instead of rising.
func TestRecorderFailureDoesNotShrinkTheDenominator(t *testing.T) {
	fixture, service := obligationFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	foldObligation(t, service, fixture.Channel, "", "continuous", base)

	// No segments exist at all: the recorder never produced anything.
	coverage, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, base.Add(-time.Minute), base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if coverage.OwedMS <= 0 {
		t.Fatal("time owed vanished when recording failed; the denominator must not shrink")
	}
	if coverage.CoveredMS != 0 {
		t.Fatalf("covered = %d with no segments", coverage.CoveredMS)
	}
	if coverage.MissingMS != coverage.OwedMS {
		t.Fatalf("missing = %d but owed = %d; with nothing recorded the whole owed "+
			"stretch is missing", coverage.MissingMS, coverage.OwedMS)
	}
	gap, found := coverage.LongestGap()
	if !found {
		t.Fatal("owed time with nothing recorded must produce a gap")
	}
	if gap.Reason != "unknown" {
		t.Fatalf("with no recorder gap rows the cause must stay unknown, got %q", gap.Reason)
	}
}

// A segment recorded and later deleted by retention still proves the recording
// happened; ageing out old media must not lower completeness.
func TestDeletedSegmentsStillCountAsCovered(t *testing.T) {
	fixture, service := publishedObligationFixture(t)
	ctx := context.Background()
	var start, end time.Time
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT start_at,end_at FROM recording_segments WHERE channel_id=$1 LIMIT 1", fixture.Channel).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	foldObligation(t, service, fixture.Channel, "", "continuous", start.Add(-time.Second))

	before, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if before.CoveredMS == 0 {
		t.Fatal("a ready segment must count as covered")
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_segments SET state='deleted' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	after, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if after.CoveredMS != before.CoveredMS {
		t.Fatalf("deleting media by retention changed coverage from %d to %d; the "+
			"recording happened, and a retention sweep is not a recording failure",
			before.CoveredMS, after.CoveredMS)
	}
}

// A segment whose boundaries were inferred rather than observed cannot testify
// at the resolution REL-03 asks for, so it is reported apart from both the
// numerator and the denominator.
func TestProvisionalSegmentsAreUnverifiedNotCovered(t *testing.T) {
	fixture, service := publishedObligationFixture(t)
	ctx := context.Background()
	var start, end time.Time
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT start_at,end_at FROM recording_segments WHERE channel_id=$1 LIMIT 1", fixture.Channel).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, "UPDATE recording_segments SET time_evidence='provisional' WHERE channel_id=$1", fixture.Channel); err != nil {
		t.Fatal(err)
	}
	foldObligation(t, service, fixture.Channel, "", "continuous", start.Add(-time.Second))

	coverage, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if coverage.UnverifiedMS == 0 {
		t.Fatal("a provisional segment must be reported as unverified")
	}
	if coverage.CoveredMS != 0 {
		t.Fatalf("covered = %d; unverified time must not inflate the numerator", coverage.CoveredMS)
	}
	// Unverified is not a failure either.
	if coverage.MissingMS != 0 {
		t.Fatalf("missing = %d; unverified time cannot be called missing", coverage.MissingMS)
	}
}

// The four durations must partition the owed time for any query, or a reader
// summing them gets a different answer than the denominator states.
func TestCoveragePartitionsTheOwedTime(t *testing.T) {
	fixture, service := publishedObligationFixture(t)
	ctx := context.Background()
	var start, end time.Time
	if err := fixture.DB.Pool.QueryRow(ctx,
		"SELECT start_at,end_at FROM recording_segments WHERE channel_id=$1 LIMIT 1", fixture.Channel).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	// A window straddling the segment so some of it is covered and some is not.
	foldObligation(t, service, fixture.Channel, "", "continuous", start.Add(-20*time.Second))
	coverage, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, start.Add(-20*time.Second), end.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if sum := coverage.CoveredMS + coverage.MissingMS + coverage.UnverifiedMS; sum != coverage.OwedMS {
		t.Fatalf("covered+missing+unverified = %d but owed = %d", sum, coverage.OwedMS)
	}
	if coverage.MissingMS == 0 {
		t.Fatal("the window extends past the segment, so some time must be missing")
	}
}

// Only a window that begins at or after the first observed policy version is
// exact. The migration's backfilled rows cannot be exact on any window.
func TestCoveragePrecisionReflectsTheBackfilledTimeline(t *testing.T) {
	fixture, service := obligationFixture(t)
	ctx := context.Background()
	// The fixture's channel has no policy history, which is the same situation
	// as a timeline that is entirely backfilled: nothing was observed.
	coverage, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, time.Now().UTC().Add(-time.Hour), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Precision != "assumed" {
		t.Fatalf("precision = %q with no observed policy versions, want assumed", coverage.Precision)
	}
	// Once a real version exists, a window after it is exact.
	now := time.Now().UTC()
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_policy_versions(id,channel_id,version,mode,effective_at,reason)
		SELECT gen_random_uuid(),$1,1,'continuous',clock_timestamp(),'setup' WHERE NOT EXISTS(SELECT 1 FROM recording_policy_versions WHERE channel_id=$1)`, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	after, err := service.RecordingCoverage(ctx, fixture.Admin, fixture.Channel, now.Add(time.Second), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if after.Precision != "exact" {
		t.Fatalf("precision = %q for a window after observed history, want exact", after.Precision)
	}
}
