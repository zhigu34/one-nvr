package app

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/channel"
	"log/slog"
	"time"
)

func cleanupImportDrafts(ctx context.Context, s *channel.SourceService) {
	timer := time.NewTicker(2 * time.Minute)
	defer timer.Stop()
	for ctx.Err() == nil {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := s.CleanupExpiredImports(bounded); err != nil && ctx.Err() == nil {
			slog.Warn("expired import draft cleanup unavailable")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
