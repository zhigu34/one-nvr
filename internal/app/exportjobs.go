package app

import (
	"context"
	"time"

	"github.com/zhigu34/one-nvr/internal/archive"
	"github.com/zhigu34/one-nvr/internal/detection"
	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/notify"
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

// runCloudExpirySweeper drives ARC-09: remote objects past their cloud deadline
// are deleted within the hour, protection and leases excepted.
func runCloudExpirySweeper(ctx context.Context, s *archive.Service) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		s.SweepCloudExpiry(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runDetectionSessionSync republishes detector configuration when a channel's
// media session moved on. It runs on a short interval because a reconnecting
// channel should resume detection within seconds, not minutes; the sync itself
// is a no-op whenever the published config still matches.
func runDetectionSessionSync(ctx context.Context, s *detection.Service) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		s.SyncAfterSessionChange(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runSnapshotArchive copies detector snapshots into the channel's bound pool
// (STO-07). It is independent of the recording path: a picture that cannot be
// copied is reported on the event and retried next tick, and the detector's own
// copy keeps answering in the meantime.
func runSnapshotArchive(ctx context.Context, s *events.Service) {
	ticker := time.NewTicker(events.SnapshotSweepInterval)
	defer ticker.Stop()
	for {
		s.ArchiveSnapshots(ctx)
		s.SweepSnapshots(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runDeliveryReconciliation settles delivery rows whose job ended without the
// worker recording it: the jobs table knows a job failed, but only this kind
// knows that means a notification was never delivered.
func runDeliveryReconciliation(ctx context.Context, s *notify.Service) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.ReconcileDeliveries(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
