package reliability

import (
	"context"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Trend and cross-channel comparison: the two reads REL-05's page needs beyond
// the per-channel metrics.
//
// Both are projections of the same rollups and intervals the metrics use, so a
// number here can always be reconciled with ChannelMetrics over the same
// window. Neither invents a derived score: PRD REL-05 forbids an unfounded
// composite "device score", so the comparison reports the facts an operator
// compares and leaves the judgement to them.

// TrendPoint is one day of one channel and stream kind.
type TrendPoint struct {
	Day           string `json:"day"`
	HealthyMS     int64  `json:"healthy_ms"`
	UnavailableMS int64  `json:"unavailable_ms"`
	UnknownMS     int64  `json:"unknown_ms"`
	ExcludedMS    int64  `json:"excluded_ms"`
	OwedMS        int64  `json:"owed_ms"`
	CoveredMS     int64  `json:"covered_ms"`
	UnverifiedMS  int64  `json:"unverified_ms"`
	FaultCount    int    `json:"fault_count"`
	// LongestFaultMS is the worst single outage of the day, clipped to the day.
	LongestFaultMS int64 `json:"longest_fault_ms"`
	// ComputedAt is when the rollup row behind this point was written, so a
	// reader can see how fresh it is (REL-06 wants the computation time kept).
	ComputedAt time.Time `json:"computed_at"`
	// Watermark is the newest fact included when it was computed.
	Watermark time.Time `json:"input_watermark"`
}

// DailyTrend returns the per-day series for one channel and stream kind.
//
// The window is read from the daily rollup, which is complete for every day it
// covers; a day still inside the minute tier is present too, because the daily
// pass folds sealed days forward. Days older than the daily retention are
// simply absent rather than reported as zero, so a reader cannot mistake "no
// evidence left" for "nothing happened".
func (s *Service) DailyTrend(ctx context.Context, p auth.Principal, channelID id.ID, kind string, from, to time.Time) ([]TrendPoint, error) {
	out := []TrendPoint{}
	if s.DB == nil || s.Auth == nil || channelID == "" || kind == "" || !to.After(from) {
		return out, nil
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Playback); err != nil {
		return out, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT day::text,healthy_ms,unavailable_ms,unknown_ms,excluded_ms,
			owed_ms,covered_ms,unverified_ms,fault_count,longest_fault_ms,computed_at,input_watermark
		FROM reliability_daily
		WHERE channel_id=$1 AND kind=$2 AND day >= $3::date AND day <= $4::date
		ORDER BY day`,
		channelID, kind, from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item TrendPoint
		if err := rows.Scan(&item.Day, &item.HealthyMS, &item.UnavailableMS, &item.UnknownMS, &item.ExcludedMS,
			&item.OwedMS, &item.CoveredMS, &item.UnverifiedMS, &item.FaultCount, &item.LongestFaultMS,
			&item.ComputedAt, &item.Watermark); err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ChannelSummary is one channel's facts for a cross-channel comparison.
type ChannelSummary struct {
	ChannelID   id.ID  `json:"channel_id"`
	ChannelNo   int    `json:"channel_no"`
	ChannelName string `json:"channel_name"`
	Kind        string `json:"kind"`
	// The same durations the single-channel report gives, per kind, so the two
	// views never disagree about a channel.
	HealthyMS     int64 `json:"healthy_ms"`
	UnavailableMS int64 `json:"unavailable_ms"`
	UnknownMS     int64 `json:"unknown_ms"`
	ExcludedMS    int64 `json:"excluded_ms"`
	ExpectedMS    int64 `json:"expected_ms"`
	OwedMS        int64 `json:"owed_ms"`
	CoveredMS     int64 `json:"covered_ms"`
	FaultCount    int   `json:"fault_count"`
	// ObservedMS is how much time this row is actually based on. REL-05 requires
	// the comparison to show observation duration beside the ratios, because a
	// channel observed for an hour cannot be ranked against one observed for a
	// month — the shorter sample is not evidence of being more reliable.
	ObservedMS int64 `json:"observed_ms"`
	// SampleSufficient is false when the window holds too little observation to
	// rank this channel. The rule is stated here rather than in the UI so every
	// caller applies the same one.
	SampleSufficient bool `json:"sample_sufficient"`
}

// comparisonMinimumObservation is how much observed time a channel needs before
// its figures can be compared with another's.
//
// PRD REL-05: "when there are not enough records, show insufficient sample
// rather than judging it the most reliable". A channel with ten minutes of
// history in a 90-day window having 100% availability is not the most reliable
// camera; it is the least monitored one.
const comparisonMinimumObservation = time.Hour

// ChannelComparison returns per-channel, per-kind facts for a window.
//
// Read from the rollups so a 90-day comparison across 32 channels is a bounded
// query rather than 96 interval scans. Only the main and recording kinds are
// included by default: the operations page compares channels, and a sub stream
// column would invite reading sub stream health as channel health.
func (s *Service) ChannelComparison(ctx context.Context, from, to time.Time) ([]ChannelSummary, error) {
	out := []ChannelSummary{}
	if s.DB == nil || !to.After(from) {
		return out, nil
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.channel_no,c.channel_name,d.kind,
			SUM(d.healthy_ms),SUM(d.unavailable_ms),SUM(d.unknown_ms),SUM(d.excluded_ms),
			SUM(d.owed_ms),SUM(d.covered_ms),SUM(d.fault_count)
		FROM reliability_daily d JOIN channels c ON c.id=d.channel_id
		WHERE d.day >= $1::date AND d.day <= $2::date AND d.kind IN ('main','recording')
		GROUP BY c.id,c.channel_no,c.channel_name,d.kind
		ORDER BY c.channel_no,d.kind`,
		from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02"))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ChannelSummary
		if err := rows.Scan(&item.ChannelID, &item.ChannelNo, &item.ChannelName, &item.Kind,
			&item.HealthyMS, &item.UnavailableMS, &item.UnknownMS, &item.ExcludedMS,
			&item.OwedMS, &item.CoveredMS, &item.FaultCount); err != nil {
			return out, err
		}
		item.ObservedMS = item.HealthyMS + item.UnavailableMS + item.UnknownMS
		// Expected is the window minus excluded time; the window is what the
		// comparison divides by, so it is computed rather than stored.
		item.ExpectedMS = item.ObservedMS + item.ExcludedMS
		item.SampleSufficient = time.Duration(item.ObservedMS)*time.Millisecond >= comparisonMinimumObservation
		out = append(out, item)
	}
	return out, rows.Err()
}
