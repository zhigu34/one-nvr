package reliability

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Query assembly: turning the fact tables into a MetricsInput.
//
// # How a window is answered
//
// Three tiers can serve a report, and which one is used follows from their
// retention windows (REL-06): detailed intervals last 365 days, minute rollups
// 30, daily rollups 730.
//
//   - Short windows are always read from the intervals. They are the
//     authoritative record, and a few hours of interval rows is nothing.
//   - Long windows inside the minute tier are read from minute rollups, purely
//     for speed. This costs no accuracy: the buckets tile the window, and the
//     rollup clips each interval to its bucket, so summing buckets reproduces
//     the interval totals exactly. The integration tests assert that equality
//     rather than trusting the argument.
//   - Windows reaching past the interval tier are answered from daily rollups,
//     and the response says so. Beyond the daily tier the window cannot be
//     answered at all, which is reported as such instead of as zeros.
//
// The `precision` field is how REL-06's "state the accuracy and the drill-down
// range" reaches the caller. It describes which tier answered, so a reader can
// tell "computed from the evidence" from "summed from a summary".

// Precision values, finest to coarsest.
const (
	// PrecisionExact means the window was computed from the interval facts.
	PrecisionExact = "exact"
	// PrecisionMinute means some of the window came from minute rollups because
	// detailed intervals are not read for windows this long. Lossless for the
	// durations reported, but not drillable to the second.
	PrecisionMinute = "minute_rollup"
	// PrecisionDaily means the window reaches past detailed retention and was
	// answered from daily rollups.
	PrecisionDaily = "daily_rollup"
	// PrecisionUnavailable means the window reaches past every tier's retention.
	// The report carries no durations; it is not the same as a window with no
	// outages.
	PrecisionUnavailable = "unavailable"
)

// detailedWindowLimit is the window length above which a report is assembled
// from minute rollups rather than intervals.
//
// A day is the point where reading intervals stops being trivial: 32 channels
// by three kinds of intervals across a month is a large scan to produce a
// handful of numbers, while minute rollups answer it from 30 times fewer rows.
// Below a day the difference does not pay for the second code path.
const detailedWindowLimit = 24 * time.Hour

// MetricsQuery names a channel, stream kind and window to report on.
type MetricsQuery struct {
	ChannelID id.ID
	Kind      string
	From      time.Time
	To        time.Time
}

// ChannelMetrics computes REL-04 metrics for one channel and one stream kind.
//
// One kind per call, never a merged result: PRD REL-04 forbids a healthy sub
// stream from masking an unavailable main stream, and the surest way to honour
// that is for no merged value to exist.
func (s *Service) ChannelMetrics(ctx context.Context, p auth.Principal, q MetricsQuery) (Metrics, error) {
	if s.DB == nil || s.Auth == nil || q.ChannelID == "" || !q.To.After(q.From) {
		return Metrics{}, auth.ErrInvalid
	}
	// Metrics describe a channel's media, so they travel with the playback
	// grant — the same grant that lets the operator watch what the numbers are
	// about.
	if err := s.Auth.RequireChannel(ctx, p, q.ChannelID, auth.Playback); err != nil {
		return Metrics{}, err
	}
	in, err := s.metricsInput(ctx, q)
	if err != nil {
		return Metrics{}, err
	}
	return ComputeMetrics(in), nil
}

