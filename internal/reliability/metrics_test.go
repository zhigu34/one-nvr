package reliability

import (
	"math"
	"testing"
	"time"
)

// The PRD states two worked examples in REL-04. Both are reproduced here
// verbatim, because they are the acceptance criteria for the definitions: if
// this build disagrees with the PRD's own arithmetic, the build is wrong.
//
// First example:
//
//	"within a 60-minute expected pull window, 40 minutes known available,
//	 10 minutes known unavailable, 10 minutes monitoring unknown, then
//	 observed availability is 80%, conservative availability is 66.67%,
//	 and monitoring coverage is 83.33%; all three are shown together."
func TestPRDWorkedExampleForAvailability(t *testing.T) {
	m := ComputeMetrics(MetricsInput{
		Kind:        "main",
		Window:      Window{From: at(0), To: at(3600)},
		Healthy:     []TimeSpan{span(0, 2400)},
		Unavailable: []TimeSpan{span(2400, 3000)},
		Unknown:     []TimeSpan{span(3000, 3600)},
		Now:         at(3600),
	})

	// 40 minutes of 50 known = 80%.
	if got, ok := m.ObservedAvailability.Percent(); !ok || math.Abs(got-80) > 0.01 {
		t.Fatalf("observed availability = %v (ok=%v), want 80%%; PRD: 40m up of 50m known", got, ok)
	}
	// 40 minutes of 60 expected = 66.67%: the unknown 10 minutes are inside the
	// denominator and outside the numerator, which is what makes this the
	// conservative figure.
	if got, ok := m.ConservativeAvailability.Percent(); !ok || math.Abs(got-66.67) > 0.01 {
		t.Fatalf("conservative availability = %v (ok=%v), want 66.67%%; PRD: unknown is not online", got, ok)
	}
	// 50 minutes of 60 = 83.33%: how much of the window we have any verdict on.
	if got, ok := m.MonitoringCoverage.Percent(); !ok || math.Abs(got-83.33) > 0.01 {
		t.Fatalf("monitoring coverage = %v (ok=%v), want 83.33%%", got, ok)
	}

	// The three inputs must travel with each figure so a reader can recompute.
	if m.MonitoringCoverage.NumeratorMS != 3000000 || m.MonitoringCoverage.DenominatorMS != 3600000 {
		t.Fatalf("coverage must carry 50m/60m, got %d/%d", m.MonitoringCoverage.NumeratorMS, m.MonitoringCoverage.DenominatorMS)
	}
	if m.Observed.UnknownMS != 600000 {
		t.Fatalf("unknown time = %d, want 600000; it must be reported beside the rates", m.Observed.UnknownMS)
	}
	if m.ExpectedMS != 3600000 {
		t.Fatalf("expected pull duration = %d, want 3600000", m.ExpectedMS)
	}
}

// Second PRD example:
//
//	"if another settled 60-minute recording window produced only 50 minutes of
//	 basic valid video, recording effective coverage is 83.33%, and the
//	 denominator is NOT changed to the 50 minutes because of the 10-minute
//	 failure."
//
// This is the requirement that a failure does not improve the score, and it is
// the single easiest thing to get wrong by "helpfully" dividing by what exists.
func TestPRDWorkedExampleForRecordingCoverage(t *testing.T) {
	m := ComputeMetrics(MetricsInput{
		Kind:        "recording",
		Window:      Window{From: at(0), To: at(3600)},
		OwedSettled: []TimeSpan{span(0, 3600)},
		Recorded:    []TimeSpan{span(0, 3000)},
		Now:         at(3600),
	})
	if got, ok := m.RecordingCoverage.Percent(); !ok || math.Abs(got-83.33) > 0.01 {
		t.Fatalf("recording coverage = %v (ok=%v), want 83.33%%", got, ok)
	}
	if m.RecordingCoverage.DenominatorMS != 3600000 {
		t.Fatalf("denominator = %d, want the full 60 minutes owed; a failure must not "+
			"shrink the denominator, or the metric would improve when the site does worse",
			m.RecordingCoverage.DenominatorMS)
	}
	if m.RecordingCoverage.NumeratorMS != 3000000 {
		t.Fatalf("numerator = %d, want 3000000", m.RecordingCoverage.NumeratorMS)
	}
	if m.RecordingGaps.MissingMS != 600000 || m.RecordingGaps.GapCount != 1 || m.RecordingGaps.LongestGapMS != 600000 {
		t.Fatalf("gaps = %+v, want one 10-minute gap", m.RecordingGaps)
	}
}

