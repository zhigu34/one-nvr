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
// already exist: raw observations for drill-down, and the intervals and faults
// that summaries are computed from. Minute and daily roll-ups belong to a later
// batch — there is nothing to roll up until the facts below them are complete,
// and a summary computed from partial facts would be a number nobody can
// recompute.
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