func (s *Service) metricsInput(ctx context.Context, q MetricsQuery) (MetricsInput, error) {
	now := time.Now().UTC()
	in := MetricsInput{
		Kind:   q.Kind,
		Window: Window{From: q.From.UTC(), To: q.To.UTC()},
		Now:    now,
	}
	from := in.Window.From
	// age is how far back the window starts. Each branch asks "is the start
	// still inside this tier's retention", which is an age comparison — getting
	// the direction wrong here is how every window ends up looking ancient.
	age := now.Sub(from)
	switch {
	case age >= DailyRetention:
		// Past every tier. Reporting zeros here would read as "no outages",
		// which is the opposite of the truth: the evidence is gone.
		in.Precision = PrecisionUnavailable
		return in, nil
	case age >= IntervalRetention:
		// Between the interval and daily tiers: only the daily rollup can
		// answer, and the caller is told the resolution dropped.
		in.Precision = PrecisionDaily
		return s.dailyMetrics(ctx, q, in)
	case in.Window.To.Sub(from) > detailedWindowLimit && age < MinuteRetention:
		// Inside detailed retention but a long window: the minute rollups
		// answer it for a fraction of the rows. Lossless for the durations
		// reported, which the integration tests assert rather than assume.
		in.Precision = PrecisionMinute
		return s.minuteMetrics(ctx, q, in)
	default:
		// Short window inside detailed retention: read the authoritative facts.
		in.Precision = PrecisionExact
		return s.exactMetrics(ctx, q, in)
	}
}

// exactMetrics reads the interval facts directly.
func (s *Service) exactMetrics(ctx context.Context, q MetricsQuery, in MetricsInput) (MetricsInput, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT state,COALESCE(exclusion,''),started_at,COALESCE(ended_at,$4)
		FROM reliability_intervals
		WHERE channel_id=$1 AND kind=$2 AND started_at < $4 AND COALESCE(ended_at,$4) > $3`,
		q.ChannelID, q.Kind, in.Window.From, in.Window.To)
	if err != nil {
		return in, err
	}
	if err := scanIntervalRows(rows, &in); err != nil {
		return in, err
	}
	return s.attachRecordingAndFaults(ctx, q, in)
}

// minuteMetrics sums the minute rollups for the whole minutes inside the
// window, and reads the two partial edge minutes from the interval facts.
//
// The edge handling is not an optimisation detail, it is what makes the two
// tiers agree. A rollup row covers a whole minute and has already discarded the
// sub-minute boundaries inside it, so a window starting at 11:00:30 cannot be
// answered by summing rows — summing whole minutes would include the thirty
// seconds before the window opened, and the same query answered from intervals
// would disagree. Reading the partial minutes exactly is the only way the two
// paths can be relied on to produce the same number, which is what the tests
// assert.
func (s *Service) minuteMetrics(ctx context.Context, q MetricsQuery, in MetricsInput) (MetricsInput, error) {
	// First whole minute at or after the window start, last whole minute at or
	// before the window end. Everything outside that band is read exactly.
	alignedFrom := in.Window.From.Truncate(time.Minute)
	if alignedFrom.Before(in.Window.From) {
		alignedFrom = alignedFrom.Add(time.Minute)
	}
	alignedTo := in.Window.To.Truncate(time.Minute)

	if alignedFrom.Before(alignedTo) {
		row := s.DB.Pool.QueryRow(ctx, `SELECT
				COALESCE(SUM(healthy_ms),0),COALESCE(SUM(unavailable_ms),0),COALESCE(SUM(unknown_ms),0),COALESCE(SUM(excluded_ms),0)
			FROM reliability_minutes
			WHERE channel_id=$1 AND kind=$2 AND minute >= $3 AND minute < $4`,
			q.ChannelID, q.Kind, alignedFrom, alignedTo)
		var healthy, unavailable, unknown, excluded int64
		if err := row.Scan(&healthy, &unavailable, &unknown, &excluded); err != nil {
			return in, err
		}
		in.addTotalsAt(alignedFrom, healthy, unavailable, unknown, excluded)
	}
	if in.Window.From.Before(alignedFrom) || alignedTo.Before(in.Window.To) {
		// The partial minutes at each edge, read from the authoritative rows.
		edges := MetricsInput{Window: in.Window}
		if err := s.readIntervalSpans(ctx, q, in.Window.From, alignedFrom, &edges); err != nil {
			return in, err
		}
		if err := s.readIntervalSpans(ctx, q, alignedTo, in.Window.To, &edges); err != nil {
			return in, err
		}
		in.Healthy = append(in.Healthy, edges.Healthy...)
		in.Unavailable = append(in.Unavailable, edges.Unavailable...)
		in.Unknown = append(in.Unknown, edges.Unknown...)
		in.Excluded = append(in.Excluded, edges.Excluded...)
	}
	return s.attachRecordingAndFaults(ctx, q, in)
}

// readIntervalSpans reads the interval facts for one sub-range into out.
func (s *Service) readIntervalSpans(ctx context.Context, q MetricsQuery, from, to time.Time, out *MetricsInput) error {
	if !to.After(from) {
		return nil
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT state,COALESCE(exclusion,''),started_at,COALESCE(ended_at,$4)
		FROM reliability_intervals
		WHERE channel_id=$1 AND kind=$2 AND started_at < $4 AND COALESCE(ended_at,$4) > $3`,
		q.ChannelID, q.Kind, from, to)
	if err != nil {
		return err
	}
	// The rows are clipped to the sub-range, so the caller can concatenate them
	// with rollup totals and get the same answer as one exact read.
	saved := out.Window
	out.Window = Window{From: from, To: to}
	err = scanIntervalRows(rows, out)
	out.Window = saved
	return err
}

