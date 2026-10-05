package app

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/recording"
	"log/slog"
	"time"
)

func runRecordingMonitor(ctx context.Context, s *recording.Service) {
	for ctx.Err() == nil {
		if err := s.Monitor(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("recording monitor unavailable")
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
