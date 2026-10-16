package reliability

import (
	"fmt"
	"time"
)

// Metrics: the rate values PRD REL-04 defines, computed from the interval facts
// batches 1 and 2 established.
//
// Two properties of this file are load-bearing and easy to lose:
//
//   - No rate leaves here without its numerator, denominator and unjudgeable
//     time. REL-04 requires all three so a reader can recompute the figure
//     rather than trust it, and a struct that cannot express a bare percentage
//     is what enforces that.
//   - Nothing here merges across stream kinds. "A healthy sub stream must not
//     mask an unavailable main stream" is a requirement about the shape of the
//     answer, so the shape has no place to put such a merge.
//
// The definitions are taken verbatim from the PRD's table and its worked
// example, which this file's tests reproduce exactly:
//
//	40m known up, 10m known down, 10m monitoring unknown, in a 60m window
//	-> monitoring coverage 83.33%, observed availability 80%, conservative 66.67%

// RateStatus distinguishes the reasons a rate can have no value. The PRD names
// three separately, and collapsing them into a zero would report "nothing was
// observed" as "everything was down".
const (
	// StatusOK means the rate is meaningful.
	StatusOK = "ok"
	// StatusNoObservation means there was no valid observation to divide by
	// (the PRD's "分母为 0 显示无有效观测").
	StatusNoObservation = "no_observation"
	// StatusNotApplicable means the question does not apply to this window —
	// an availability denominator of zero with no monitoring at all, or a
	// coverage window with no owed time.
	StatusNotApplicable = "not_applicable"
	// StatusNoSamples means no completed sample exists yet (mean time to
	// recovery with nothing recovered).
	StatusNoSamples = "no_samples"
)

// Rate is one ratio with everything needed to recompute it.
//
// Milliseconds rather than a float percentage: REL-04 wants the inputs to
// travel, and a caller that wants a percentage can divide. Storing only the
// quotient is the thing the requirement forbids.
type Rate struct {
	Status string `json:"status"`
	// NumeratorMS and DenominatorMS are the raw durations. A caller can derive
	// a percentage; a caller given only a percentage cannot derive these.
	NumeratorMS   int64 `json:"numerator_ms"`
	DenominatorMS int64 `json:"denominator_ms"`
	// UnjudgeableMS is time inside the window that cannot be counted either
	// way. It is reported beside the ratio, never folded into it.
	UnjudgeableMS int64 `json:"unjudgeable_ms"`
}

// Percent returns the ratio as a percentage, rounded to two decimals, and
// whether it is defined. Rounding here rather than storing it keeps the raw
// durations authoritative.
func (r Rate) Percent() (float64, bool) {
	if r.Status != StatusOK || r.DenominatorMS <= 0 {
		return 0, false
	}
	return float64(r.NumeratorMS) / float64(r.DenominatorMS) * 100, true
}

// FaultSample is one non-plan interruption, reduced to what the metrics need.
type FaultSample struct {
	Kind string
	// Category is the effective attribution (operator confirmation if present).
	Category string
	// Component marks a component-level fault (media server, storage, the
	// monitor itself). PRD REL-04: component outages and maintenance are listed
	// apart and not all blamed on the camera.
	Component   bool
	Maintenance bool
	Started     time.Time
	Ended       *time.Time
}

// DurationMS is the fault's length so far. An open fault is measured to `now`
// so a running outage is visible rather than counted as zero.
func (f FaultSample) DurationMS(now time.Time) int64 {
	end := now
	if f.Ended != nil {
		end = *f.Ended
	}
	if !end.After(f.Started) {
		return 0
	}
	return end.Sub(f.Started).Milliseconds()
}

// CopyEvidence is one archived copy's accessibility evidence.
type CopyEvidence struct {
	SegmentStart time.Time
	SegmentEnd   time.Time
	// Available is true when this copy was *verified reachable within its
	// validity window*. REL-04: an old or absent verification counts as
	// unknown, and a successful upload in the past is not current evidence.
	Available bool
	// VerifiedAt is when accessibility was last confirmed; nil means never.
	VerifiedAt *time.Time
	// RemoteUnknown marks a copy whose remote service was unreachable at check
	// time. That is not physical loss and must not be treated as one.
	RemoteUnknown bool
}

// IntervalTotals is the time accounting for one stream kind inside a window.
type IntervalTotals struct {
	HealthyMS     int64 `json:"healthy_ms"`
	UnavailableMS int64 `json:"unavailable_ms"`
	UnknownMS     int64 `json:"unknown_ms"`
	// ExcludedMS is time deliberately not required to be up (disabled,
	// unconfigured, maintenance). It is shown, never divided by.
	ExcludedMS int64 `json:"excluded_ms"`
}

