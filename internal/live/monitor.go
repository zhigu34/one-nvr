package live

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"sync"
	"time"
)

func (s *Service) invalidate(ctx context.Context) error {
	_, err := s.DB.Pool.Exec(ctx, `UPDATE live_sessions expired SET state='closing' WHERE expired.state IN ('pending','active') AND (expired.expires_at<=clock_timestamp() OR NOT EXISTS(SELECT 1`+joins+`WHERE l.id=expired.id AND `+validity+`))`)
	return err
}
func (s *Service) Reap(ctx context.Context) error {
	if err := s.invalidate(ctx); err != nil {
		return err
	}
	return s.cleanup(ctx)
}
func (s *Service) cleanup(ctx context.Context) error {
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,peer_id,peer_token FROM live_sessions WHERE state='closing' AND peer_id IS NOT NULL AND cleanup_after<=clock_timestamp() ORDER BY cleanup_after,created_at LIMIT 8`)
	if err != nil {
		return err
	}
	type pending struct {
		id   id.ID
		peer zlm.RTCPeer
	}
	var peers []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.peer.ID, &p.peer.Token); err != nil {
			rows.Close()
			return err
		}
		peers = append(peers, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var group sync.WaitGroup
	for _, p := range peers {
		group.Go(func() {
			bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
			deletionErr := s.Media.CloseRTC(bounded, p.peer)
			cancel()
			if deletionErr == nil {
				s.DB.Pool.Exec(ctx, `UPDATE live_sessions SET state='closed',peer_token=NULL,peer_id=NULL WHERE id=$1 AND state='closing'`, p.id)
			} else {
				s.DB.Pool.Exec(ctx, `UPDATE live_sessions SET cleanup_after=clock_timestamp()+interval '10 seconds' WHERE id=$1 AND state='closing'`, p.id)
			}
		})
	}
	group.Wait()
	_, err = s.DB.Pool.Exec(ctx, `UPDATE live_sessions SET state='closed' WHERE state='closing' AND peer_id IS NULL AND created_at<clock_timestamp()-interval '1 minute'; DELETE FROM live_sessions WHERE state='closed' AND created_at<clock_timestamp()-interval '1 day'`)
	return err
}
func (s *Service) Monitor(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var group sync.WaitGroup
	defer group.Wait()
	completed := make(chan struct{}, 1)
	busy := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-completed:
			busy = false
		case <-ticker.C:
			// Authorization scanning never waits for potentially unavailable media HTTP.
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			s.invalidate(bounded)
			cancel()
			if !busy {
				busy = true
				group.Go(func() { s.cleanup(ctx); completed <- struct{}{} })
			}
		}
	}
}
