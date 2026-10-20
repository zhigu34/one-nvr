package recording

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// RetentionSweepInterval is how often the worker looks for expired recordings.
// Retention is measured in days, so a few minutes of latency is irrelevant while
// a slow sweep against a large index would be wasteful.
const RetentionSweepInterval = 5 * time.Minute

// retentionBatch bounds one sweep. Deleting is I/O-bound and unbounded work
// would hold a worker for an arbitrarily long time on first enable, when every
// old recording is suddenly expired; successive sweeps drain the backlog.
const retentionBatch = 200

// RunRetention deletes recordings older than the site's retention period.
//
// It is disabled by default (0 days) and therefore does nothing at all on an
// upgraded site until an administrator sets a period: automatic deletion of
// existing footage is not a decision an upgrade may make for the operator.
//
// Retention never touches protected evidence, never touches a segment inside
// FreshnessDelay, and never cascades to any other copy. Each deletion is
// recorded per file so the audit trail stays per-segment rather than a bulk
// claim, and a failure is retried on the next sweep instead of being lost.
func (s *Service) RunRetention(ctx context.Context) {
	ticker := time.NewTicker(RetentionSweepInterval)
	defer ticker.Stop()
	for {
		// Export artifacts expire on their own seven-day clock regardless of
		// the site's recording retention setting, so this sweep always runs.
		s.SweepExportArtifacts(ctx)
		s.SweepRetention(ctx)
		// Observation rows are not history: they carry a thirty-second TTL and
		// only the newest one per channel and stream kind is ever read. Without
		// this they accumulate at three rows per channel every ten seconds —
		// millions a month — because nothing else deletes them.
		s.SweepObservations(ctx)
		// Event clip jobs are bookkeeping: once a job is finished the event's
		// verdict is already on the event row, and a failed one is an answer
		// ("no buffered footage") rather than work to retry. Keeping them forever
		// would grow a table nobody reads, one row per detection.
		s.SweepEventClipJobs(ctx)
		// The completion inbox is bookkeeping: a row records that a media-server
		// callback was processed. Its payload is large (the full completion JSON)
		// and nothing reads an old one, so without this it grows without bound —
		// measured on the live site at 144,000 diagnostic rows and 60 MB from a
		// single class of callback.
		s.SweepHookInbox(ctx)
		// Event clips quarantined by the re-cut defect carry evidence about a file
		// that no longer exists; only the file itself can say what its inode is, so
		// this cannot be a SQL migration. It runs on the same ticker and stops
		// finding candidates once the rows are repaired.
		s.RepairEventClipDamage(ctx)
		// The same defect left duplicate files: one per assembly, only one of them
		// referenced. They are the same event at earlier extents, so nothing can
		// ever play them; leaving them would keep every long event's footage on
		// disk twice over.
		s.SweepEventClipOrphans(ctx)
		// The safety net for clips whose event ended while their job was already
		// settled: nothing re-queues those, so without this they stay finalizing.
		s.PublishSettledClips(ctx)
		// Archive-bound channels clean on their own shorter window, gated on a
		// confirmed remote copy (ARC-05); the gate hook is injected by the
		// assembly, so without it nothing happens.
		s.SweepArchiveLocal(ctx)
		// Pressure cleanup is what keeps a long-running site from filling its
		// disk: everything above only removes what is *old*, and a 24/7 site
		// with a generous retention window reaches full before anything is old
		// enough. It runs after the age-based sweeps so space freed by expiry is
		// already visible, and it runs through the same delete core, so
		// protection, export pins, uploads, playback leases and the archive
		// safe-copy gate all still apply.
		s.SweepWatermarks(ctx)
		// Quota runs last: filling the volume is an emergency, one channel's
		// share is bookkeeping, and the more urgent policy should free space
		// first.
		s.SweepQuotas(ctx)
		s.SweepWatermarkEvents(ctx)
		// The audit trail has its own 180-day window (SYS-01) and nothing else
		// ever deletes from it. It runs on this ticker because the cadence is
		// already right: five minutes is far below the resolution of a
		// six-month window, and a second timer would be another thing to keep
		// alive.
		if s.Audits != nil {
			s.Audits.SweepAudits(ctx)
		}
		// A manual window that expired without being closed would read as still
		// running in the audit trail, and the scheduler would keep ignoring it
		// for the same reason it ignores any expired window — so this is
		// bookkeeping, and it belongs on the same ticker as the rest.
		s.SweepManualWindows(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SweepExportArtifacts removes the files of artifacts past their download
// validity. A protected artifact outlives expiry (EXP-05); an expired artifact
// keeps its row with state 'expired' so the drawer can say what happened, and
// a download attempt finds it gone, exactly like a deleted recording.
func (s *Service) SweepExportArtifacts(ctx context.Context) {
	if s.Pools == nil || s.DB == nil {
		return
	}
	for {
		var batch []struct {
			ID           id.ID
			PoolID       id.ID
			RelativePath string
		}
		rows, err := s.DB.Pool.Query(ctx, `SELECT a.id,a.pool_id,a.relative_path FROM recording_export_artifacts a JOIN storage_pools p ON p.id=a.pool_id WHERE a.state='ready' AND NOT a.protected AND a.expires_at<clock_timestamp() AND p.enabled LIMIT 100`)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("export artifact expiry scan unavailable")
			}
			return
		}
		for rows.Next() {
			var item struct {
				ID           id.ID
				PoolID       id.ID
				RelativePath string
			}
			if err := rows.Scan(&item.ID, &item.PoolID, &item.RelativePath); err != nil {
				rows.Close()
				slog.Warn("export artifact expiry scan unavailable")
				return
			}
			batch = append(batch, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(batch) == 0 {
			if err != nil && ctx.Err() == nil {
				slog.Warn("export artifact expiry scan unavailable")
			}
			return
		}
		for _, item := range batch {
			root, _, err := s.Pools.OpenMediaRoot(ctx, item.PoolID)
			if err != nil {
				return
			}
			err = root.Remove(item.RelativePath)
			root.Close()
			if err != nil && !os.IsNotExist(err) {
				return
			}
			if _, err := s.DB.Pool.Exec(ctx, `UPDATE recording_export_artifacts SET state='expired' WHERE id=$1 AND state='ready'`, item.ID); err != nil {
				return
			}
		}
		if len(batch) < 100 {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// SweepRetention performs one retention pass. It is exported so a test can run
// the sweep deterministically instead of waiting for the interval, and so an
// operator-facing trigger can reuse it later.
func (s *Service) SweepRetention(ctx context.Context) {
	// No Auth check: this sweep acts on the site's behalf, not a user's. deleteLocal
	// takes an empty actor precisely for that case. Requiring Auth here meant the
	// whole pass returned on its first line in the worker, where the service is
	// built without it — so a site configured to keep 30 days kept everything
	// forever, silently, with the setting visible and audited on the page.
	if s.Pools == nil || s.DB == nil {
		return
	}
	days, err := s.retentionDays(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("retention window unavailable")
		}
		return
	}
	if days <= 0 {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days).Add(-FreshnessDelay)
	for {
		expired, err := s.expiredSegments(ctx, cutoff, retentionBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("retention candidate scan unavailable")
			}
			return
		}
		if len(expired) == 0 {
			return
		}
		outcomes := s.deleteLocal(ctx, "", expired, "retention")
		if ctx.Err() != nil {
			return
		}
		// A sweep that deleted nothing means every candidate was kept for a
		// permanent reason (protected, or a file that vanished on its own), so
		// ending here avoids rescanning the same rows forever.
		progressed := false
		for _, outcome := range outcomes {
			if outcome.State == deleteStateDeleted {
				progressed = true
				break
			}
		}
		if !progressed {
			return
		}
		if len(expired) < retentionBatch {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (s *Service) retentionDays(ctx context.Context) (int, error) {
	var days int
	err := s.DB.Pool.QueryRow(ctx, "SELECT recording_retention_days FROM sites WHERE id=$1", s.siteID).Scan(&days)
	return days, err
}

// expiredSegments lists deletable candidates: published, unprotected, older than
// the cutoff, and on an enabled pool. Archive-bound channels are excluded —
// they clean on their own shorter window under the safe-copy gate (ARC-05).
// The age cutoff carries the freshness delay as well, so a segment that just
// finished is never a candidate.
func (s *Service) expiredSegments(ctx context.Context, cutoff time.Time, limit int) ([]id.ID, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT s.id FROM recording_segments s JOIN storage_pools p ON p.id=s.pool_id WHERE s.state='ready' AND NOT s.protected AND s.end_at<$1 AND p.enabled
		AND NOT EXISTS(SELECT 1 FROM archive_policies ap WHERE ap.channel_id=s.channel_id) ORDER BY s.end_at LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]id.ID, 0, limit)
	for rows.Next() {
		var segment id.ID
		if err := rows.Scan(&segment); err != nil {
			return nil, err
		}
		out = append(out, segment)
	}
	return out, rows.Err()
}

// ArchiveLocalGate is injected by the assembly: given one candidate segment it
// confirms the verified cloud copy is really reachable right now (ARC-05
// requires current availability, not a stale success row). false keeps the
// local file; an error aborts the sweep round.
type ArchiveLocalGate func(ctx context.Context, segmentID id.ID) (bool, error)

// SweepArchiveLocal cleans the local sources of archive-bound channels: past
// the archive window (default 48h, 0 = never), with a verified unexpired cloud
// copy that the injected gate confirms reachable, and — via the shared delete
// core — free of protection, task pins and playback leases. The plain days-
// based retention keeps governing unbound channels.
func (s *Service) SweepArchiveLocal(ctx context.Context) {
	// Same reasoning as SweepRetention: an archive-driven cleanup is a site policy,
	// not an operator action.
	if s.Pools == nil || s.DB == nil || s.ArchiveLocalGate == nil {
		return
	}
	var hours int
	if err := s.DB.Pool.QueryRow(ctx, `SELECT archive_local_retention_hours FROM sites WHERE id=$1`, s.siteID).Scan(&hours); err != nil {
		if ctx.Err() == nil {
			slog.Warn("archive local window unavailable")
		}
		return
	}
	if hours <= 0 {
		return
	}
	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Add(-FreshnessDelay)
	for {
		rows, err := s.DB.Pool.Query(ctx, `SELECT s.id FROM recording_segments s
			JOIN archive_policies ap ON ap.channel_id=s.channel_id
			JOIN archive_targets t ON t.id=ap.target_id AND t.enabled
			WHERE s.state='ready' AND NOT s.protected AND s.end_at<$1
			AND EXISTS(SELECT 1 FROM recording_archive_copies ac WHERE ac.segment_id=s.id AND ac.state='verified'
				AND (ac.cloud_expires_at IS NULL OR ac.cloud_expires_at>clock_timestamp()))
			AND NOT EXISTS(SELECT 1 FROM recording_playback_leases l WHERE l.segment_id=s.id AND l.lease_expires_at>clock_timestamp())
			AND NOT EXISTS(SELECT 1 FROM recording_archive_copies c2 WHERE c2.segment_id=s.id AND c2.state='uploading')
			ORDER BY s.end_at LIMIT 50`, cutoff)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("archive local scan unavailable")
			}
			return
		}
		var batch []id.ID
		for rows.Next() {
			var segment id.ID
			if err := rows.Scan(&segment); err != nil {
				rows.Close()
				return
			}
			batch = append(batch, segment)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(batch) == 0 {
			return
		}
		// A round that deletes nothing must not loop. deleteOne reports a skip
		// WITHOUT changing the row (a missing file, an identity mismatch), so the
		// same segments are selected again on the next iteration — forever. That
		// hang sits before the pressure, quota and audit sweeps on the same
		// ticker, so a single undeletable segment would silently stop the cleanup
		// that keeps the disk from filling.
		deleted := 0
		for _, segment := range batch {
			available, err := s.ArchiveLocalGate(ctx, segment)
			if err != nil {
				// Cloud unconfirmable: stop archive-channel cleanup this round
				// (ARC-05 — never delete when the safe copy cannot be proven).
				return
			}
			if !available {
				continue
			}
			for _, outcome := range s.deleteLocal(ctx, "", []id.ID{segment}, "retention") {
				if outcome.State == deleteStateDeleted {
					deleted++
				}
			}
			if ctx.Err() != nil {
				return
			}
		}
		if deleted == 0 || len(batch) < 50 {
			return
		}
	}
}

// ObservationRetention is how long a raw sample outlives its own TTL.
//
// These rows are CURRENT STATE, not history. The row expires thirty seconds after
// it is written, the status page takes only the newest row per channel and stream,
// and the reliability layer derives its intervals as each sample arrives — no
// reader ever goes back for an older row. PRD REL-06's seven days applies to the
// INTERVAL tier (kept 365 days), not to the samples that produced it.
//
// Measured on the live site: 164,084 rows and about 170 MB per day at sixteen
// channels. A seven-day window is 1.2 GB of rows nothing reads.
const ObservationRetention = 24 * time.Hour

// SweepObservations deletes observation rows past their usefulness.
//
// These rows describe *now*: the reader takes the newest row per channel and
// stream kind and marks it stale once expires_at passes. Nothing queries an old
// observation, so an expired row is dead weight — and at three rows per channel
// every ten seconds it is heavy dead weight.
//
// Deleting them cannot change what the UI reports: a kind with no rows at all
// already falls back to an explicit unknown state rather than to healthy, so a
// missing row means "not observed", never "fine".
func (s *Service) SweepObservations(ctx context.Context) {
	if s.DB == nil {
		return
	}
	// Bounded batches, like the recording sweeps: the first run after upgrade
	// may have months of backlog, and one enormous DELETE would hold locks and
	// write-ahead log for an arbitrarily long time.
	for {
		tag, err := s.DB.Pool.Exec(ctx, `DELETE FROM source_observations
			WHERE id IN (SELECT id FROM source_observations
				WHERE observed_at < clock_timestamp() - $1::interval
				LIMIT $2)`, ObservationRetention.String(), retentionBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("source observation cleanup unavailable", "error", err)
			}
			return
		}
		if tag.RowsAffected() < retentionBatch || ctx.Err() != nil {
			return
		}
	}
}

// HookInboxRetention keeps settled completions long enough to explain a recording
// that did not appear, and no longer.
//
// Seven days matches the clip-job window: both answer "why is this footage
// missing", and both stop being useful once the footage itself is gone.
const HookInboxRetention = 7 * 24 * time.Hour

// SweepHookInbox removes settled inbox rows past the retention window.
//
// Only settled states are eligible. 'pending' and 'processing' are live work —
// deleting one would strand the completion it represents — so they are kept
// regardless of age, exactly like the clip-job sweep.
func (s *Service) SweepHookInbox(ctx context.Context) {
	if s.DB == nil {
		return
	}
	total := int64(0)
	for {
		tag, err := s.DB.Pool.Exec(ctx, `DELETE FROM hook_inbox
			WHERE id IN (SELECT id FROM hook_inbox
				WHERE state IN ('processed','diagnostic')
				  AND received_at < clock_timestamp() - make_interval(secs => $1)
				LIMIT $2)`, HookInboxRetention.Seconds(), retentionBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("hook inbox retention unavailable", "error", err)
			}
			return
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < retentionBatch || ctx.Err() != nil {
			break
		}
	}
	if total > 0 {
		slog.Info("hook inbox rows swept", "removed", total)
	}
}