// Metrics is the full REL-04 report for one channel and stream kind.
type Metrics struct {
	Kind string `json:"kind"`
	// Window is the statistics window the numbers describe, so a reader never
	// has to guess what "60 minutes" referred to.
	Window   Window         `json:"window"`
	Observed IntervalTotals `json:"observed"`
	// ExpectedMS is the expected pull duration: everything except time that was
	// explicitly excluded. This is the denominator coverage divides by.
	ExpectedMS int64 `json:"expected_ms"`
	// MonitoringCoverage is (healthy+unavailable)/expected: how much of the
	// window we have a verdict about at all.
	MonitoringCoverage Rate `json:"monitoring_coverage"`
	// ObservedAvailability is healthy/(healthy+unavailable): of the time we
	// know about, how much was up. This can be high while coverage is low.
	ObservedAvailability Rate `json:"observed_availability"`
	// ConservativeAvailability is healthy/expected: unknown counted as not-up,
	// so this is the lower bound an operator can commit to. Always <= observed.
	ConservativeAvailability Rate `json:"conservative_availability"`
	// Interruptions counts non-plan outages and describes them.
	Interruptions InterruptionStats `json:"interruptions"`
	// RecordingCoverage is settled obligation time that real media covers.
	RecordingCoverage Rate `json:"recording_coverage"`
	// RecordingGaps describes the missing recording time.
	RecordingGaps RecordingGapStats `json:"recording_gaps"`
	// MediaAccessibility is the verified lower bound on retrievable history.
	MediaAccessibility MediaAccessibility `json:"media_accessibility"`
	// MetricVersion travels with every report so a stored figure can be
	// identified as belonging to a particular definition (REL-06).
	MetricVersion int `json:"metric_version"`
	// Precision states which retention tier answered this window.
	Precision string `json:"precision"`
}

// Window is the half-open [From, To) a report describes.
type Window struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// InterruptionStats describes non-plan outages.
type InterruptionStats struct {
	Count   int   `json:"count"`
	TotalMS int64 `json:"total_ms"`
	// LongestMS is the single worst outage, which is what an operator triages.
	LongestMS int64 `json:"longest_ms"`
	// OpenCount and OpenMS are the outages still running. Kept apart from the
	// totals because their duration is not final: adding a running outage to
	// "total interruption time" would make the number grow while nothing new
	// happens.
	OpenCount int   `json:"open_count"`
	OpenMS    int64 `json:"open_ms"`
	// MeanTimeToRecovery averages only closed outages, as the PRD specifies.
	MeanTimeToRecovery Rate `json:"mean_time_to_recovery"`
	// ByCategory attributes the outages. Component and maintenance causes are
	// kept apart so an operator does not read a media-server outage as N broken
	// cameras.
	ByCategory       map[string]int `json:"by_category"`
	ComponentCount   int            `json:"component_count"`
	MaintenanceCount int            `json:"maintenance_count"`
}

// RecordingGapStats describes owed recording that is missing or damaged.
type RecordingGapStats struct {
	MissingMS int64 `json:"missing_ms"`
	// GapCount and LongestGapMS come from the coverage gap spans.
	GapCount     int   `json:"gap_count"`
	LongestGapMS int64 `json:"longest_gap_ms"`
	// DamagedCount is segments that exist but are not usable media.
	DamagedCount int `json:"damaged_count"`
	// Reasons counts gaps by attributed cause; "unknown" is a real answer.
	Reasons map[string]int `json:"reasons"`
}

// MediaAccessibility is the verified lower bound on retrievable history.
type MediaAccessibility struct {
	// Rate is verified-reachable time over time that still has a retention
	// requirement. It is a LOWER BOUND: unverified time is not counted as
	// accessible, and neither is time whose remote was simply unreachable.
	Rate Rate `json:"rate"`
	// UnverifiedMS is time with copies that could not be checked.
	UnverifiedMS int64 `json:"unverified_ms"`
	// RemoteUnknownMS is time whose remote was unreachable when checked — not
	// physical loss.
	RemoteUnknownMS int64 `json:"remote_unknown_ms"`
	// VerifiedCoverageMS is how much of the denominator actually carries a
	// verification, so a reader can tell a solild figure from a thin one.
	VerifiedCoverageMS int64 `json:"verified_coverage_ms"`
}

// MetricVersion identifies the definitions this build computes. Bump it when a
// definition changes, so stored rollups can be told apart.
const MetricVersion = 1

