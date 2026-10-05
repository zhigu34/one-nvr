package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/recording"
)

func runSourceTestJobs(ctx context.Context, s *recording.Service) {
	runSourceJobs(ctx, s, "source.test", s.ExecuteSourceTest)
}
func runSourceChangeJobs(ctx context.Context, s *recording.Service, kind string) {
	runSourceJobs(ctx, s, kind, s.ExecuteSourceChange)
}
func runSourceJobs(ctx context.Context, s *recording.Service, kind string, handler jobs.Handler) {
	for ctx.Err() == nil {
		err := jobs.Run(ctx, jobs.Repository{DB: s.DB}, kind, handler)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !errors.Is(err, jobs.ErrLeaseLost) {
			slog.Warn("source operation queue unavailable", "kind", kind)
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
