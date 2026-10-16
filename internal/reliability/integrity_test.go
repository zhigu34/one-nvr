package reliability

import (
	"testing"
	"time"
)

func at(seconds int) time.Time {
	return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC).Add(time.Duration(seconds) * time.Second)
}

func span(from, to int) TimeSpan { return TimeSpan{Start: at(from), End: at(to)} }

// TestCoverageArithmetic pins the four durations and, more importantly, the
// three distinctions that are easy to get wrong and expensive to get wrong:
//
//   - the denominator does not shrink when recording fails (REL-03),
//   - media outside the owed window is not credited as coverage,
//   - unverified time lands in neither numerator nor denominator.
func TestCoverageArithmetic(t *testing.T) {
	cases := []struct {
		name       string
		in         CoverageInput
		owed       int64
		covered    int64
		missing    int64
		unverified int64
	}{
		{
			name:    "complete coverage",
			in:      CoverageInput{Owed: []TimeSpan{span(0, 60)}, Recorded: []TimeSpan{span(0, 60)}},
			owed:    60000,
			covered: 60000,
		},
		{
			name: "recorder down keeps the time owed (REL-03)",
			in:   CoverageInput{Owed: []TimeSpan{span(0, 60)}, Recorded: nil},
			// The failure must not shrink the denominator: owed stays 60s and
			// the whole of it is missing. A version of this that returned owed=0
			// would make availability rise when the recorder dies.
			owed:    60000,
			missing: 60000,
		},
		{
			name:    "partial coverage",
			in:      CoverageInput{Owed: []TimeSpan{span(0, 60)}, Recorded: []TimeSpan{span(0, 20), span(40, 60)}},
			owed:    60000,
			covered: 40000,
			missing: 20000,
		},
		{
			name: "media from outside the owed window is not credited",
			in:   CoverageInput{Owed: []TimeSpan{span(30, 60)}, Recorded: []TimeSpan{span(0, 60)}},
			// Only the overlap counts. Crediting the whole segment would let
			// media recorded before a policy change inflate the numerator.
			owed:    30000,
			covered: 30000,
		},
		{
			name: "unverified is neither covered nor missing",
			in:   CoverageInput{Owed: []TimeSpan{span(0, 60)}, Recorded: []TimeSpan{span(0, 60)}, Unverified: []TimeSpan{span(20, 40)}},
			// The recording probably exists; we simply cannot prove this
			// stretch, so it leaves the numerator without being called a
			// failure.
			owed:       60000,
			covered:    40000,
			missing:    0,
			unverified: 20000,
		},
		{
			name: "overlapping owed spans are not double counted",
			in:   CoverageInput{Owed: []TimeSpan{span(0, 40), span(20, 60)}, Recorded: []TimeSpan{span(0, 60)}},
			// REL-04: parallel sources and duplicate files must not add their
			// durations together. The union of [0,40) and [20,60) is [0,60).
			owed:    60000,
			covered: 60000,
		},
		{
			name: "touching spans merge because intervals are half-open",
			in:   CoverageInput{Owed: []TimeSpan{span(0, 30), span(30, 60)}, Recorded: []TimeSpan{span(0, 60)}},
			owed: 60000, covered: 60000,
		},
		{
			name: "overlapping recorded segments are not double counted",
			in:   CoverageInput{Owed: []TimeSpan{span(0, 60)}, Recorded: []TimeSpan{span(0, 40), span(20, 60)}},
			owed: 60000, covered: 60000,
		},
		{
			name: "excluded time is reported separately, not as owed",
			in: CoverageInput{
				Owed:       []TimeSpan{span(0, 30)},
				Recorded:   []TimeSpan{span(0, 30)},
				Exclusions: []ExclusionSpan{{Exclusion: "maintenance", Span: span(30, 90)}, {Exclusion: "disabled", Span: span(90, 100)}},
			},
			owed:    30000,
			covered: 30000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeCoverage(tc.in)
			if got.OwedMS != tc.owed {
				t.Errorf("owed = %d, want %d", got.OwedMS, tc.owed)
			}
			if got.CoveredMS != tc.covered {
				t.Errorf("covered = %d, want %d", got.CoveredMS, tc.covered)
			}
			if got.MissingMS != tc.missing {
				t.Errorf("missing = %d, want %d", got.MissingMS, tc.missing)
			}
			if got.UnverifiedMS != tc.unverified {
				t.Errorf("unverified = %d, want %d", got.UnverifiedMS, tc.unverified)
			}
			// The four durations must partition the owed time plus the excluded
			// time; anything else means an instant was counted twice or lost.
			if sum := got.CoveredMS + got.MissingMS + got.UnverifiedMS; sum != got.OwedMS {
				t.Errorf("covered+missing+unverified = %d but owed = %d", sum, got.OwedMS)
			}
		})
	}
}