// MetricsInput is the evidence a report is computed from. Every span set is
// already clipped to the window; ComputeMetrics unions what needs unioning.
type MetricsInput struct {
	Kind        string
	Window      Window
	Healthy     []TimeSpan
	Unavailable []TimeSpan
	Unknown     []TimeSpan
	Excluded    []ExclusionSpan
	Faults      []FaultSample
	// OwedSettled is obligation time that has been settled (closed). Time in an
	// open obligation is not counted: an in-progress window would make the same
	// history report differently on every query.
	OwedSettled []TimeSpan
	// UnverifiedOwed is settled owed time whose evidence is not trustworthy;
	// reported apart from both numerator and denominator.
	UnverifiedOwed []TimeSpan
	Recorded       []TimeSpan
	Damaged        []TimeSpan
	Copies         []CopyEvidence
	// Now anchors durations of still-open faults.
	Now       time.Time
	Precision string
}

// ComputeMetrics turns evidence into the REL-04 report.
func ComputeMetrics(in MetricsInput) Metrics {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := Metrics{
		Kind:          in.Kind,
		Window:        in.Window,
		MetricVersion: MetricVersion,
		Precision:     in.Precision,
		Interruptions: InterruptionStats{ByCategory: map[string]int{}},
		RecordingGaps: RecordingGapStats{Reasons: map[string]int{}},
	}
	if out.Precision == "" {
		out.Precision = "exact"
	}

	// Time accounting. Each set is unioned first so overlapping intervals —
	// which happen at every source switch — are never counted twice (REL-04:
	// "parallel streams or duplicate files must not accumulate duration").
	healthy := union(in.Healthy)
	unavailable := union(in.Unavailable)
	unknown := union(in.Unknown)
	excluded := unionExclusions(in.Excluded)
	out.Observed = IntervalTotals{
		HealthyMS:     totalMS(healthy),
		UnavailableMS: totalMS(unavailable),
		UnknownMS:     totalMS(unknown),
		ExcludedMS:    totalMS(excluded),
	}

	// Expected pull duration is the window minus what was explicitly not
	// required. Monitoring coverage and conservative availability both divide
	// by it, which is what makes "unknown" hurt: it is inside the denominator
	// but in neither numerator's known-good part.
	windowSpan := TimeSpan{Start: in.Window.From, End: in.Window.To}
	out.ExpectedMS = totalMS(subtract([]TimeSpan{windowSpan}, excluded))

	// Known time cannot exceed the window even if facts overlap awkwardly; clip
	// so a malformed row cannot produce a rate above 100%.
	known := subtract(union(append(append([]TimeSpan{}, healthy...), unavailable...)), excluded)
	out.MonitoringCoverage = rate(totalMS(known), out.ExpectedMS, out.Observed.UnknownMS, StatusNotApplicable)
	out.ObservedAvailability = rate(out.Observed.HealthyMS, out.Observed.HealthyMS+out.Observed.UnavailableMS, out.Observed.UnknownMS, StatusNoObservation)
	// Unknown is not treated as online: the numerator is healthy time only.
	out.ConservativeAvailability = rate(out.Observed.HealthyMS, out.ExpectedMS, out.Observed.UnknownMS, StatusNotApplicable)

	out.Interruptions = computeInterruptions(in.Faults, now)
	out.RecordingCoverage, out.RecordingGaps = computeRecordingMetrics(in)
	out.MediaAccessibility = computeAccessibility(in.Copies, in.Window)
	return out
}

// rate builds a Rate, choosing the status the PRD requires for an empty
// denominator. The caller passes which of the two zero-denominator semantics
// applies, because the PRD distinguishes them.
func rate(numeratorMS, denominatorMS, unjudgeableMS int64, emptyStatus string) Rate {
	out := Rate{NumeratorMS: numeratorMS, DenominatorMS: denominatorMS, UnjudgeableMS: unjudgeableMS}
	if denominatorMS <= 0 {
		out.Status = emptyStatus
		return out
	}
	out.Status = StatusOK
	return out
}

func computeInterruptions(faults []FaultSample, now time.Time) InterruptionStats {
	out := InterruptionStats{ByCategory: map[string]int{}}
	var closedTotal int64
	for _, fault := range faults {
		duration := fault.DurationMS(now)
		if fault.Maintenance {
			// Planned work is counted apart, never as an interruption (REL-02/04).
			out.MaintenanceCount++
			continue
		}
		if fault.Component {
			out.ComponentCount++
		}
		out.Count++
		out.ByCategory[fault.Category]++
		out.TotalMS += duration
		if duration > out.LongestMS {
			out.LongestMS = duration
		}
		if fault.Ended == nil {
			out.OpenCount++
			out.OpenMS += duration
			continue
		}
		closedTotal += duration
	}
	// Mean time to recovery averages closed outages only: including a running
	// one would make the mean drift as time passes with nothing changing.
	closed := out.Count - out.OpenCount
	if closed > 0 {
		out.MeanTimeToRecovery = Rate{Status: StatusOK, NumeratorMS: closedTotal, DenominatorMS: int64(closed), UnjudgeableMS: out.OpenMS}
	} else {
		// The PRD's wording: no completed sample means no samples, not zero.
		out.MeanTimeToRecovery = Rate{Status: StatusNoSamples, UnjudgeableMS: out.OpenMS}
	}
	return out
}

