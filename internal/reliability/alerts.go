package reliability

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Reliability thresholds (PRD REL-05).
//
// Four kinds, and the reason each is evaluated on its own cadence rather than in
// one pass: two of them describe a state happening right now, and two describe a
// settled period. Evaluating "the last day's coverage" every ten seconds would
// recompute a number that cannot change until the day is over, and evaluating
// "is the stream down now" once a day would miss the thing entirely.

// Alert kinds.
const (
	// AlertMainDownStreak fires when the main stream has been continuously
	// unavailable for longer than the threshold.
	AlertMainDownStreak = "main_down_streak"
	// AlertRecordingTimeout fires when no recording segment has completed for
	// longer than the threshold.
	AlertRecordingTimeout = "recording_segment_timeout"
	// AlertDailyCoverageDrop fires when a settled day's recording coverage falls
	// below the threshold percentage.
	AlertDailyCoverageDrop = "daily_coverage_drop"
	// AlertMonitoringStopped fires when nothing has been observed at all for
	// longer than the threshold — which is about our own health, not the camera's.
	AlertMonitoringStopped = "monitoring_stopped"
)

// AlertRule is one configured threshold.
type AlertRule struct {
	ID               id.ID    `json:"id"`
	ChannelID        *id.ID   `json:"channel_id"`
	Kind             string   `json:"kind"`
	ThresholdMS      *int64   `json:"threshold_ms"`
	ThresholdPercent *float64 `json:"threshold_percent"`
	Enabled          bool     `json:"enabled"`
	Version          int64    `json:"version"`
}

// Alert is one firing or resolved crossing of a threshold.
type Alert struct {
	ID          id.ID      `json:"id"`
	RuleID      id.ID      `json:"rule_id"`
	ChannelID   id.ID      `json:"channel_id"`
	ChannelNo   int        `json:"channel_no"`
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	Started     time.Time  `json:"started_at"`
	Ended       *time.Time `json:"ended_at"`
	WindowStart time.Time  `json:"window_start"`
	WindowEnd   *time.Time `json:"window_end"`
	Threshold   float64    `json:"threshold_value"`
	Observed    float64    `json:"observed_value"`
	Peak        float64    `json:"peak_value"`
	// Notified is whether this firing has been announced. Exposed so an operator
	// can see that a condition is being suppressed rather than forgotten.
	Notified bool `json:"notified"`
}

// AlertObservation is one rule's verdict for one channel.
type AlertObservation struct {
	// Breaching is true when the threshold is crossed.
	Breaching bool
	// Value is what was actually measured; Threshold what it was compared to.
	Value     float64
	Threshold float64
	// WindowStart and WindowEnd describe the period judged, so the alert can
	// record what it was looking at.
	WindowStart time.Time
	WindowEnd   *time.Time
	// RecoveryAfter is when a breach stopped, used for the resolution window.
	RecoveryAt *time.Time
}

// EvaluateMainDownStreak measures how long the main stream has been
// continuously unavailable.
//
// Read from the intervals, so "continuously" is exact: an interval that started
// long ago and is still open IS the streak. A gap in monitoring is not a streak
// either way, which is why the walk stops at unknown rather than treating it as
// continuing downtime.
func (s *Service) EvaluateMainDownStreak(ctx context.Context, channelID id.ID, now time.Time) (AlertObservation, error) {
	var state, reason string
	var started time.Time
	var ended *time.Time
	err := s.DB.Pool.QueryRow(ctx, `
		SELECT state,reason_code,started_at,ended_at FROM reliability_intervals
		WHERE channel_id=$1 AND kind='main' AND ended_at IS NULL
		ORDER BY started_at DESC LIMIT 1`, channelID).Scan(&state, &reason, &started, &ended)
	if errors.Is(err, pgx.ErrNoRows) {
		// No interval at all: nothing is known, which is not the same as down.
		return AlertObservation{WindowStart: now}, nil
	}
	if err != nil {
		return AlertObservation{}, err
	}
	out := AlertObservation{WindowStart: now, RecoveryAt: ended}
	if state != "unavailable" {
		return out, nil
	}
	// The streak is measured from the interval's start. It is bounded by the
	// observation that opened it, so the value is a lower bound on the real
	// downtime — which is the honest direction for an alarm.
	out.Breaching = true
	out.Value = float64(now.Sub(started).Milliseconds())
	if !out.Breaching {
		out.Value = 0
	}
	return out, nil
}

