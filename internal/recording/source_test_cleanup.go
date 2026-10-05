package recording

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

// CleanupSourceTests retains bounded source.test retries while keeping the
// durable physical cleanup intent. Only terminal test jobs and their immutable
// private UUID sessions qualify; configured main/sub sessions never qualify.
func (s *Service) CleanupSourceTests(ctx context.Context) error {
	if s.Media == nil {
		return ErrPublicationUnavailable
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT ss.id,ss.channel_id,ss.source_revision_id,ss.vhost,ss.app,ss.stream,ss.state FROM stream_sessions ss JOIN source_tests t ON t.id=ss.source_test_id JOIN jobs j ON j.id=t.job_id JOIN channels c ON c.id=ss.channel_id WHERE c.site_id=$1 AND ss.purpose='test' AND ss.state NOT IN ('closed','failed') AND j.state IN ('succeeded','failed','cancelled') ORDER BY ss.created_at LIMIT 50`, s.siteID)
	if err != nil {
		return err
	}
	var sessions []physicalSession
	for rows.Next() {
		ss, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, ss)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var first error
	for _, ss := range sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.cleanupTerminalTest(ctx, ss); err != nil && first == nil {
			first = err
		}
	}
	// A crash can exhaust the finite test before any physical session exists.
	// Finalize only after all mapped cleanup receipts have been closed.
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE source_tests t SET state=CASE WHEN j.state='cancelled' THEN 'cancelled' ELSE 'failed' END,error_code='test_execution_expired',result='{"main":{"state":"unavailable","first_frame":false,"reason":"interrupted_test"},"sub":{"state":"pending","first_frame":false}}' FROM jobs j,channels c WHERE t.job_id=j.id AND t.channel_id=c.id AND c.site_id=$1 AND t.state IN ('queued','testing') AND j.state IN ('failed','cancelled') AND NOT EXISTS(SELECT 1 FROM stream_sessions ss WHERE ss.source_test_id=t.id AND ss.state NOT IN ('closed','failed'))`, s.siteID); err != nil && first == nil {
		first = err
	}
	return first
}
func (s *Service) cleanupTerminalTest(ctx context.Context, ss physicalSession) error {
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := s.DB.Pool.Acquire(bounded)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(bounded, "SELECT pg_try_advisory_lock(hashtextextended($1,170019))", string(ss.ChannelID)).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer func() {
		release, finish := context.WithTimeout(context.Background(), 2*time.Second)
		defer finish()
		var unlocked bool
		if err := conn.QueryRow(release, "SELECT pg_advisory_unlock(hashtextextended($1,170019))", string(ss.ChannelID)).Scan(&unlocked); err != nil || !unlocked {
			conn.Conn().Close(release)
		}
	}()
	var testID string
	if err := conn.QueryRow(bounded, `SELECT t.id::text FROM stream_sessions ss JOIN source_tests t ON t.id=ss.source_test_id JOIN jobs j ON j.id=t.job_id JOIN channels c ON c.id=ss.channel_id WHERE ss.id=$1 AND ss.channel_id=$2 AND c.site_id=$3 AND ss.purpose='test' AND ss.state NOT IN ('closed','failed') AND j.state IN ('succeeded','failed','cancelled')`, ss.ID, ss.ChannelID, s.siteID).Scan(&testID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if _, err := conn.Exec(bounded, "UPDATE stream_sessions SET state='closing',error_code='test_cleanup_required' WHERE id=$1", ss.ID); err != nil {
		return err
	}
	ref := zlm.ProxyRef{Key: ss.Key, OpaqueKey: ss.Key.VHost + "/" + ss.Key.App + "/" + ss.Key.Stream}
	if err := s.Media.RemoveProxy(bounded, ref); err != nil && !errors.Is(err, zlm.ErrProxyAbsent) {
		return err
	}
	tx, err := conn.Begin(bounded)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(bounded))
	if _, err := tx.Exec(bounded, "UPDATE stream_sessions SET state='closed',closed_at=clock_timestamp(),error_code=NULL WHERE id=$1", ss.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(bounded, `UPDATE source_tests SET state='failed',error_code='test_execution_expired',result='{"main":{"state":"unavailable","first_frame":false,"reason":"interrupted_test"},"sub":{"state":"pending","first_frame":false}}' WHERE id=$1 AND state IN ('queued','testing')`, testID); err != nil {
		return err
	}
	return tx.Commit(bounded)
}
func (s *Service) RunSourceTestCleanup(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.CleanupSourceTests(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("source test cleanup unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