// An open obligation has not settled, so it is not in the coverage denominator.
// Including it would make the same historical window report a different figure
// on every query.
func TestOpenObligationIsNotCountedInCoverage(t *testing.T) {
	m := ComputeMetrics(MetricsInput{
		Kind: "recording",
		// Only the first hour is settled; the second is still running.
		OwedSettled: []TimeSpan{span(0, 3600)},
		Recorded:    []TimeSpan{span(0, 3600)},
		Window:      Window{From: at(0), To: at(7200)},
		Now:         at(7200),
	})
	if m.RecordingCoverage.DenominatorMS != 3600000 {
		t.Fatalf("denominator = %d, want only the settled hour", m.RecordingCoverage.DenominatorMS)
	}
	if got, _ := m.RecordingCoverage.Percent(); math.Abs(got-100) > 0.01 {
		t.Fatalf("settled coverage = %v, want 100%%", got)
	}
}

// The three empty-denominator cases are named separately by the PRD and must
// not collapse into a zero, which would report "nothing observed" as
// "everything down".
func TestEmptyDenominatorsAreDistinguished(t *testing.T) {
	// No observations at all: observed availability has no observation, while
	// the two window-based figures are applicable but zero.
	bare := ComputeMetrics(MetricsInput{Kind: "main", Window: Window{From: at(0), To: at(3600)}, Now: at(3600)})
	if bare.ObservedAvailability.Status != StatusNoObservation {
		t.Fatalf("observed availability with no observations = %q, want %q",
			bare.ObservedAvailability.Status, StatusNoObservation)
	}
	if bare.ConservativeAvailability.Status != StatusOK {
		t.Fatalf("conservative availability over a window = %q, want ok; the denominator "+
			"is the window, which exists", bare.ConservativeAvailability.Status)
	}
	if bare.ConservativeAvailability.NumeratorMS != 0 {
		t.Fatalf("conservative numerator = %d, want 0 with nothing known up",
			bare.ConservativeAvailability.NumeratorMS)
	}

	// No owed time: coverage is not applicable rather than zero.
	noOwed := ComputeMetrics(MetricsInput{Kind: "recording", Window: Window{From: at(0), To: at(3600)}, Now: at(3600)})
	if noOwed.RecordingCoverage.Status != StatusNotApplicable {
		t.Fatalf("coverage with nothing owed = %q, want %q",
			noOwed.RecordingCoverage.Status, StatusNotApplicable)
	}

	// No recovered outage: the mean has no samples.
	noSamples := ComputeMetrics(MetricsInput{
		Kind: "main", Window: Window{From: at(0), To: at(3600)},
		Faults: []FaultSample{{Kind: "main", Category: CategoryUpstream, Started: at(60)}},
		Now:    at(3600),
	})
	if noSamples.Interruptions.MeanTimeToRecovery.Status != StatusNoSamples {
		t.Fatalf("mean recovery with no closed outage = %q, want %q",
			noSamples.Interruptions.MeanTimeToRecovery.Status, StatusNoSamples)
	}
	if noSamples.Interruptions.OpenCount != 1 || noSamples.Interruptions.OpenMS != 3540000 {
		t.Fatalf("the running outage must be reported separately: %+v", noSamples.Interruptions)
	}
}

// Conservative availability can never exceed observed availability: the only
// difference between them is that the conservative one counts unknown time
// against the site.
func TestConservativeNeverExceedsObserved(t *testing.T) {
	m := ComputeMetrics(MetricsInput{
		Kind:        "main",
		Window:      Window{From: at(0), To: at(3600)},
		Healthy:     []TimeSpan{span(0, 1200)},
		Unavailable: []TimeSpan{span(1200, 1800)},
		Unknown:     []TimeSpan{span(1800, 3600)},
		Now:         at(3600),
	})
	observed, _ := m.ObservedAvailability.Percent()
	conservative, _ := m.ConservativeAvailability.Percent()
	if conservative > observed+0.001 {
		t.Fatalf("conservative %v%% exceeds observed %v%%", conservative, observed)
	}
	// 20m known up of 30m known = 66.67%; 20m of 60m expected = 33.33%.
	if math.Abs(observed-66.67) > 0.01 || math.Abs(conservative-33.33) > 0.01 {
		t.Fatalf("observed = %v%%, conservative = %v%%", observed, conservative)
	}
}

