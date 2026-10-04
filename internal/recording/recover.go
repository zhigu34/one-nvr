package recording

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// Recover only consumes durable registered callbacks. It never scans arbitrary
// mounted content, and attempts stay retryable regardless of attempt count.
func (s *Service) Recover(ctx context.Context) error {
	if s.Probe == nil || s.Pools == nil {
		return ErrPublicationUnavailable
	}
	discoveryErr := s.discover(ctx)
	rows, err := s.DB.Pool.Query(ctx, `SELECT h.id FROM hook_inbox h JOIN recording_runs r ON r.id=h.run_id WHERE r.site_id=$1 AND r.purpose='continuous' AND ((h.state='pending' AND h.available_at<=clock_timestamp()) OR (h.state='processing' AND h.lease_expires_at<clock_timestamp())) ORDER BY h.available_at,h.id LIMIT 100`, s.siteID)
	if err != nil {
		return err
	}
	var ids []id.ID
	for rows.Next() {
		var value id.ID
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, value)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var first error
	for _, value := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := s.Publish(ctx, value); err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		return first
	}
	return discoveryErr
}
func (s *Service) RunPublisher(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.Recover(ctx); err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			slog.Warn("recording publication requires retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