// dailyMetrics answers from daily rollups, which is all that survives past
// detailed retention.
func (s *Service) dailyMetrics(ctx context.Context, q MetricsQuery, in MetricsInput) (MetricsInput, error) {
	row := s.DB.Pool.QueryRow(ctx, `SELECT
			COALESCE(SUM(healthy_ms),0),COALESCE(SUM(unavailable_ms),0),COALESCE(SUM(unknown_ms),0),COALESCE(SUM(excluded_ms),0)
		FROM reliability_daily
		WHERE channel_id=$1 AND kind=$2 AND day >= $3::date AND day <= $4::date`,
		q.ChannelID, q.Kind, in.Window.From.Format("2006-01-02"), in.Window.To.Format("2006-01-02"))
	var healthy, unavailable, unknown, excluded int64
	if err := row.Scan(&healthy, &unavailable, &unknown, &excluded); err != nil {
		return in, err
	}
	in.addTotals(healthy, unavailable, unknown, excluded)
	return s.attachRecordingAndFaults(ctx, q, in)
}

// attachRecordingAndFaults fills the recording and interruption evidence.
//
// These two are always read from their own tables rather than from rollups:
// recording obligations and segments both outlive the minute tier, and faults
// carry their own attribution and open/closed state, which a summed rollup
// cannot express.
func (s *Service) attachRecordingAndFaults(ctx context.Context, q MetricsQuery, in MetricsInput) (MetricsInput, error) {
	obligations, err := s.ListObligations(ctx, q.ChannelID, in.Window.From, in.Window.To)
	if err != nil {
		return in, err
	}
	for _, item := range obligations {
		span := clip(TimeSpan{Start: item.Started, End: endOf(item.Ended, in.Window.To)}, in.Window.From, in.Window.To)
		if item.Exclusion != "" {
			// Exclusions are already accounted by the interval side; here they
			// only matter for the owed/not-owed split.
			continue
		}
		if item.Ended != nil {
			// Settled only. An open obligation has not finished, and counting it
			// would make the same past window report differently over time.
			in.OwedSettled = append(in.OwedSettled, span)
		}
	}
	segments, err := s.segmentSpans(ctx, q.ChannelID, in.Window.From, in.Window.To)
	if err != nil {
		return in, err
	}
	for _, segment := range segments {
		span := clip(TimeSpan{Start: segment.Start, End: segment.End}, in.Window.From, in.Window.To)
		switch {
		case segment.Provisional:
			in.UnverifiedOwed = append(in.UnverifiedOwed, span)
		case segment.Counts:
			in.Recorded = append(in.Recorded, span)
		}
	}
	faults, err := s.faultSamples(ctx, q.ChannelID, q.Kind, in.Window.From, in.Window.To)
	if err != nil {
		return in, err
	}
	in.Faults = faults
	return in, nil
}