// Excluded time leaves every denominator but is still reported, so a disabled
// channel reads as "not required" rather than as an outage.
func TestExcludedTimeLeavesTheDenominatorButIsShown(t *testing.T) {
	m := ComputeMetrics(MetricsInput{
		Kind:        "recording",
		Window:      Window{From: at(0), To: at(3600)},
		Healthy:     []TimeSpan{span(0, 1800)},
		Excluded:    []ExclusionSpan{{Exclusion: "disabled", Span: span(1800, 3600)}},
		OwedSettled: []TimeSpan{span(0, 1800)},
		Recorded:    []TimeSpan{span(0, 1800)},
		Now:         at(3600),
	})
	if m.ExpectedMS != 1800000 {
		t.Fatalf("expected pull duration = %d, want the half that was required", m.ExpectedMS)
	}
	if m.Observed.ExcludedMS != 1800000 {
		t.Fatalf("excluded time = %d, want it reported even though it is not divided by",
			m.Observed.ExcludedMS)
	}
	if got, ok := m.MonitoringCoverage.Percent(); !ok || math.Abs(got-100) > 0.01 {
		t.Fatalf("coverage = %v (ok=%v), want 100%%; a disabled half must not count against it", got, ok)
	}
}

// Outages are attributed, and component causes are counted apart so a media
// server outage is not read as N broken cameras (PRD REL-04).
func TestInterruptionsAttributedAndComponentsSeparated(t *testing.T) {
	m := ComputeMetrics(MetricsInput{
		Kind:   "main",
		Window: Window{From: at(0), To: at(3600)},
		Faults: []FaultSample{
			{Kind: "main", Category: CategoryUpstream, Started: at(0), Ended: ptrTime(at(600))},
			{Kind: "main", Category: CategoryUpstream, Started: at(1200), Ended: ptrTime(at(1800))},
			{Kind: "main", Category: CategoryRecording, Component: true, Started: at(1800), Ended: ptrTime(at(2400))},
			{Kind: "main", Category: CategoryMaintenance, Maintenance: true, Started: at(2400), Ended: ptrTime(at(3000))},
		},
		Now: at(3600),
	})
	stats := m.Interruptions
	if stats.Count != 3 {
		t.Fatalf("count = %d, want 3 unplanned interruptions (maintenance is not one)", stats.Count)
	}
	if stats.MaintenanceCount != 1 {
		t.Fatalf("maintenance count = %d, want 1 shown separately", stats.MaintenanceCount)
	}
	if stats.ComponentCount != 1 {
		t.Fatalf("component count = %d, want 1 so a server fault is not read as a camera fault",
			stats.ComponentCount)
	}
	if stats.TotalMS != 1800000 {
		t.Fatalf("total = %d, want 30 minutes", stats.TotalMS)
	}
	// Longest single outage is the 10-minute one.
	if stats.LongestMS != 600000 {
		t.Fatalf("longest = %d, want 600000", stats.LongestMS)
	}
	// Three closed outages of ten minutes each: the mean carries the total and
	// the count so a reader can divide them rather than trust a quotient.
	if stats.MeanTimeToRecovery.NumeratorMS != 1800000 || stats.MeanTimeToRecovery.DenominatorMS != 3 {
		t.Fatalf("mean recovery must carry total and count: %+v", stats.MeanTimeToRecovery)
	}
	// And the mean itself is ten minutes.
	if mean := time.Duration(stats.MeanTimeToRecovery.NumeratorMS/stats.MeanTimeToRecovery.DenominatorMS) * time.Millisecond; mean != 10*time.Minute {
		t.Fatalf("mean recovery = %s, want 10m", mean)
	}
	if stats.ByCategory[CategoryUpstream] != 2 || stats.ByCategory[CategoryRecording] != 1 {
		t.Fatalf("category attribution = %+v", stats.ByCategory)
	}
}

// The mean time to recovery averages closed outages only; a running outage must
// not be averaged in or the figure would drift while nothing changes.
func TestMeanRecoveryExcludesRunningOutages(t *testing.T) {
	m := ComputeMetrics(MetricsInput{
		Kind:   "main",
		Window: Window{From: at(0), To: at(3600)},
		Faults: []FaultSample{
			{Kind: "main", Category: CategoryUpstream, Started: at(0), Ended: ptrTime(at(600))},
			{Kind: "main", Category: CategoryUpstream, Started: at(1800)}, // still open
		},
		Now: at(3600),
	})
	recovery := m.Interruptions
	if recovery.MeanTimeToRecovery.DenominatorMS != 1 {
		t.Fatalf("mean denominator = %d, want 1 closed outage", recovery.MeanTimeToRecovery.DenominatorMS)
	}
	if recovery.MeanTimeToRecovery.NumeratorMS != 600000 {
		t.Fatalf("mean numerator = %d, want only the closed outage's 10 minutes",
			recovery.MeanTimeToRecovery.NumeratorMS)
	}
	if recovery.OpenCount != 1 || recovery.OpenMS != 1800000 {
		t.Fatalf("the running outage must be listed separately: %+v", recovery)
	}
	// Total and longest DO include the running outage's elapsed time. Excluding
	// it would report "total interruption time 0" alongside "longest
	// interruption 30 minutes", which is nonsense — an outage happening right
	// now is interruption time. What must exclude it is the *mean recovery*
	// above, because a running outage has no recovery duration yet.
	if recovery.TotalMS != 2400000 {
		t.Fatalf("total = %d, want 40 minutes including the running outage's elapsed time",
			recovery.TotalMS)
	}
	if recovery.LongestMS != 1800000 {
		t.Fatalf("longest = %d, want the running outage's 30 minutes", recovery.LongestMS)
	}
}

