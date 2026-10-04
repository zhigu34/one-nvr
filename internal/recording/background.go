package recording

import (
	"context"
	"log/slog"
	"time"
)

func (s *Service) Replay(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := s.DrainSpool(bounded)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("recording callback replay unavailable")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
