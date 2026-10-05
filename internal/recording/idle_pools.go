package recording

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/id"
	"log/slog"
	"time"
)

// Idle pools have no ordinary MP4 completions to refresh media proof. An explicit
// temporary probe uses the same checked source/write/MP4 path as the Test button.
func (s *Service) MonitorIdlePools(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		rows, err := s.DB.Pool.Query(ctx, `SELECT p.id FROM storage_pools p WHERE EXISTS(SELECT 1 FROM pool_probe_intents pi JOIN stream_sessions ss ON ss.id=pi.session_id WHERE pi.pool_id=p.id AND ss.state<>'closed') OR EXISTS(SELECT 1 FROM recording_runs r WHERE r.pool_id=p.id AND r.purpose='probe' AND r.state IN ('starting','recording','stopping')) OR (p.enabled AND EXISTS(SELECT 1 FROM channels c JOIN recording_policies rp ON rp.channel_id=c.id WHERE c.enabled AND c.storage_pool_id=p.id AND rp.mode='continuous') AND NOT EXISTS(SELECT 1 FROM recording_runs r WHERE r.pool_id=p.id AND r.purpose='continuous' AND r.state IN ('starting','recording','stopping')) AND NOT EXISTS(SELECT 1 FROM storage_pool_checks c WHERE c.pool_id=p.id AND c.service='zlm' AND c.expires_at>clock_timestamp())) ORDER BY p.id`)
		var pools []id.ID
		if err == nil {
			for rows.Next() {
				var pool id.ID
				if err = rows.Scan(&pool); err != nil {
					break
				}
				pools = append(pools, pool)
			}
			rows.Close()
			if err == nil {
				err = rows.Err()
			}
		}
		if err == nil {
			for _, pool := range pools {
				probe, cancel := context.WithTimeout(ctx, 20*time.Second)
				owned, release, ownErr := s.ownPoolProbe(probe, pool)
				if ownErr == nil {
					err = s.recoverPoolProbe(owned, pool)
					release()
				} else {
					err = ownErr
				}
				var wanted bool
				if err == nil {
					err = s.DB.Pool.QueryRow(probe, `SELECT EXISTS(SELECT 1 FROM channels c JOIN recording_policies rp ON rp.channel_id=c.id JOIN storage_pools p ON p.id=c.storage_pool_id WHERE c.enabled AND p.enabled AND p.id=$1 AND rp.mode='continuous')`, pool).Scan(&wanted)
				}
				if err == nil && wanted {
					err = s.CheckPool(probe, pool)
				}
				cancel()
				if err != nil && ctx.Err() == nil {
					slog.Warn("idle pool media check unavailable")
				}
			}
		} else if ctx.Err() == nil {
			slog.Warn("idle pool checks unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
