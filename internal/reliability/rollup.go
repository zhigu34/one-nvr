package reliability

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Rollups: the projection that makes long windows cheap (PRD REL-06).
//
// Design constraints, all from the PRD and all structural rather than stylistic:
//
//   - Nothing reads a rollup to decide anything. It is a cache of a
//     recomputable answer, so a divergence is a projection bug, never a new
//     truth. This is what "accumulated snapshots must not cover for the raw
//     facts" means in practice.
//   - The work is bounded and incremental, and it only reads facts and writes
//     summary rows. There is no queue between the recorder and this loop, so a
//     slow database cannot block recording, uploads or configuration jobs.
//   - Every row records when it was computed and the newest fact it included,
//     so a figure crossing the retention boundary can state its basis.
//
// Only *sealed* minutes are summarised — a minute that has fully elapsed. A
// partial minute would produce a row that changes value as the minute fills,
// and a reader comparing two queries a second apart would see the history move.

// RollupInterval is how often sealed minutes are summarised. Minutes are the
// finest tier, so a pass a minute behind is enough; running faster would only
// rewrite rows that cannot have changed.
const RollupInterval = time.Minute

// rollupBatch bounds how many minutes one pass folds into the daily rows.
const rollupBatch = 500

// rollupMinute reconciles one sealed minute for every channel and stream kind.
//
// The accounting is derived with the same union/subtract arithmetic the report
// uses, so a rollup and a direct computation of the same window agree by
// construction rather than by coincidence.
func (s *Service) rollupMinute(ctx context.Context, minute time.Time) error {
	if s.DB == nil {
		return nil
	}
	from, to := minute.UTC(), minute.UTC().Add(time.Minute)
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		// Which channels and kinds have any fact in this minute. A minute with
		// no rows at all writes nothing: an all-zero row is indistinguishable
		// from "not yet summarised", and a reader needs those to differ.
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT channel_id,kind FROM reliability_intervals
			WHERE started_at < $2 AND COALESCE(ended_at,$2) > $1
			UNION
			SELECT DISTINCT channel_id,kind FROM fault_events
			WHERE started_at < $2 AND COALESCE(ended_at,$2) > $1`, from, to)
		if err != nil {
			return err
		}
		type key struct {
			channel id.ID
			kind    string
		}
		var keys []key
		for rows.Next() {
			var item key
			if err := rows.Scan(&item.channel, &item.kind); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range keys {
			if err := s.rollupMinuteFor(ctx, tx, item.channel, item.kind, from, to); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) rollupMinuteFor(ctx context.Context, tx pgx.Tx, channelID id.ID, kind string, from, to time.Time) error {
	var healthy, unavailable, unknown, excluded int64
	if err := tx.QueryRow(ctx, `
		SELECT
			COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$3),$3) - GREATEST(started_at,$2)))*1000)) FILTER (WHERE state='healthy' AND exclusion IS NULL),0)::bigint,
			COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$3),$3) - GREATEST(started_at,$2)))*1000)) FILTER (WHERE state='unavailable' AND exclusion IS NULL),0)::bigint,
			COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$3),$3) - GREATEST(started_at,$2)))*1000)) FILTER (WHERE state='unknown' AND exclusion IS NULL),0)::bigint,
			COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$3),$3) - GREATEST(started_at,$2)))*1000)) FILTER (WHERE exclusion IS NOT NULL),0)::bigint
		FROM reliability_intervals
		WHERE channel_id=$1 AND kind=$4 AND started_at < $3 AND COALESCE(ended_at,$3) > $2`,
		channelID, from, to, kind).Scan(&healthy, &unavailable, &unknown, &excluded); err != nil {
		return err
	}
	// Intervals never overlap for one channel and kind (a partial unique index
	// guarantees one open row), so summing them cannot double-count. The
	// clipping to the minute keeps a spanning interval from being billed to
	// every minute it crosses.

	var owed, covered, unverified int64
	if kind == "recording" {
		// Obligations do not overlap either: one open row per channel.
		if err := tx.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$3),$3) - GREATEST(started_at,$2)))*1000)) FILTER (WHERE exclusion IS NULL),0)::bigint,
				COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(COALESCE(ended_at,$3),$3) - GREATEST(started_at,$2)))*1000)) FILTER (WHERE exclusion IS NOT NULL),0)::bigint
			FROM recording_obligations
			WHERE channel_id=$1 AND started_at < $3 AND COALESCE(ended_at,$3) > $2`,
			channelID, from, to).Scan(&owed, &excluded); err != nil {
			return err
		}
		// Covered recording is the intersection of owed time with usable media.
		// Measured in SQL because the intervals live in a different table shape
		// than the segments; the arithmetic matches ComputeCoverage's contract.
		if err := tx.QueryRow(ctx, `
			WITH owed_spans AS (
				SELECT GREATEST(started_at,$2) AS s, LEAST(COALESCE(ended_at,$3),$3) AS e
				FROM recording_obligations
				WHERE channel_id=$1 AND exclusion IS NULL AND started_at < $3 AND COALESCE(ended_at,$3) > $2
			), media AS (
				SELECT GREATEST(start_at,$2) AS s, LEAST(end_at,$3) AS e,
					(time_evidence='provisional') AS provisional
				FROM recording_segments
				WHERE channel_id=$1 AND state IN ('ready','deleted') AND start_at < $3 AND end_at > $2
			)
			SELECT
				COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(o.e,m.e) - GREATEST(o.s,m.s)))*1000)) FILTER (WHERE NOT m.provisional),0)::bigint,
				COALESCE(SUM(GREATEST(0,EXTRACT(EPOCH FROM (LEAST(o.e,m.e) - GREATEST(o.s,m.s)))*1000)) FILTER (WHERE m.provisional),0)::bigint
			FROM owed_spans o JOIN media m ON o.s < m.e AND m.s < o.e`,
			channelID, from, to).Scan(&covered, &unverified); err != nil {
			return err
		}
		// The join counts a minute once per matching pair, and two overlapping
		// segments cannot exist for one channel (the state machine moves one
		// file at a time), so covered+unverified cannot exceed the owed span.
		if covered+unverified > owed {
			// Rather than silently write an impossible row, clamp and complain:
			// an overlap that breaks this invariant is a defect worth seeing.
			slog.Warn("recording rollup exceeded its obligation span",
				"channel", channelID, "kind", kind, "minute", from,
				"covered", covered, "unverified", unverified, "owed", owed)
			if covered > owed {
				covered = owed
			}
			if covered+unverified > owed {
				unverified = owed - covered
			}
		}
	}

	var faultCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM fault_events
		WHERE channel_id=$1 AND kind=$2 AND NOT maintenance
			AND started_at < $4 AND COALESCE(ended_at,$4) > $3`,
		channelID, kind, from, to).Scan(&faultCount); err != nil {
		return err
	}

	_, err := tx.Exec(ctx, `INSERT INTO reliability_minutes
			(channel_id,kind,minute,healthy_ms,unavailable_ms,unknown_ms,excluded_ms,owed_ms,covered_ms,unverified_ms,fault_count,computed_at,input_watermark,metric_version)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,clock_timestamp(),$12,$13)
			ON CONFLICT(channel_id,kind,minute) DO UPDATE SET
				healthy_ms=EXCLUDED.healthy_ms, unavailable_ms=EXCLUDED.unavailable_ms,
				unknown_ms=EXCLUDED.unknown_ms, excluded_ms=EXCLUDED.excluded_ms,
				owed_ms=EXCLUDED.owed_ms, covered_ms=EXCLUDED.covered_ms,
				unverified_ms=EXCLUDED.unverified_ms, fault_count=EXCLUDED.fault_count,
				computed_at=EXCLUDED.computed_at, input_watermark=EXCLUDED.input_watermark,
				metric_version=EXCLUDED.metric_version`,
		channelID, kind, from, healthy, unavailable, unknown, excluded, owed, covered, unverified, faultCount, to, MetricVersion)
	return err
}

// RollupMinutes summarises every sealed minute in [from, to).
//
// The range is explicit rather than "everything since X up to now", which was
// the previous shape and a trap: a caller passing a timestamp far in the past
// silently asked for a walk of every minute in between, one transaction each.
// A production loop passing time.Time{} was fine; the first test that passed a
// real old timestamp hung. Naming both ends makes the cost of a call visible at
// the call site.
//
// Minutes at or after `to` are left alone — they have not sealed, and
// summarising a partial minute would write a row whose value changes as the
// minute fills.
func (s *Service) RollupMinutes(ctx context.Context, from, to time.Time) error {
	if s.DB == nil {
		return nil
	}
	start := from.UTC().Truncate(time.Minute)
	end := to.UTC().Truncate(time.Minute)
	if start.IsZero() || start.After(end) {
		return nil
	}
	for minute := start; minute.Before(end); minute = minute.Add(time.Minute) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.rollupMinute(ctx, minute); err != nil {
			return err
		}
	}
	return nil
}

// rollupLookback bounds one pass of the background loop.
//
// A worker that has been down must catch up, but not in a single unbounded
// pass: the loop runs every minute, so a backlog drains a bounded slice at a
// time while live minutes keep being summarised. This is the same
// bounded-batch discipline the retention sweeps use.
const rollupLookback = 2 * time.Hour

// RollupPending summarises every sealed minute that has not been summarised
// yet, in bounded slices.
//
// The watermark is read from the tables rather than assumed: a fixed lookback
// window would silently skip history whenever the gap exceeds it, which is
// exactly the case for a site whose worker was down for a day, or a fresh
// install whose only facts are a few hours old. The oldest unsummarised minute
// is the newer of "the earliest fact that exists" and "the oldest minute still
// inside the retention window" — there is no point summarising minutes that
// have already been swept, and no way to read facts that are gone.
func (s *Service) RollupPending(ctx context.Context, now time.Time) error {
	if s.DB == nil {
		return nil
	}
	end := now.UTC().Truncate(time.Minute)
	// NULL rather than infinity for "no rows": PostgreSQL's +/-infinity cannot be
	// scanned into a Go time.Time, and a fresh install legitimately has neither
	// summarised minutes nor observed intervals — the condition this function
	// has to handle most often. GREATEST ignores NULLs, so the two sides combine
	// to the later of whatever exists, and NULL when neither does.
	var watermark *time.Time
	if err := s.DB.Pool.QueryRow(ctx, `
		SELECT GREATEST(
			(SELECT MAX(minute) FROM reliability_minutes),
			(SELECT MIN(started_at) FROM reliability_intervals) - interval '1 minute'
		) AS from_minute`).Scan(&watermark); err != nil {
		return err
	}
	if watermark == nil {
		// Nothing to summarise anywhere; the next tick will find facts if any
		// arrive.
		return nil
	}
	start := watermark.UTC().Truncate(time.Minute)
	retentionFloor := now.UTC().Add(-MinuteRetention).Truncate(time.Minute)
	if start.Before(retentionFloor) {
		// Older rows have been swept; their facts are gone, so there is nothing
		// to summarise and pretending otherwise would write empty days.
		start = retentionFloor
	}
	if !start.Before(end) {
		return nil
	}
	// One bounded slice per pass; the loop runs every minute, so a backlog
	// drains steadily without ever holding a long transaction.
	if start.Add(rollupLookback).Before(end) {
		end = start.Add(rollupLookback)
	}
	return s.RollupMinutes(ctx, start, end)
}

// FinalizeDaily derives daily rows from the minute rows that exist.
//
// Derived from minutes rather than from intervals so the two tiers cannot
// disagree about the same day, and so the daily tier keeps working after the
// minute tier has been swept.
//
// No age filter. An earlier version folded only minutes older than the minute
// retention window, on the reasoning that "a day is finalised once it is about
// to be swept" — which had it backwards: it meant recent days produced no daily
// rows at all, so the trend and cross-channel endpoints were empty on a healthy
// site. The correct picture is that the daily row is *maintained* while its
// minutes still exist and simply stops changing once they are swept, having
// already accumulated everything.
//
// A day still in progress is included for the same reason: a trend chart has to
// show today. It is recomputed on every pass, and its watermark says so.
func (s *Service) FinalizeDaily(ctx context.Context) error {
	if s.DB == nil {
		return nil
	}
	if _, err := s.DB.Pool.Exec(ctx, `
		WITH daily AS (
			SELECT channel_id,kind,
				date_trunc('day',minute)::date AS day,
				date_trunc('day',minute) AS day_start,
				SUM(healthy_ms) AS healthy_ms, SUM(unavailable_ms) AS unavailable_ms,
				SUM(unknown_ms) AS unknown_ms, SUM(excluded_ms) AS excluded_ms,
				SUM(owed_ms) AS owed_ms, SUM(covered_ms) AS covered_ms,
				SUM(unverified_ms) AS unverified_ms, SUM(fault_count) AS fault_count,
				MAX(input_watermark) AS input_watermark
			FROM reliability_minutes
			GROUP BY channel_id,kind,date_trunc('day',minute)
		), worst AS (
			-- The worst single outage that touched each day, clipped to the day.
			-- Clipping matters: a fault spanning midnight is two days' problem, and
			-- reporting its full length against both would make "sum of daily
			-- worst" exceed any real outage.
			SELECT f.channel_id,f.kind,
				date_trunc('day',f.started_at + (g.n || ' day')::interval)::date AS day,
				MAX(GREATEST(0,EXTRACT(EPOCH FROM (
					LEAST(COALESCE(f.ended_at,clock_timestamp()), date_trunc('day',f.started_at + (g.n || ' day')::interval) + interval '1 day')
					- GREATEST(f.started_at, date_trunc('day',f.started_at + (g.n || ' day')::interval))
				))*1000))::bigint AS longest_fault_ms
			FROM fault_events f
			CROSS JOIN generate_series(0,8) AS g(n)
			WHERE NOT f.maintenance
				AND f.started_at < date_trunc('day',f.started_at + (g.n || ' day')::interval) + interval '1 day'
				AND COALESCE(f.ended_at,clock_timestamp()) > date_trunc('day',f.started_at + (g.n || ' day')::interval)
			GROUP BY f.channel_id,f.kind,date_trunc('day',f.started_at + (g.n || ' day')::interval)::date
		)
		INSERT INTO reliability_daily
			(channel_id,kind,day,healthy_ms,unavailable_ms,unknown_ms,excluded_ms,owed_ms,covered_ms,unverified_ms,fault_count,longest_fault_ms,computed_at,input_watermark,metric_version)
		SELECT d.channel_id,d.kind,d.day,d.healthy_ms,d.unavailable_ms,d.unknown_ms,d.excluded_ms,
			d.owed_ms,d.covered_ms,d.unverified_ms,d.fault_count,COALESCE(w.longest_fault_ms,0),
			clock_timestamp(),d.input_watermark,$1
		FROM daily d LEFT JOIN worst w ON w.channel_id=d.channel_id AND w.kind=d.kind AND w.day=d.day
		ON CONFLICT(channel_id,kind,day) DO UPDATE SET
			healthy_ms=EXCLUDED.healthy_ms, unavailable_ms=EXCLUDED.unavailable_ms,
			unknown_ms=EXCLUDED.unknown_ms, excluded_ms=EXCLUDED.excluded_ms,
			owed_ms=EXCLUDED.owed_ms, covered_ms=EXCLUDED.covered_ms,
			unverified_ms=EXCLUDED.unverified_ms, fault_count=EXCLUDED.fault_count,
			longest_fault_ms=EXCLUDED.longest_fault_ms,
			computed_at=EXCLUDED.computed_at, input_watermark=EXCLUDED.input_watermark,
			metric_version=EXCLUDED.metric_version`,
		MetricVersion); err != nil {
		return err
	}
	return nil
}

// RunRollups drives the minute and daily passes until the context ends.
func RunRollups(ctx context.Context, db *database.DB) {
	service := New(db, nil)
	ticker := time.NewTicker(RollupInterval)
	defer ticker.Stop()
	for {
		if err := service.RollupPending(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			slog.Warn("minute rollup unavailable", "error", err)
		}
		if err := service.FinalizeDaily(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("daily rollup unavailable", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// AlertInterval is how often configured thresholds are evaluated.
//
// The same cadence as the sampler, because two of the four rule kinds describe a
// state happening right now: a stream that went down should alarm within a
// sampling interval, not within an hour. The two period-based kinds tolerate it
// easily — they read a settled day, which cannot change between passes.
const AlertInterval = 30 * time.Second

// RunAlerts evaluates the configured thresholds until the context ends.
//
// Failures are logged and retried rather than fatal: an alerting loop that stops
// on a database blip is worse than one that misses a pass, since the next tick
// re-evaluates the same conditions from the same facts.
func RunAlerts(ctx context.Context, db *database.DB) {
	service := New(db, nil)
	ticker := time.NewTicker(AlertInterval)
	defer ticker.Stop()
	for {
		if _, err := service.EvaluateAlerts(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			slog.Warn("reliability alert evaluation unavailable", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