// addTotals seeds the interval totals from a rollup sum.
//
// The spans are reconstructed as one contiguous span per category starting at
// the window, because ComputeMetrics unions spans and then measures them: the
// total is what matters, and a single span per state carries it without
// inventing boundaries the rollup never recorded.
func (in *MetricsInput) addTotals(healthy, unavailable, unknown, excluded int64) {
	in.addTotalsAt(in.Window.From, healthy, unavailable, unknown, excluded)
}

// addTotalsAt places the reconstructed spans starting at `at`, so a rollup sum
// can be positioned inside a window whose edges are read exactly.
func (in *MetricsInput) addTotalsAt(at time.Time, healthy, unavailable, unknown, excluded int64) {
	cursor := at
	add := func(ms int64) []TimeSpan {
		if ms <= 0 {
			return nil
		}
		span := TimeSpan{Start: cursor, End: cursor.Add(time.Duration(ms) * time.Millisecond)}
		cursor = span.End
		return []TimeSpan{span}
	}
	in.Healthy = append(in.Healthy, add(healthy)...)
	in.Unavailable = append(in.Unavailable, add(unavailable)...)
	in.Unknown = append(in.Unknown, add(unknown)...)
	for _, span := range add(excluded) {
		in.Excluded = append(in.Excluded, ExclusionSpan{Exclusion: "excluded", Span: span})
	}
}

// faultSamples reads the interruptions overlapping the window.
func (s *Service) faultSamples(ctx context.Context, channelID id.ID, kind string, from, to time.Time) ([]FaultSample, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT COALESCE(confirmed_category,category),maintenance,
			kind IN ('storage','media_server','monitoring','switch'),started_at,ended_at
		FROM fault_events
		WHERE channel_id=$1 AND started_at < $4 AND COALESCE(ended_at,$4) > $3
			AND ($2 = '' OR kind=$2)
		ORDER BY started_at`, channelID, kind, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FaultSample
	for rows.Next() {
		var item FaultSample
		if err := rows.Scan(&item.Category, &item.Maintenance, &item.Component, &item.Started, &item.Ended); err != nil {
			return nil, err
		}
		item.Kind = kind
		out = append(out, item)
	}
	return out, rows.Err()
}

// scanIntervalRows classifies interval rows into the metric input's spans.
func scanIntervalRows(rows pgx.Rows, in *MetricsInput) error {
	defer rows.Close()
	for rows.Next() {
		var state, exclusion string
		var started time.Time
		var ended time.Time
		if err := rows.Scan(&state, &exclusion, &started, &ended); err != nil {
			return err
		}
		span := clip(TimeSpan{Start: started, End: ended}, in.Window.From, in.Window.To)
		if exclusion != "" {
			in.Excluded = append(in.Excluded, ExclusionSpan{Exclusion: exclusion, Span: span})
			continue
		}
		switch state {
		case "healthy":
			in.Healthy = append(in.Healthy, span)
		case "unavailable":
			in.Unavailable = append(in.Unavailable, span)
		case "unknown":
			in.Unknown = append(in.Unknown, span)
		}
	}
	return rows.Err()
}

// ChannelMetricsAllKinds reports every stream kind separately, so a caller
// cannot accidentally present one kind's health as the channel's.
func (s *Service) ChannelMetricsAllKinds(ctx context.Context, p auth.Principal, channelID id.ID, from, to time.Time) ([]Metrics, error) {
	out := make([]Metrics, 0, 3)
	for _, kind := range []string{"main", "sub", "recording"} {
		item, err := s.ChannelMetrics(ctx, p, MetricsQuery{ChannelID: channelID, Kind: kind, From: from, To: to})
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}
