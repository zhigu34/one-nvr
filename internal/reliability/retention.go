package reliability

import (
	"context"
	"log/slog"
	"time"

	"github.com/zhigu34/one-nvr/internal/database"
)

// Retention, PRD REL-06's first stage.
//
// The chapter asks for four tiers and this batch delivers the two whose facts
// already exist: raw observations for drill-down, the intervals and faults that
// summaries are computed from, and (from batch 3) the minute and daily rollups
// those facts project into.
const (
	// ObservationRetention keeps raw samples long enough to answer "what exactly
	// did the sampler see" for a recent incident. The reader only ever wants the
	// newest row (a thirty-second TTL); this window is for a human investigating
	// something that happened last week.
	ObservationRetention = 7 * 24 * time.Hour
	// IntervalRetention and FaultRetention are the PRD's 365 days for state,
	// faults, gaps and verification evidence.
	IntervalRetention = 365 * 24 * time.Hour
	FaultRetention    = 365 * 24 * time.Hour
	// ObligationRetention matches the interval tier: an availability or
	// completeness figure divides a numerator by a denominator, and the two
	// expiring on different schedules would leave windows nobody can recompute.
	ObligationRetention = 365 * 24 * time.Hour
	// MinuteRetention and DailyRetention are the PRD's 30 and 730 days for the
	// summary tiers. They exist so a 90-day query does not have to re-read every
	// interval: beyond 30 days the answer comes from the rollups.
	MinuteRetention = 30 * 24 * time.Hour
	DailyRetention  = 730 * 24 * time.Hour
)

// retentionBatch bounds one sweep. The first run after upgrade may have a long
// backlog, and one enormous DELETE would hold locks and write-ahead log for an
// arbitrarily long time — the same reasoning the recording sweeps use.
const retentionBatch = 200

// Sweep removes data past its tier's window.
//
// Every delete is bounded and resumable: a partial pass simply continues on the
// next tick. Nothing here touches an *open* interval or fault, however old:
// an ongoing outage is current fact, not expired history, and deleting it would
// erase the running clock the operator is looking at.
func (s *Service) Sweep(ctx context.Context) {
	if s.DB == nil {
		return
	}
	s.sweepTable(ctx, "source_observations", "observed_at", ObservationRetention)
	s.sweepTable(ctx, "reliability_intervals", "ended_at", IntervalRetention)
	s.sweepTable(ctx, "fault_events", "ended_at", FaultRetention)
	// Obligations are the completeness denominator (REL-03), so they live in the
	// same 365-day tier as the intervals they are read against: a rate whose
	// denominator expired while its numerator survived would be incomputable.
	s.sweepTable(ctx, "recording_obligations", "ended_at", ObligationRetention)
	// The summary tiers (REL-06). Minute rows are recomputable from the
	// intervals above them and daily rows from the minutes, so sweeping either
	// costs precision, never correctness.
	s.sweepKeyed(ctx, "reliability_minutes", "minute", MinuteRetention)
	s.sweepKeyed(ctx, "reliability_daily", "day", DailyRetention)
	// Policy versions are metadata, not observations, and PRD REL-06 keeps
	// metadata that is still referenced: an obligation names the policy version
	// that formed it, so deleting the version would leave the obligation
	// unexplainable. Kept forever, and the table only grows when a policy
	// actually changes (single digits per channel per month at worst).
}

// sweepKeyed is the same bounded delete for tables whose age column is never
// null and whose key is composite.
//
// The interval tables need "ended_at IS NOT NULL" because an open row has no
// end and must survive however old it is. A rollup row has no such state: a
// minute either was summarised or was not, so age alone decides.
func (s *Service) sweepKeyed(ctx context.Context, table, column string, window time.Duration) {
	// Identifiers are constants from this file, never caller input.
	delete := "DELETE FROM " + table + " WHERE (" + column + ") IN (SELECT " + column +
		" FROM " + table + " WHERE " + column + " < clock_timestamp() - $1::interval LIMIT $2)"
	for {
		tag, err := s.DB.Pool.Exec(ctx, delete, window.String(), retentionBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("reliability retention sweep unavailable", "table", table, "error", err)
			}
			return
		}
		if tag.RowsAffected() < retentionBatch || ctx.Err() != nil {
			return
		}
	}
}

// sweepTable deletes rows older than the window in bounded batches.
//
// Closed rows only: the predicate requires the timestamp to be set, which for
// both tables means the row has ended. Read literally, that is what keeps an
// open outage — whose ended_at is null and whose start may be very old — out of
// the sweep.
func (s *Service) sweepTable(ctx context.Context, table, column string, window time.Duration) {
	// Identifiers are constants from this file, never caller input, so the
	// interpolation below cannot be reached by anything an operator sends.
	delete := "DELETE FROM " + table + " WHERE id IN (SELECT id FROM " + table +
		" WHERE " + column + " IS NOT NULL AND " + column + " < clock_timestamp() - $1::interval LIMIT $2)"
	for {
		tag, err := s.DB.Pool.Exec(ctx, delete, window.String(), retentionBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("reliability retention sweep unavailable", "table", table, "error", err)
			}
			return
		}
		if tag.RowsAffected() < retentionBatch || ctx.Err() != nil {
			return
		}
	}
}

// SweepInterval is how often the worker runs the retention pass. Retention is
// measured in days, so a slow tick is plenty and a fast one would only add
// query load.
const SweepInterval = time.Hour

// RunRetention drives the sweep until the context ends.
func RunRetention(ctx context.Context, db *database.DB) {
	service := New(db, nil)
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()
	for {
		service.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
