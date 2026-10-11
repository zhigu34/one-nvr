package recording

import (
	"context"
	"log/slog"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// RetentionSweepInterval is how often the worker looks for expired recordings.
// Retention is measured in days, so a few minutes of latency is irrelevant while
// a slow sweep against a large index would be wasteful.
const RetentionSweepInterval = 5 * time.Minute

// retentionBatch bounds one sweep. Deleting is I/O-bound and unbounded work
// would hold a worker for an arbitrarily long time on first enable, when every
// old recording is suddenly expired; successive sweeps drain the backlog.
const retentionBatch = 200

// RunRetention deletes recordings older than the site's retention period.
//
// It is disabled by default (0 days) and therefore does nothing at all on an
// upgraded site until an administrator sets a period: automatic deletion of
// existing footage is not a decision an upgrade may make for the operator.
//
// Retention never touches protected evidence, never touches a segment inside
// FreshnessDelay, and never cascades to any other copy. Each deletion is
// recorded per file so the audit trail stays per-segment rather than a bulk
// claim, and a failure is retried on the next sweep instead of being lost.
func (s *Service) RunRetention(ctx context.Context) {
	ticker := time.NewTicker(RetentionSweepInterval)
	defer ticker.Stop()
	for {
		s.SweepRetention(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SweepRetention performs one retention pass. It is exported so a test can run
// the sweep deterministically instead of waiting for the interval, and so an
// operator-facing trigger can reuse it later.
func (s *Service) SweepRetention(ctx context.Context) {
	if s.Auth == nil || s.Pools == nil || s.DB == nil {
		return
	}
	days, err := s.retentionDays(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("retention window unavailable")
		}
		return
	}
	if days <= 0 {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Add(-FreshnessDelay)
	for {
		expired, err := s.expiredSegments(ctx, cutoff, retentionBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("retention candidate scan unavailable")
			}
			return
		}
		if len(expired) == 0 {
			return
		}
		outcomes := s.deleteLocal(ctx, "", expired, "retention")
		if ctx.Err() != nil {
			return
		}
		// A sweep that deleted nothing means every candidate was kept for a
		// permanent reason (protected, or a file that vanished on its own), so
		// ending here avoids rescanning the same rows forever.
		progressed := false
		for _, outcome := range outcomes {
			if outcome.State == deleteStateDeleted {
				progressed = true
				break
			}
		}
		if !progressed {
			return
		}
		if len(expired) < retentionBatch {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (s *Service) retentionDays(ctx context.Context) (int, error) {
	var days int
	err := s.DB.Pool.QueryRow(ctx, "SELECT recording_retention_days FROM sites WHERE id=$1", s.siteID).Scan(&days)
	return days, err
}

// expiredSegments lists deletable candidates: published, unprotected, older than
// the cutoff, and on an enabled pool. The age cutoff carries the freshness delay
// as well, so a segment that just finished is never a candidate.
func (s *Service) expiredSegments(ctx context.Context, cutoff time.Time, limit int) ([]id.ID, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT s.id FROM recording_segments s JOIN storage_pools p ON p.id=s.pool_id WHERE s.state='ready' AND NOT s.protected AND s.end_at<$1 AND p.enabled ORDER BY s.end_at LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]id.ID, 0, limit)
	for rows.Next() {
		var segment id.ID
		if err := rows.Scan(&segment); err != nil {
			return nil, err
		}
		out = append(out, segment)
	}
	return out, rows.Err()
}