// Overlapping spans are unioned, not summed: source switches produce exactly
// this overlap and REL-04 forbids accumulating it twice.
func TestOverlappingSpansAreNotDoubleCounted(t *testing.T) {
	m := ComputeMetrics(MetricsInput{
		Kind:    "main",
		Window:  Window{From: at(0), To: at(600)},
		Healthy: []TimeSpan{span(0, 400), span(200, 600)},
		Now:     at(600),
	})
	if m.Observed.HealthyMS != 600000 {
		t.Fatalf("healthy = %d, want 600000; the union of [0,400) and [200,600) is [0,600)",
			m.Observed.HealthyMS)
	}
	if got, ok := m.MonitoringCoverage.Percent(); !ok || math.Abs(got-100) > 0.01 {
		t.Fatalf("coverage = %v (ok=%v), want 100%% rather than 133%%", got, ok)
	}
}

// Media accessibility is a verified lower bound: unchecked copies are unknown,
// and an unreachable remote is not loss.
func TestMediaAccessibilityIsAVerifiedLowerBound(t *testing.T) {
	verified := at(100)
	m := ComputeMetrics(MetricsInput{
		Kind:   "recording",
		Window: Window{From: at(0), To: at(3600)},
		Copies: []CopyEvidence{
			{SegmentStart: at(0), SegmentEnd: at(600), Available: true, VerifiedAt: &verified},
			// Never verified: unknown, not accessible.
			{SegmentStart: at(600), SegmentEnd: at(1200)},
			// Remote unreachable: also not loss, and not proof of access.
			{SegmentStart: at(1200), SegmentEnd: at(1800), RemoteUnknown: true},
			// Verified available.
			{SegmentStart: at(1800), SegmentEnd: at(2400), Available: true, VerifiedAt: &verified},
		},
		Now: at(3600),
	})
	access := m.MediaAccessibility
	if access.Rate.DenominatorMS != 2400000 {
		t.Fatalf("denominator = %d, want the 40 minutes still required", access.Rate.DenominatorMS)
	}
	if access.Rate.NumeratorMS != 1200000 {
		t.Fatalf("numerator = %d, want the 20 verified minutes; an unverified copy must not "+
			"count as accessible", access.Rate.NumeratorMS)
	}
	if got, ok := access.Rate.Percent(); !ok || math.Abs(got-50) > 0.01 {
		t.Fatalf("accessibility = %v (ok=%v), want 50%%", got, ok)
	}
	if access.UnverifiedMS != 600000 {
		t.Fatalf("unverified = %d, want 10 minutes reported apart", access.UnverifiedMS)
	}
	if access.RemoteUnknownMS != 600000 {
		t.Fatalf("remote-unknown = %d, want 10 minutes reported apart; an unreachable remote "+
			"is not physical loss", access.RemoteUnknownMS)
	}
	if access.VerifiedCoverageMS != 1200000 {
		t.Fatalf("verified coverage = %d, want the 20 minutes that were actually checked",
			access.VerifiedCoverageMS)
	}
}

// Duplicate copies of one segment must not multiply its duration.
func TestDuplicateCopiesDoNotMultiplyDuration(t *testing.T) {
	verified := at(100)
	m := ComputeMetrics(MetricsInput{
		Kind:   "recording",
		Window: Window{From: at(0), To: at(600)},
		Copies: []CopyEvidence{
			{SegmentStart: at(0), SegmentEnd: at(600), Available: true, VerifiedAt: &verified},
			{SegmentStart: at(0), SegmentEnd: at(600), Available: true, VerifiedAt: &verified},
		},
		Now: at(600),
	})
	if m.MediaAccessibility.Rate.NumeratorMS != 600000 || m.MediaAccessibility.Rate.DenominatorMS != 600000 {
		t.Fatalf("two copies of one segment = %d/%d, want 600000/600000",
			m.MediaAccessibility.Rate.NumeratorMS, m.MediaAccessibility.Rate.DenominatorMS)
	}
	if got, ok := m.MediaAccessibility.Rate.Percent(); !ok || math.Abs(got-100) > 0.01 {
		t.Fatalf("accessibility = %v (ok=%v), want 100%%", got, ok)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }
