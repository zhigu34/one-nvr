package app

import (
	"context"
	"time"

	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/recording"
)

// runExportReconciliation terminalizes export rows whose job exhausted its
// attempts: the jobs table marks itself failed on claim, but only the domain
// knows the task it belongs to must read as failed too.
func runExportReconciliation(ctx context.Context, s *recording.Service) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.ReconcileExports(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runArchiveScheduleSweeper drives the scheduled-upload triggers. The ledger's
// UNIQUE constraint fences repeats, so a short tick is safe.
func runArchiveScheduleSweeper(ctx context.Context, s *archive.Service) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.SweepSchedules(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
