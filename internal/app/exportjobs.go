package app

import (
	"context"
	"time"

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
