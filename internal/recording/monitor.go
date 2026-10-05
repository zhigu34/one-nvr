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
	work, cancel := context.WithCancel(ctx)
	var running sync.WaitGroup
	defer func() { cancel(); running.Wait() }()
	var mu sync.Mutex
	active := map[id.ID]bool{}
	// All permanent slots fit in a bounded FIFO. Busy workers must not cause
	// healthy configured channels to be skipped for an entire polling interval.
	queue := make(chan id.ID, 32)
	for i := 0; i < 4; i++ {
		running.Add(1)
		go func() {
			defer running.Done()
			for {
				select {
				case <-work.Done():
					return
				case ch := <-queue:
					sample, stop := context.WithTimeout(work, 25*time.Second)
					err := s.Reconcile(sample, ch)
					stop()
					mu.Lock()
					delete(active, ch)
					mu.Unlock()
					if err != nil && !errors.Is(err, channel.ErrExecutionBusy) && work.Err() == nil {
						slog.Warn("recording reconciliation unavailable")
					}
				}
			}
		}()
	}
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
		for _, ch := range channels {
			mu.Lock()
			if !active[ch] {
				active[ch] = true
				select {
				case queue <- ch:
				default:
					delete(active, ch)
				}
			}
			mu.Unlock()
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
