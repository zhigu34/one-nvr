package recording

import (
	"context"
	"errors"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"log/slog"
	"sync"
	"time"
)

func (s *Service) Monitor(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var mu sync.Mutex
	active := map[id.ID]bool{}
	slots := make(chan struct{}, 4)
	var running sync.WaitGroup
	defer running.Wait()
	offset := 0
	for ctx.Err() == nil {
		// Empty permanent slots have no media to observe. Keep cleanup candidates
		// even when their channel is disabled/cleared, until its sessions close.
		rows, err := s.DB.Pool.Query(ctx, `SELECT c.id FROM channels c WHERE (c.enabled AND c.current_revision_id IS NOT NULL) OR EXISTS(SELECT 1 FROM stream_sessions ss WHERE ss.channel_id=c.id AND ss.purpose IN ('main','sub') AND ss.state NOT IN ('closed','failed')) ORDER BY c.channel_no`)
		if err != nil {
			return err
		}
		var channels []id.ID
		for rows.Next() {
			var ch id.ID
			if err := rows.Scan(&ch); err != nil {
				rows.Close()
				return err
			}
			channels = append(channels, ch)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range channels {
			ch := channels[(i+offset)%len(channels)]
			mu.Lock()
			busy := active[ch]
			if !busy {
				select {
				case slots <- struct{}{}:
					active[ch] = true
				default:
					busy = true
				}
			}
			mu.Unlock()
			if busy {
				continue
			}
			running.Add(1)
			go func() {
				defer running.Done()
				defer func() { mu.Lock(); delete(active, ch); mu.Unlock(); <-slots }()
				sample, cancel := context.WithTimeout(ctx, 25*time.Second)
				defer cancel()
				if err := s.Reconcile(sample, ch); err != nil && !errors.Is(err, channel.ErrExecutionBusy) && ctx.Err() == nil {
					slog.Warn("recording reconciliation unavailable")
				}
			}()
		}
		if len(channels) > 0 {
			offset = (offset + 4) % len(channels)
		}
		// These are transient rate samples, not camera reliability history.
		if _, err := s.DB.Pool.Exec(ctx, "DELETE FROM recording_bitrate_samples WHERE observed_at<clock_timestamp()-interval '10 minutes'"); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return ctx.Err()
}