func TestCoverageReportsExclusionsSeparately(t *testing.T) {
	got := ComputeCoverage(CoverageInput{
		Owed: []TimeSpan{span(0, 30)},
		Exclusions: []ExclusionSpan{
			{Exclusion: "maintenance", Span: span(30, 90)},
			{Exclusion: "maintenance", Span: span(90, 100)},
			{Exclusion: "disabled", Span: span(100, 110)},
		},
		Precision: "assumed",
	})
	if got.ExcludedMS != 80000 {
		t.Fatalf("excluded = %d, want 80000", got.ExcludedMS)
	}
	// PRD REL-03: each exclusion is shown separately rather than blended, so an
	// operator can tell planned maintenance from a channel someone disabled.
	if got.Exclusions["maintenance"] != 70000 || got.Exclusions["disabled"] != 10000 {
		t.Fatalf("exclusions = %+v", got.Exclusions)
	}
	if got.Precision != "assumed" {
		t.Fatalf("precision = %q, want assumed", got.Precision)
	}
}

func TestCoverageGapsCoverExactlyTheMissingTime(t *testing.T) {
	got := ComputeCoverage(CoverageInput{
		Owed:     []TimeSpan{span(0, 100)},
		Recorded: []TimeSpan{span(0, 20), span(50, 70)},
	})
	var total int64
	for _, gap := range got.Gaps {
		total += gap.End.Sub(gap.Start).Milliseconds()
	}
	if total != got.MissingMS {
		t.Fatalf("gaps total %d but missing is %d", total, got.MissingMS)
	}
	// The cause is unknown because nothing supplied one; REL-02 forbids
	// inventing an explanation.
	for _, gap := range got.Gaps {
		if gap.Reason != "unknown" {
			t.Fatalf("gap reason = %q, want unknown", gap.Reason)
		}
	}
}

func TestPolicyPrecisionDowngradesOnBackfill(t *testing.T) {
	versions := []PolicyVersionView{
		{Version: 1, Mode: "none", EffectiveAt: at(0), Reason: "backfill"},
		{Version: 2, Mode: "continuous", EffectiveAt: at(3600), Reason: "policy_apply"},
	}
	// A window starting before observed history begins cannot be trusted: the
	// backfilled row's effective_at is a lower bound, not a measurement.
	if got := PolicyPrecision(versions, at(-10)); got != "assumed" {
		t.Fatalf("window before observed history = %q, want assumed", got)
	}
	// A window at or after the first observed version is exact.
	if got := PolicyPrecision(versions, at(3600)); got != "exact" {
		t.Fatalf("window from observed history = %q, want exact", got)
	}
	// A timeline that is entirely backfilled can never be exact, however late
	// the window starts: nothing in it was ever observed.
	allBackfill := []PolicyVersionView{{Version: 1, Mode: "continuous", EffectiveAt: at(0), Reason: "backfill"}}
	if got := PolicyPrecision(allBackfill, at(99999)); got != "assumed" {
		t.Fatalf("all-backfill timeline = %q, want assumed", got)
	}
	// An empty timeline is also not exact.
	if got := PolicyPrecision(nil, at(0)); got != "assumed" {
		t.Fatalf("empty timeline = %q, want assumed", got)
	}
	observed := []PolicyVersionView{{Version: 1, Mode: "continuous", EffectiveAt: at(0), Reason: "setup"}}
	if got := PolicyPrecision(observed, at(10)); got != "exact" {
		t.Fatalf("fully observed timeline = %q, want exact", got)
	}
}
