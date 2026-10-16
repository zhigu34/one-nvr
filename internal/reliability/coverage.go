package reliability

import (
	"context"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// The read side of REL-03: assembling coverage evidence for one channel and
// window out of the facts in the database.
//
// Two properties are load-bearing and both come from the PRD:
//
//   - Segments that were recorded and later *deleted* still count as covered.
//     `state='deleted'` means retention removed the file; the recording
//     happened, and treating a retention decision as a recording failure would
//     make completeness drop whenever old media ages out. Only `missing` (the
//     file vanished unexpectedly) and `damaged` are failures.
//   - Segments whose own time evidence is `provisional` are carried as
//     unverified rather than credited. Their boundaries were inferred, not
//     observed, so they cannot prove coverage at the resolution REL-03 asks for.

// RecordingCoverage assembles and computes the completeness of one channel.
func (s *Service) RecordingCoverage(ctx context.Context, p auth.Principal, channelID id.ID, from, to time.Time) (Coverage, error) {
	if s.DB == nil || s.Auth == nil || channelID == "" || !to.After(from) {
		return Coverage{}, auth.ErrInvalid
	}
	// Reading recording evidence needs the same grant as reading the media
	// itself: if you may play it, you may see how complete it is.
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Playback); err != nil {
		return Coverage{}, err
	}
	in := CoverageInput{Precision: "exact"}

	obligations, err := s.ListObligations(ctx, channelID, from, to)
	if err != nil {
		return Coverage{}, err
	}
	for _, item := range obligations {
		span := clip(TimeSpan{Start: item.Started, End: endOf(item.Ended, to)}, from, to)
		if item.Exclusion == "" {
			in.Owed = append(in.Owed, span)
			continue
		}
		in.Exclusions = append(in.Exclusions, ExclusionSpan{Exclusion: item.Exclusion, Span: span})
	}

	segments, err := s.segmentSpans(ctx, channelID, from, to)
	if err != nil {
		return Coverage{}, err
	}
	for _, segment := range segments {
		span := clip(TimeSpan{Start: segment.Start, End: segment.End}, from, to)
		if segment.Provisional {
			in.Unverified = append(in.Unverified, span)
			continue
		}
		if !segment.Counts {
			continue
		}
		in.Recorded = append(in.Recorded, span)
	}

	versions, err := s.ListPolicyVersions(ctx, channelID, from, to)
	if err != nil {
		return Coverage{}, err
	}
	in.Precision = PolicyPrecision(versions, from)

	out := ComputeCoverage(in)
	// Attribute the missing time. Reasons come from the recorder's own gap
	// records so the operator sees why, not just that something is absent.
	reasons, err := s.gapReasons(ctx, channelID, from, to)
	if err != nil {
		return Coverage{}, err
	}
	for i := range out.Gaps {
		if reason, ok := reasonFor(reasons, out.Gaps[i].Start, out.Gaps[i].End); ok {
			out.Gaps[i].Reason = reason
		}
	}
	return out, nil
}

// segmentSpan is one recording segment reduced to what coverage needs.
type segmentSpan struct {
	Start time.Time
	End   time.Time
	// Counts is false for a segment that exists in the index but represents no
	// usable media.
	Counts bool
	// Provisional marks boundaries that were inferred rather than observed.
	Provisional bool
}

// segmentSpans loads the recorded media for a channel inside a window.
func (s *Service) segmentSpans(ctx context.Context, channelID id.ID, from, to time.Time) ([]segmentSpan, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT start_at,end_at,state,time_evidence
		FROM recording_segments
		WHERE channel_id=$1 AND start_at < $3::timestamptz AND end_at > $2::timestamptz
		ORDER BY start_at`, channelID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []segmentSpan
	for rows.Next() {
		var item segmentSpan
		var state, evidence string
		if err := rows.Scan(&item.Start, &item.End, &state, &evidence); err != nil {
			return nil, err
		}
		// ready: normal media. deleted: recorded and then removed by retention —
		// the recording happened, and counting a retention sweep as a recording
		// failure would make completeness fall as old media ages out.
		item.Counts = state == "ready" || state == "deleted"
		// provisional means the boundaries were inferred from the completion
		// event rather than read from the container, so it cannot testify at the
		// resolution REL-03 asks for.
		item.Provisional = evidence == "provisional" && item.Counts
		out = append(out, item)
	}
	return out, rows.Err()
}

// gapReason is one stretch the recorder itself recorded as a gap.
type gapReason struct {
	Start  time.Time
	End    time.Time
	Reason string
}

func (s *Service) gapReasons(ctx context.Context, channelID id.ID, from, to time.Time) ([]gapReason, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT start_at,COALESCE(end_at,$3::timestamptz),reason_code
		FROM recording_gaps
		WHERE channel_id=$1 AND start_at < $3::timestamptz AND COALESCE(end_at,$3::timestamptz) > $2::timestamptz
		ORDER BY start_at`, channelID, from.UTC(), to.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gapReason
	for rows.Next() {
		var item gapReason
		if err := rows.Scan(&item.Start, &item.End, &item.Reason); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// reasonFor finds the recorder's explanation for a missing stretch.
//
// A gap is attributed only when a recorded gap overlaps it substantially: the
// boundaries of the two come from different clocks (recorder completion events
// versus the derived coverage window), so requiring exact containment would
// attribute almost nothing. The overlap test asks "is this gap about this
// missing stretch", and leaving the reason unknown when nothing matches is
// required by REL-02 — an invented cause is worse than none.
func reasonFor(gaps []gapReason, start, end time.Time) (string, bool) {
	missing := end.Sub(start)
	if missing <= 0 {
		return "", false
	}
	for _, gap := range gaps {
		overlapStart := start
		if gap.Start.After(overlapStart) {
			overlapStart = gap.Start
		}
		overlapEnd := end
		if gap.End.Before(overlapEnd) {
			overlapEnd = gap.End
		}
		if overlapEnd.After(overlapStart) && overlapEnd.Sub(overlapStart) >= missing/2 {
			return gap.Reason, true
		}
	}
	return "", false
}

// clip trims a span to the query window, so an obligation or segment that
// extends beyond it contributes only its part inside.
func clip(span TimeSpan, from, to time.Time) TimeSpan {
	if span.Start.Before(from) {
		span.Start = from
	}
	if span.End.After(to) {
		span.End = to
	}
	return span
}

func endOf(ended *time.Time, fallback time.Time) time.Time {
	if ended == nil {
		// An open obligation is still owed; treat it as running to the window
		// edge rather than dropping it, or the newest time would always look
		// unowed.
		return fallback
	}
	return *ended
}
