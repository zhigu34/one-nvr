package reliability

import (
	"context"
	"sort"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// Recording completeness (PRD REL-03).
//
// This computes one thing and refuses to compute another. It returns the four
// durations an operator actually needs — owed, covered, missing, unverified —
// and the attribution of the missing part. It deliberately does NOT return a
// percentage-as-verdict, because PRD REL-04 requires every rate to carry its
// numerator, denominator, unjudgeable time, verification level and time zone,
// and a single number without them is exactly the kind of figure this chapter
// exists to prevent.
//
// The central rule is that the denominator does not shrink when something
// breaks. A recorder that is down, a source that is unreachable, a pool that
// cannot be written: all of those keep the time owed and move it into "missing".
// Only a deliberate, configured absence (channel disabled, no source, outside a
// registered maintenance window) removes time from the denominator, and each of
// those is reported separately rather than blended in.

// CoverageInput is the raw evidence for one channel and window: what was owed
// and what was produced. Both are already merged into non-overlapping spans by
// the query layer, so the arithmetic below cannot double-count.
type CoverageInput struct {
	// Owed spans are time recording was required, already unioned.
	Owed []TimeSpan
	// Recorded spans are the media that exists, already unioned. A segment that
	// was recorded and later deleted still counts: the recording happened, and
	// deleting it is a retention decision, not a recording failure.
	Recorded []TimeSpan
	// Unverified spans are recorded time whose own evidence is not trustworthy
	// enough to credit — a provisional segment boundary, a stretch that crosses
	// the retention horizon. Carried separately so it cannot inflate coverage.
	Unverified []TimeSpan
	// Exclusions are the reasons time was not owed, for separate display.
	Exclusions []ExclusionSpan
	// Precision is how trustworthy the policy timeline behind this window is.
	Precision string
}

// TimeSpan is a half-open [Start, End) interval.
type TimeSpan struct {
	Start time.Time
	End   time.Time
}

// ExclusionSpan is time that was not owed, with the reason.
type ExclusionSpan struct {
	Exclusion string
	Span      TimeSpan
}

// Coverage is the answer: four durations plus attribution, all in the same
// half-open convention so they add up.
type Coverage struct {
	// OwedMS is the denominator: time recording was required.
	OwedMS int64 `json:"owed_ms"`
	// CoveredMS is the numerator: owed time that real media covers.
	CoveredMS int64 `json:"covered_ms"`
	// MissingMS is owed and not covered. It is not "excluded" time — it is the
	// failure the operator is looking for.
	MissingMS int64 `json:"missing_ms"`
	// UnverifiedMS is time that cannot be judged either way, reported apart from
	// both numerator and denominator so it can never be mistaken for coverage.
	UnverifiedMS int64 `json:"unverified_ms"`
	// ExcludedMS with its reasons: time not owed, shown separately (REL-03).
	ExcludedMS int64            `json:"excluded_ms"`
	Exclusions map[string]int64 `json:"exclusions"`
	// Gaps attribute the missing time to causes. Only populated for spans the
	// caller supplied evidence for.
	Gaps []CoverageGap `json:"gaps,omitempty"`
	// Precision is "exact" when the whole window is covered by observed policy
	// versions, "assumed" when a backfilled version was involved. REL-06: a
	// statistic spanning the edge of detailed retention must state its accuracy.
	Precision string `json:"precision"`
}

// CoverageGap is one stretch of missing time and what is known about it.
type CoverageGap struct {
	Start time.Time `json:"start_at"`
	End   time.Time `json:"end_at"`
	// Reason is the recorder's own explanation when one exists, or a
	// reliability-level verdict (recording_unavailable, source_unavailable),
	// or "unknown" when nothing recorded why. REL-02 forbids inventing a cause,
	// so unknown stays unknown.
	Reason string `json:"reason"`
}

// LongestGap returns the single largest missing stretch, which is the one an
// operator investigates first. The boolean separates "nothing was missing" from
// "nothing was measured" — a caller that only read a zero would confuse the two.
func (c Coverage) LongestGap() (CoverageGap, bool) {
	var longest CoverageGap
	found := false
	for _, gap := range c.Gaps {
		if !found || gap.End.Sub(gap.Start) > longest.End.Sub(longest.Start) {
			longest, found = gap, true
		}
	}
	return longest, found
}

// ComputeCoverage turns already-unioned spans into the four durations.
//
// The paths through this function are the specification, so they are enumerated
// rather than inferred:
//
//	owed = union(owed)
//	covered = owed ∩ recorded            (recorded outside owed time is ignored:
//	                                      media from before a policy change was
//	                                      not owed, and crediting it would
//	                                      overstate coverage)
//	missing = owed \ (recorded ∪ unverified)
//	unverified = owed ∩ unverified, removed from covered
//
// Unverified time is subtracted from covered rather than added to missing: the
// recording may well be fine, we simply cannot prove it. Calling that missing
// would be as wrong as calling it covered.
func ComputeCoverage(in CoverageInput) Coverage {
	out := Coverage{Exclusions: map[string]int64{}, Precision: in.Precision}
	if out.Precision == "" {
		out.Precision = "exact"
	}
	owed := union(in.Owed)
	recorded := union(in.Recorded)
	unverified := intersect(owed, union(in.Unverified))

	out.OwedMS = totalMS(owed)
	for _, span := range in.Exclusions {
		out.Exclusions[span.Exclusion] += durationMS(span.Span)
		out.ExcludedMS += durationMS(span.Span)
	}
	covered := intersect(owed, recorded)
	// Anything we cannot vouch for leaves the numerator. Subtract before
	// measuring, so the same instant is never counted as both.
	covered = subtract(covered, unverified)
	out.UnverifiedMS = totalMS(unverified)
	out.CoveredMS = totalMS(covered)
	missing := subtract(owed, union(append(append([]TimeSpan{}, recorded...), unverified...)))
	out.MissingMS = totalMS(missing)
	for _, span := range missing {
		out.Gaps = append(out.Gaps, CoverageGap{Start: span.Start, End: span.End, Reason: "unknown"})
	}
	return out
}

// union merges overlapping or touching spans into a sorted, disjoint set.
//
// Touching spans (one ends exactly where the next begins) are merged because the
// interval convention is half-open: [a,b) and [b,c) describe one continuous
// stretch with no instant missing between them.
func union(spans []TimeSpan) []TimeSpan {
	if len(spans) == 0 {
		return nil
	}
	sorted := make([]TimeSpan, 0, len(spans))
	for _, span := range spans {
		if span.End.After(span.Start) {
			sorted = append(sorted, span)
		}
	}
	if len(sorted) == 0 {
		return nil
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })
	out := []TimeSpan{sorted[0]}
	for _, span := range sorted[1:] {
		last := &out[len(out)-1]
		if !span.Start.After(last.End) {
			if span.End.After(last.End) {
				last.End = span.End
			}
			continue
		}
		out = append(out, span)
	}
	return out
}