// EvaluateMonitoringStopped measures how long since any observation arrived.
func (s *Service) EvaluateMonitoringStopped(ctx context.Context, channelID id.ID, now time.Time) (AlertObservation, error) {
	var last *time.Time
	if err := s.DB.Pool.QueryRow(ctx,
		"SELECT MAX(last_observed_at) FROM reliability_intervals WHERE channel_id=$1", channelID).Scan(&last); err != nil {
		return AlertObservation{}, err
	}
	out := AlertObservation{WindowStart: now}
	if last == nil {
		// Never observed. That is the condition itself, but with no window to
		// measure, so report it as a breach of everything since the epoch is not
		// useful; leave it to the caller's own grace period.
		return out, nil
	}
	out.Value = float64(now.Sub(*last).Milliseconds())
	out.Breaching = true
	return out, nil
}

// EvaluateRecordingTimeout measures how long since a recording segment last
// completed.
//
// Only meaningful where recording is actually owed: a channel with recording off
// is not late, it is switched off, and alarming on it would make the alarm
// useless on any site that leaves some channels unrecorded.
func (s *Service) EvaluateRecordingTimeout(ctx context.Context, channelID id.ID, now time.Time) (AlertObservation, error) {
	var owed bool
	if err := s.DB.Pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM recording_obligations
			WHERE channel_id=$1 AND exclusion IS NULL AND started_at <= $2 AND COALESCE(ended_at,$2) > $2
		)`, channelID, now).Scan(&owed); err != nil {
		return AlertObservation{}, err
	}
	out := AlertObservation{WindowStart: now}
	if !owed {
		return out, nil
	}
	var last *time.Time
	if err := s.DB.Pool.QueryRow(ctx, `
		SELECT MAX(GREATEST(COALESCE(ready_at,start_at),end_at))
		FROM recording_segments
		WHERE channel_id=$1 AND state IN ('ready','deleted')`, channelID).Scan(&last); err != nil {
		return AlertObservation{}, err
	}
	if last == nil {
		// Recording is owed and nothing has ever been produced. Measure from
		// when the obligation began so the alarm can still fire.
		var since time.Time
		if err := s.DB.Pool.QueryRow(ctx, `
			SELECT started_at FROM recording_obligations
			WHERE channel_id=$1 AND exclusion IS NULL AND started_at <= $2 AND COALESCE(ended_at,$2) > $2
			ORDER BY started_at LIMIT 1`, channelID, now).Scan(&since); err != nil {
			return out, nil
		}
		out.Value = float64(now.Sub(since).Milliseconds())
		out.Breaching = true
		return out, nil
	}
	out.Value = float64(now.Sub(*last).Milliseconds())
	out.Breaching = true
	return out, nil
}

// EvaluateDailyCoverageDrop measures a settled day's recording coverage.
//
// Settled days only: today is still being written, and alarming on a partial day
// would fire every morning and resolve every afternoon.
func (s *Service) EvaluateDailyCoverageDrop(ctx context.Context, channelID id.ID, now time.Time, lookback time.Duration) (AlertObservation, error) {
	day := now.UTC().Add(-lookback).Truncate(24 * time.Hour)
	var owed, covered int64
	err := s.DB.Pool.QueryRow(ctx, `
		SELECT COALESCE(owed_ms,0),COALESCE(covered_ms,0) FROM reliability_daily
		WHERE channel_id=$1 AND kind='recording' AND day=$2::date`,
		channelID, day.Format("2006-01-02")).Scan(&owed, &covered)
	if errors.Is(err, pgx.ErrNoRows) {
		return AlertObservation{WindowStart: day}, nil
	}
	if err != nil {
		return AlertObservation{}, err
	}
	out := AlertObservation{WindowStart: day, WindowEnd: &day}
	if owed <= 0 {
		// Nothing was owed that day, so there is no coverage to be low.
		return out, nil
	}
	out.Value = float64(covered) / float64(owed) * 100
	out.Breaching = true
	return out, nil
}

// EvaluateAlerts runs every enabled rule and records what it finds.
//
// The dedup is structural: an alert already firing for the same rule and channel
// has its observed value refreshed rather than a second row created, which is
// what REL-05's "deduplicate ongoing faults" means. Notification is left to the
// caller — this records the crossing, and the delivery layer decides how to
// announce the first one.
func (s *Service) EvaluateAlerts(ctx context.Context, now time.Time) ([]Alert, error) {
	if s.DB == nil {
		return nil, nil
	}
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT r.id,r.channel_id,r.kind,r.threshold_ms,r.threshold_percent
		FROM reliability_alert_rules r
		WHERE r.enabled`)
	if err != nil {
		return nil, err
	}
	type rule struct {
		id               id.ID
		channelID        *id.ID
		kind             string
		thresholdMS      *int64
		thresholdPercent *float64
	}
	var rules []rule
	for rows.Next() {
		var item rule
		if err := rows.Scan(&item.id, &item.channelID, &item.kind, &item.thresholdMS, &item.thresholdPercent); err != nil {
			rows.Close()
			return nil, err
		}
		rules = append(rules, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var fired []Alert
	for _, rule := range rules {
		channels, err := s.ruleChannels(ctx, rule.channelID)
		if err != nil {
			return fired, err
		}
		for _, channelID := range channels {
			if ctx.Err() != nil {
				return fired, ctx.Err()
			}
			observation, err := s.observeForRule(ctx, rule.kind, channelID, now)
			if err != nil {
				return fired, err
			}
			threshold := 0.0
			if rule.thresholdMS != nil {
				threshold = float64(*rule.thresholdMS)
			}
			if rule.thresholdPercent != nil {
				threshold = *rule.thresholdPercent
			}
			breaching := observation.Breaching && observation.Value >= threshold
			// Coverage is a floor rather than a ceiling: it fires when the value
			// falls BELOW the threshold, which is the opposite direction from
			// every duration rule.
			if rule.kind == AlertDailyCoverageDrop {
				breaching = observation.Breaching && observation.Value < threshold
			}
			alert, err := s.recordAlert(ctx, rule.id, channelID, rule.kind, threshold, observation, breaching, now)
			if err != nil {
				return fired, err
			}
			if alert != nil && alert.State == "firing" {
				fired = append(fired, *alert)
			}
		}
	}
	return fired, nil
}

func (s *Service) observeForRule(ctx context.Context, kind string, channelID id.ID, now time.Time) (AlertObservation, error) {
	switch kind {
	case AlertMainDownStreak:
		return s.EvaluateMainDownStreak(ctx, channelID, now)
	case AlertRecordingTimeout:
		return s.EvaluateRecordingTimeout(ctx, channelID, now)
	case AlertDailyCoverageDrop:
		// Yesterday, because today has not settled.
		return s.EvaluateDailyCoverageDrop(ctx, channelID, now, 24*time.Hour)
	case AlertMonitoringStopped:
		return s.EvaluateMonitoringStopped(ctx, channelID, now)
	}
	// An unknown kind cannot be judged; reporting no breach is right, because a
	// rule this build does not understand must not produce an alarm.
	return AlertObservation{WindowStart: now}, nil
}

// ruleChannels resolves a rule's scope: one channel, or every channel that has
// ever been observed when the rule is site-wide.
func (s *Service) ruleChannels(ctx context.Context, channelID *id.ID) ([]id.ID, error) {
	if channelID != nil {
		return []id.ID{*channelID}, nil
	}
	rows, err := s.DB.Pool.Query(ctx, "SELECT id FROM channels WHERE enabled ORDER BY channel_no")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []id.ID
	for rows.Next() {
		var ch id.ID
		if err := rows.Scan(&ch); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// recordAlert opens, refreshes or resolves the alert for one rule and channel.
//
// Returns the alert when it is firing, so the caller can decide about notifying;
// nil when nothing is firing or the breach resolved.
func (s *Service) recordAlert(ctx context.Context, ruleID, channelID id.ID, kind string, threshold float64, observation AlertObservation, breaching bool, now time.Time) (*Alert, error) {
	var out *Alert
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var existing Alert
		var existingPeak float64
		err := tx.QueryRow(ctx, `
			SELECT id,started_at,observed_value,peak_value FROM reliability_alerts
			WHERE rule_id=$1 AND channel_id=$2 AND state='firing' FOR UPDATE`,
			ruleID, channelID).Scan(&existing.ID, &existing.Started, &existing.Observed, &existingPeak)
		switch {
		case err == nil && breaching:
			// Still breaching: refresh, keeping the highest value seen so a
			// resolved alert can show how bad it got.
			peak := existingPeak
			if observation.Value > peak {
				peak = observation.Value
			}
			if _, err := tx.Exec(ctx, `UPDATE reliability_alerts
					SET observed_value=$2,peak_value=$3,window_end=$4,updated_at=clock_timestamp()
					WHERE id=$1`, existing.ID, observation.Value, peak, observation.WindowEnd); err != nil {
				return err
			}
			existing.Observed = observation.Value
			existing.Peak = peak
			out = &existing
			return nil
		case err == nil && !breaching:
			// Recovered. Kept as history, never deleted (REL-05: recovery leaves
			// a record).
			if _, err := tx.Exec(ctx, `UPDATE reliability_alerts
					SET state='resolved',ended_at=$2,observed_value=$3,updated_at=clock_timestamp()
					WHERE id=$1`, existing.ID, now, observation.Value); err != nil {
				return err
			}
			return nil
		case errors.Is(err, pgx.ErrNoRows) && breaching:
			alertID, idErr := id.New()
			if idErr != nil {
				return idErr
			}
			windowEnd := observation.WindowEnd
			if _, err := tx.Exec(ctx, `INSERT INTO reliability_alerts
					(id,rule_id,channel_id,kind,state,started_at,window_start,window_end,threshold_value,observed_value,peak_value)
					VALUES($1,$2,$3,$4,'firing',$5,$5,$6,$7,$8,$8)`,
				alertID, ruleID, channelID, kind, now, windowEnd, threshold, observation.Value); err != nil {
				return err
			}
			out = &Alert{ID: alertID, RuleID: ruleID, ChannelID: channelID, Kind: kind,
				State: "firing", Started: now, WindowStart: now, WindowEnd: windowEnd,
				Threshold: threshold, Observed: observation.Value, Peak: observation.Value}
			return nil
		case errors.Is(err, pgx.ErrNoRows) && !breaching:
			// Nothing firing and nothing breaching: nothing to record.
			return nil
		default:
			return err
		}
	})
	return out, err
}

// ListAlerts returns alerts inside a window, newest first.
func (s *Service) ListAlerts(ctx context.Context, p auth.Principal, channelID *id.ID, from, to time.Time) ([]Alert, error) {
	out := []Alert{}
	if s.DB == nil || s.Auth == nil || !to.After(from) {
		return out, auth.ErrInvalid
	}
	// An alert about a channel's media is governed by the same grant that lets
	// the operator see that media; a cross-channel list is an administrator's
	// comparison.
	if channelID != nil {
		if err := s.Auth.RequireChannel(ctx, p, *channelID, auth.Playback); err != nil {
			return out, err
		}
	} else if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT a.id,a.rule_id,a.channel_id,c.channel_no,a.kind,a.state,a.started_at,a.ended_at,
			a.window_start,a.window_end,a.threshold_value,a.observed_value,a.peak_value,
			a.notified_at IS NOT NULL
		FROM reliability_alerts a JOIN channels c ON c.id=a.channel_id
		WHERE a.started_at < $3 AND COALESCE(a.ended_at,$3) > $2
			AND ($1::uuid IS NULL OR a.channel_id=$1)
		ORDER BY a.started_at DESC,a.id DESC LIMIT 500`,
		channelID, from.UTC(), to.UTC())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Alert
		if err := rows.Scan(&item.ID, &item.RuleID, &item.ChannelID, &item.ChannelNo, &item.Kind, &item.State,
			&item.Started, &item.Ended, &item.WindowStart, &item.WindowEnd,
			&item.Threshold, &item.Observed, &item.Peak, &item.Notified); err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