func computeRecordingMetrics(in MetricsInput) (Rate, RecordingGapStats) {
	gaps := RecordingGapStats{Reasons: map[string]int{}}
	// Settled only: an open obligation has not been settled, and including it
	// would make the same past window report differently over time.
	owed := union(in.OwedSettled)
	unverified := intersect(owed, union(in.UnverifiedOwed))
	recorded := union(in.Recorded)

	// The denominator stays the settled owed time even when the recorder failed.
	// PRD REL-04's second worked example is explicit about this: 60 minutes owed
	// with 50 minutes recorded is 83.33%, not 100% of the 50 that exist.
	denominator := totalMS(owed)
	covered := subtract(intersect(owed, recorded), unverified)
	numerator := totalMS(covered)

	for _, gap := range subtract(owed, union(append(append([]TimeSpan{}, recorded...), unverified...))) {
		gaps.GapCount++
		length := durationMS(gap)
		gaps.MissingMS += length
		if length > gaps.LongestGapMS {
			gaps.LongestGapMS = length
		}
		// The cause is unknown until something supplies one; REL-02 forbids
		// inventing an explanation, so "unknown" is a real answer here.
		gaps.Reasons["unknown"]++
	}
	// Damaged segments are counted from the same evidence the coverage came
	// from. They are reported as a count rather than a duration because a
	// damaged file's intended span is exactly what is in doubt.
	gaps.DamagedCount = len(in.Damaged)

	status := StatusOK
	if denominator <= 0 {
		// No owed time at all: coverage is not applicable rather than zero.
		status = StatusNotApplicable
	}
	return Rate{Status: status, NumeratorMS: numerator, DenominatorMS: denominator, UnjudgeableMS: totalMS(unverified)}, gaps
}

// computeAccessibility answers "of the history we are still required to keep,
// how much can we actually retrieve".
//
// The PRD is specific that this is a verified lower bound: a copy whose
// accessibility was never checked, or was checked too long ago, is unknown
// rather than accessible, and a remote that was simply unreachable is not
// physical loss. Both distinctions are kept in the output.
func computeAccessibility(copies []CopyEvidence, window Window) MediaAccessibility {
	out := MediaAccessibility{Rate: Rate{Status: StatusNotApplicable}}
	if len(copies) == 0 {
		return out
	}
	var accessible, total, unverified, remoteUnknown, verified []TimeSpan
	for _, copy := range copies {
		span := TimeSpan{Start: copy.SegmentStart, End: copy.SegmentEnd}
		total = append(total, span)
		switch {
		case copy.RemoteUnknown:
			remoteUnknown = append(remoteUnknown, span)
		case copy.Available && copy.VerifiedAt != nil:
			accessible = append(accessible, span)
			verified = append(verified, span)
		default:
			unverified = append(unverified, span)
		}
	}
	denominator := totalMS(union(total))
	if denominator <= 0 {
		return out
	}
	// Accessible time is unioned, not summed: one segment with two copies is
	// still that segment's duration.
	accessibleMS := totalMS(union(accessible))
	out.Rate = Rate{Status: StatusOK, NumeratorMS: accessibleMS, DenominatorMS: denominator}
	out.UnverifiedMS = totalMS(union(unverified))
	out.RemoteUnknownMS = totalMS(union(remoteUnknown))
	out.VerifiedCoverageMS = totalMS(union(verified))
	return out
}

// unionExclusions flattens exclusion spans into plain spans for time
// accounting; the reasons travel separately in the interval totals.
func unionExclusions(spans []ExclusionSpan) []TimeSpan {
	out := make([]TimeSpan, 0, len(spans))
	for _, span := range spans {
		out = append(out, span.Span)
	}
	return union(out)
}

// PercentText renders a rate for logs and CSV without hiding its status. A
// missing value prints its reason rather than an empty cell, because an empty
// cell reads as zero in a spreadsheet.
func (r Rate) PercentText() string {
	if percent, ok := r.Percent(); ok {
		return fmt.Sprintf("%.2f%%", percent)
	}
	return r.Status
}