// intersect returns the overlap of two already-unioned span sets.
func intersect(a, b []TimeSpan) []TimeSpan {
	var out []TimeSpan
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		start := a[i].Start
		if b[j].Start.After(start) {
			start = b[j].Start
		}
		end := a[i].End
		if b[j].End.Before(end) {
			end = b[j].End
		}
		if end.After(start) {
			out = append(out, TimeSpan{Start: start, End: end})
		}
		// Advance whichever ends first; the other may still overlap the next.
		if a[i].End.Before(b[j].End) {
			i++
		} else {
			j++
		}
	}
	return out
}

// subtract returns a minus b, both already unioned.
func subtract(a, b []TimeSpan) []TimeSpan {
	var out []TimeSpan
	j := 0
	for _, span := range a {
		cursor := span.Start
		for j < len(b) && !b[j].End.After(cursor) {
			j++
		}
		for k := j; k < len(b) && b[k].Start.Before(span.End); k++ {
			if b[k].Start.After(cursor) {
				out = append(out, TimeSpan{Start: cursor, End: b[k].Start})
			}
			if b[k].End.After(cursor) {
				cursor = b[k].End
			}
			if !cursor.Before(span.End) {
				break
			}
		}
		if cursor.Before(span.End) {
			out = append(out, TimeSpan{Start: cursor, End: span.End})
		}
	}
	return out
}

func totalMS(spans []TimeSpan) int64 {
	var total int64
	for _, span := range spans {
		total += durationMS(span)
	}
	return total
}

func durationMS(span TimeSpan) int64 {
	if !span.End.After(span.Start) {
		return 0
	}
	return span.End.Sub(span.Start).Milliseconds()
}

// PolicyVersionView is one entry of the policy timeline, for the API and for
// the completeness reader that needs to know how much of a window it can trust.
type PolicyVersionView struct {
	Version     int64     `json:"version"`
	Mode        string    `json:"mode"`
	EffectiveAt time.Time `json:"effective_at"`
	Reason      string    `json:"reason"`
}

// ListPolicyVersions returns a channel's policy timeline, oldest first.
//
// The reader uses it to decide precision: a window entirely covered by
// versions whose effective_at was observed is "exact"; one that reaches into a
// backfilled row is "assumed", because the migration could not know the real
// instant and said so rather than inventing one.
func (s *Service) ListPolicyVersions(ctx context.Context, channelID id.ID, from, to time.Time) ([]PolicyVersionView, error) {
	out := []PolicyVersionView{}
	if s.DB == nil || channelID == "" {
		return out, nil
	}
	// The window's start is included as a lower bound rather than ignored: a
	// version in force when the window opened still governs part of it, so
	// dropping it would leave the reader without the mode that was active. It
	// also gives $2 a type — a parameter the query never mentions cannot be
	// inferred by PostgreSQL and fails at prepare time.
	rows, err := s.DB.Pool.Query(ctx, `SELECT version,mode,effective_at,reason
		FROM recording_policy_versions
		WHERE channel_id=$1 AND effective_at <= $3::timestamptz
			AND (effective_at >= $2::timestamptz
				OR NOT EXISTS(SELECT 1 FROM recording_policy_versions later
					WHERE later.channel_id=$1 AND later.effective_at >= $2::timestamptz
						AND later.effective_at < recording_policy_versions.effective_at))
		ORDER BY effective_at,version`, channelID, from.UTC(), to.UTC())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item PolicyVersionView
		if err := rows.Scan(&item.Version, &item.Mode, &item.EffectiveAt, &item.Reason); err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// PolicyPrecision decides how much a window's policy timeline can be trusted.
//
// The rule follows from what a backfilled row actually claims. The migration
// could not know when an existing policy took effect, so it wrote the channel's
// creation time as a lower bound and labelled the row 'backfill'. Observed
// history therefore begins at the earliest version that is NOT a backfill, and
// any window starting before that instant is answered partly by a row whose
// effective_at is an upper bound on how little we know.
//
// So: exact only when the window begins at or after the first observed version.
// A timeline that is entirely backfilled can never be exact.
func PolicyPrecision(versions []PolicyVersionView, from time.Time) string {
	observedFrom := time.Time{}
	for _, version := range versions {
		if version.Reason == "backfill" {
			continue
		}
		if observedFrom.IsZero() || version.EffectiveAt.Before(observedFrom) {
			observedFrom = version.EffectiveAt
		}
	}
	if observedFrom.IsZero() || from.Before(observedFrom) {
		return "assumed"
	}
	return "exact"
}
