package recording

import (
	"context"
	"log/slog"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// Watermark state names. The order is the ordering of severity, and the sweeper
// only ever escalates: a pool reported as "warning" this round is not silently
// downgraded to "ok" because one sample happened to land under the line.
//
// "unknown" is deliberately separate from "ok". A pool whose capacity has never
// been observed has not been measured as healthy; reporting 0% used would be the
// same class of lie as reporting an unobserved stream as available.
const (
	WatermarkOK       = "ok"
	WatermarkWarning  = "warning"
	WatermarkPressure = "pressure"
	WatermarkCritical = "critical"
	WatermarkUnknown  = "unknown"
)

// watermarkPolicy is the effective policy for one pool. Percentages come from
// the pool row; the byte safety line comes from the capacity gate, because it
// is derived from measured bitrate rather than configured.
type watermarkPolicy struct {
	PoolID          id.ID
	FilesystemID    string
	WarningPercent  int
	PressurePercent int
	TargetPercent   int
	CriticalPercent int
	UsedBytes       int64
	TotalBytes      int64
	FreeBytes       int64
	// SafeLine is max(10GB, ten minutes of measured bitrate for the channels
	// sharing this filesystem). It is the floor below which no cleanup policy
	// may take free space, because a cleanup that lands exactly on the line
	// leaves no room for the next segment to be written.
	SafeLine int64
	Known    bool
}

// percentUsed reports the rounded-up used percentage, for display.
//
// Rounding up is deliberate here: the number an operator compares against the
// threshold must not be the friendlier of the two readings, so 84.2% shows as
// 85%. It must NOT drive the threshold check — see crosses().
func percentUsed(used, total int64) int {
	if total <= 0 || used <= 0 {
		return 0
	}
	p := (used*100 + total - 1) / total
	if p > 100 {
		return 100
	}
	return int(p)
}

// crosses reports whether used/total reaches the given percentage, without
// routing the comparison through the display rounding.
//
// The distinction is not academic: at 799/1000 the display says 80, but the
// pool has not reached 80% and must not be classified as warning. Comparing the
// rounded display value would fire every threshold up to one point early.
func crosses(used, total, percent int64) bool {
	if total <= 0 {
		return false
	}
	if percent <= 0 {
		return true
	}
	return used*100 >= percent*total
}

// state classifies a measurement. Critical takes precedence over pressure,
// pressure over warning: a pool at 96% is critical even though it also crosses
// the warning line.
func (p watermarkPolicy) state() string {
	if !p.Known || p.TotalBytes <= 0 {
		return WatermarkUnknown
	}
	switch {
	case crosses(p.UsedBytes, p.TotalBytes, int64(p.CriticalPercent)):
		return WatermarkCritical
	case crosses(p.UsedBytes, p.TotalBytes, int64(p.PressurePercent)):
		return WatermarkPressure
	case crosses(p.UsedBytes, p.TotalBytes, int64(p.WarningPercent)):
		return WatermarkWarning
	default:
		return WatermarkOK
	}
}

// cleanupNeeded reports whether this state asks the sweeper to remove footage.
//
// The recovery hysteresis that keeps a blocked channel from flapping belongs to
// the recorder-start gate (capacity_gate.go), not here: the sweeper is allowed
// to stop the instant it reaches the target, because deletion is cheap to
// resume and holding a channel blocked for two extra samples would be a
// different policy than "clean until back under the target".
func (p watermarkPolicy) cleanupNeeded() bool {
	switch p.state() {
	case WatermarkPressure, WatermarkCritical:
		return true
	}
	return false
}

// targetFreeBytes is the free space the sweeper aims to restore.
//
// It is expressed as free space rather than used percentage so that the safety
// line can act as a hard floor: the target is the larger of "back under the
// configured target percentage" and the measured safety line. Without the
// floor, a target percentage of 99 on a full disk would compute a target of one
// percent free — less than a single segment needs.
func (p watermarkPolicy) targetFreeBytes() int64 {
	var byPercent int64
	if p.TotalBytes > 0 && p.TargetPercent < 100 {
		byPercent = p.TotalBytes * int64(100-p.TargetPercent) / 100
	}
	if p.SafeLine > byPercent {
		return p.SafeLine
	}
	return byPercent
}

// SweepWatermarks runs one pressure-cleanup pass over every enabled pool.
//
// It is the counterpart to SweepRetention: retention deletes what is old,
// pressure cleanup deletes what is oldest first in order to keep the filesystem
// from filling up. Both go through the same delete core, so protection, export
// pins, in-flight archive uploads, playback leases and the archive safe-copy
// gate all apply without this code restating them — that is the whole point of
// not writing a second deletion path.
func (s *Service) SweepWatermarks(ctx context.Context) {
	if s.Pools == nil || s.DB == nil {
		return
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT p.id FROM storage_pools p WHERE p.enabled ORDER BY p.id`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("watermark pool scan unavailable", "error", err)
		}
		return
	}
	var pools []id.ID
	for rows.Next() {
		var pool id.ID
		if err := rows.Scan(&pool); err != nil {
			rows.Close()
			return
		}
		pools = append(pools, pool)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	for _, pool := range pools {
		if ctx.Err() != nil {
			return
		}
		s.sweepPoolWatermark(ctx, pool)
	}
}

// sweepPoolWatermark handles one pool.
//
// Pools on the same filesystem are cleaned one at a time and each round
// re-measures: after the first pool frees space, the second pool's own
// measurement is read again rather than acting on the stale number from before
// the first pool's deletions. That is the coordination STO-03 asks for, and it
// needs no cross-pool bookkeeping.
func (s *Service) sweepPoolWatermark(ctx context.Context, pool id.ID) {
	policy, err := s.watermarkFor(ctx, pool)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("watermark measurement unavailable", "pool", string(pool), "error", err)
		}
		return
	}
	if !policy.Known {
		// Not measured is not the same as healthy: record the unknown state so
		// the UI can say "capacity unknown" instead of showing a green pool.
		s.recordWatermark(ctx, policy, policy.state(), 0, 0, "capacity_unknown")
		return
	}
	if !policy.cleanupNeeded() {
		if policy.state() == WatermarkWarning {
			// Warning is advisory: record it, delete nothing. Deleting at the
			// warning line would make the lower two thresholds indistinguishable
			// from the operator's point of view.
			s.recordWatermark(ctx, policy, policy.state(), 0, 0, "")
		}
		return
	}
	outcome, requested, deleted := s.drainPool(ctx, policy)
	s.recordWatermark(ctx, policy, policy.state(), requested, deleted, outcome)
}

// drainPool removes the oldest safe candidates until the target is met, one
// batch at a time.
//
// Free space is re-measured after each batch instead of tracking what the
// deletions claim to release. The two are not the same number: the indexed size
// is what the file held when it was published, while the filesystem may report
// block-rounded, sparse or already-deleted space. Progressing on the estimate
// can stop the sweep believing it restored space that the volume never got —
// which is the one thing this feature must not do, because the next thing that
// happens is the recorder being blocked.
func (s *Service) drainPool(ctx context.Context, policy watermarkPolicy) (outcome string, requested, deleted int) {
	target := policy.targetFreeBytes()
	for {
		if ctx.Err() != nil {
			return "cancelled", requested, deleted
		}
		policy, err := s.watermarkFor(ctx, policy.PoolID)
		if err != nil || !policy.Known {
			return "capacity_unknown", requested, deleted
		}
		if policy.FreeBytes >= target {
			return "target_reached", requested, deleted
		}
		candidates, err := s.oldestSafeSegments(ctx, policy.PoolID, watermarkBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("watermark candidate scan unavailable", "pool", string(policy.PoolID), "error", err)
			}
			return "capacity_unknown", requested, deleted
		}
		if len(candidates) == 0 {
			// Nothing left that the safety rules allow removing. The channel
			// gate decides whether that means "stop recording"; this sweeper
			// must never break a protection or an archive obligation to make
			// room (STO-02's explicit prohibition).
			return "no_safe_candidate", requested, deleted
		}
		requested += len(candidates)
		roundDeleted := 0
		for _, candidate := range candidates {
			outcome, err := s.deleteOne(ctx, "", candidate.ID, deleteReasonWatermark)
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("watermark delete failed", "segment", string(candidate.ID), "error", err)
				}
				return "delete_failed", requested, deleted + roundDeleted
			}
			if outcome.State == deleteStateDeleted {
				roundDeleted++
			}
		}
		deleted += roundDeleted
		if roundDeleted == 0 {
			// The whole batch was skipped for permanent reasons; scanning the
			// same rows again would loop forever.
			return "no_safe_candidate", requested, deleted
		}
		// A short batch means there is no backlog left to retry; the loop's next
		// measurement decides whether the target was actually reached.
		if len(candidates) < watermarkBatch {
			policy, err := s.watermarkFor(ctx, policy.PoolID)
			if err != nil || !policy.Known {
				return "capacity_unknown", requested, deleted
			}
			if policy.FreeBytes >= target {
				return "target_reached", requested, deleted
			}
			return "batch_exhausted", requested, deleted
		}
	}
}

// watermarkBatch bounds one deletion round. Pressure cleanup runs while a disk
// is filling, so it should make visible progress per round without holding the
// worker for minutes on a nearly full pool.
const watermarkBatch = 50

// oldestSafeSegments lists the oldest deletable segments on a pool.
//
// "Safe" is exactly the set STO-02 permits under pressure: published, not
// protected, not pinned by an export, an in-flight archive upload or a playback
// lease, and — for archive-bound channels — past the local archive window with
// a verified, unexpired remote copy. That last condition is what makes the
// pressure path safe to reuse: a rebuild that skips it would let a full disk
// destroy footage that was never uploaded.
func (s *Service) oldestSafeSegments(ctx context.Context, pool id.ID, limit int) ([]watermarkCandidate, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT s.id FROM recording_segments s
		WHERE s.pool_id=$1 AND s.state='ready' AND NOT s.protected
		AND s.end_at < clock_timestamp() - $2::interval
		AND NOT EXISTS(SELECT 1 FROM recording_export_sources es JOIN recording_exports e ON e.id=es.export_id WHERE es.segment_id=s.id AND e.state IN ('queued','running'))
		AND NOT EXISTS(SELECT 1 FROM recording_archive_copies c WHERE c.segment_id=s.id AND c.state='uploading')
		AND NOT EXISTS(SELECT 1 FROM recording_playback_leases l WHERE l.segment_id=s.id AND l.lease_expires_at>clock_timestamp())
		AND (
			NOT EXISTS(SELECT 1 FROM archive_policies ap WHERE ap.channel_id=s.channel_id)
			OR EXISTS(SELECT 1 FROM archive_policies ap JOIN recording_archive_copies c ON c.segment_id=s.id
				WHERE ap.channel_id=s.channel_id AND c.state='verified'
				AND (c.cloud_expires_at IS NULL OR c.cloud_expires_at>clock_timestamp())
				AND s.end_at < clock_timestamp() - make_interval(hours => (SELECT archive_local_retention_hours FROM sites WHERE id=(SELECT site_id FROM storage_pools WHERE id=$1))))
		)
		ORDER BY s.end_at LIMIT $3`, pool, FreshnessDelay.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]watermarkCandidate, 0, limit)
	for rows.Next() {
		var candidate watermarkCandidate
		if err := rows.Scan(&candidate.ID); err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

type watermarkCandidate struct {
	ID id.ID
}

// watermarkFor reads the pool's policy, measurement and the derived safety line.
func (s *Service) watermarkFor(ctx context.Context, pool id.ID) (watermarkPolicy, error) {
	var out watermarkPolicy
	out.PoolID = pool
	if err := s.DB.Pool.QueryRow(ctx, `SELECT warning_percent,pressure_percent,pressure_target_percent,critical_percent FROM storage_pools WHERE id=$1`, pool).Scan(&out.WarningPercent, &out.PressurePercent, &out.TargetPercent, &out.CriticalPercent); err != nil {
		return out, err
	}
	// Capacity is taken from the api and worker probes together: both must be
	// fresh and agree on the filesystem, exactly like the recorder-start gate,
	// because a pool visible to one process and not the other is not a usable
	// pool.
	var checks, identities int
	if err := s.DB.Pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT nullif(filesystem_id,'')),coalesce(min(nullif(filesystem_id,'')),''),coalesce(min(total_bytes),0),coalesce(min(free_bytes),0) FROM storage_pool_checks WHERE pool_id=$1 AND service IN ('api','worker') AND state='healthy' AND expires_at>clock_timestamp()`, pool).Scan(&checks, &identities, &out.FilesystemID, &out.TotalBytes, &out.FreeBytes); err != nil {
		return out, err
	}
	if checks != 2 || identities != 1 || out.FilesystemID == "" || out.TotalBytes <= 0 {
		// Leave Known false; the caller records the unknown state.
		return out, nil
	}
	if out.FreeBytes < 0 || out.FreeBytes > out.TotalBytes {
		return out, nil
	}
	out.UsedBytes = out.TotalBytes - out.FreeBytes
	line, err := s.safetyLineForPool(ctx, pool)
	if err != nil {
		return out, err
	}
	out.SafeLine = line
	out.Known = true
	return out, nil
}

// safetyLineForPool reuses the recorder-start gate's measurement so that the
// two decisions cannot disagree about how much free space recording needs.
func (s *Service) safetyLineForPool(ctx context.Context, pool id.ID) (int64, error) {
	var ch, revision id.ID
	if err := s.DB.Pool.QueryRow(ctx, `SELECT c.id,c.current_revision_id FROM channels c WHERE c.enabled AND c.storage_pool_id=$1 AND c.current_revision_id IS NOT NULL ORDER BY c.channel_no LIMIT 1`, pool).Scan(&ch, &revision); err != nil {
		// A pool with no bound channel has no measured bitrate; the floor is
		// the minimum free space alone.
		return storage.MinimumFreeBytes, nil
	}
	capacity, err := s.capacityFor(ctx, ch, revision, pool)
	if err != nil || capacity.Line <= 0 {
		// The gate reports "unknown" when its evidence is stale; pressure
		// cleanup still has the measured bitrate of the pool's own channels to
		// work from, so fall back to the hard floor rather than skipping the
		// round. Deleting nothing because a sampler was briefly stale would let
		// a disk fill.
		return storage.MinimumFreeBytes, nil
	}
	return capacity.Line, nil
}

// deleteReasonWatermark is the audit reason recorded for pressure deletions.
// It is distinct from "retention" so an operator can tell "this was old" from
// "the disk was filling up".
const deleteReasonWatermark = "watermark"

// recordWatermark appends one decision row. Failure to record does not undo the
// deletions that already happened; it only makes this round's reasoning absent
// from the operational log.
func (s *Service) recordWatermark(ctx context.Context, policy watermarkPolicy, state string, requested, deleted int, outcome string) {
	if s.DB == nil {
		return
	}
	observation, err := id.New()
	if err != nil {
		return
	}
	if _, err := s.DB.Pool.Exec(ctx, `INSERT INTO storage_watermark_events(id,pool_id,filesystem_id,state,used_bytes,total_bytes,percent,requested,deleted,outcome) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		observation, policy.PoolID, policy.FilesystemID, state, policy.UsedBytes, policy.TotalBytes, percentUsed(policy.UsedBytes, policy.TotalBytes), requested, deleted, outcome); err != nil && ctx.Err() == nil {
		slog.Warn("watermark event not recorded", "pool", string(policy.PoolID), "error", err)
	}
}

// watermarkRetention bounds the decision log. This is operational evidence with
// a short useful life — an operator asks what the sweeper did today, not last
// quarter. Thirty days keeps a month of history at one row per pool per round.
const watermarkRetention = 30 * 24 * time.Hour

// SweepWatermarkEvents drops decision rows past their usefulness.
func (s *Service) SweepWatermarkEvents(ctx context.Context) {
	if s.DB == nil {
		return
	}
	for {
		tag, err := s.DB.Pool.Exec(ctx, `DELETE FROM storage_watermark_events WHERE id IN (
			SELECT id FROM storage_watermark_events WHERE observed_at < clock_timestamp() - $1::interval LIMIT $2)`,
			watermarkRetention.String(), retentionBatch)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("watermark event cleanup unavailable", "error", err)
			}
			return
		}
		if tag.RowsAffected() < retentionBatch || ctx.Err() != nil {
			return
		}
	}
}

// SweepQuotas enforces the optional per-channel byte quota (REC-07).
//
// Quota is a logical limit: it counts the bytes one-nvr has indexed for the
// channel and removes the oldest safe segments past it. It is deliberately not
// a filesystem quota and never affects other channels or other applications on
// the same volume (STO-03).
//
// Ordering matters with the watermark sweep: quota runs after pressure cleanup
// because pressure is about the whole volume failing, while quota is about one
// channel's share. Running the more urgent one first means a nearly full disk
// gets its space back before per-channel bookkeeping starts deleting.
func (s *Service) SweepQuotas(ctx context.Context) {
	if s.Pools == nil || s.DB == nil {
		return
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,c.recording_quota_bytes FROM channels c WHERE c.recording_quota_bytes>0 AND c.enabled ORDER BY c.channel_no`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("quota channel scan unavailable", "error", err)
		}
		return
	}
	type quotaTarget struct {
		Channel id.ID
		Quota   int64
	}
	var targets []quotaTarget
	for rows.Next() {
		var target quotaTarget
		if err := rows.Scan(&target.Channel, &target.Quota); err != nil {
			rows.Close()
			return
		}
		targets = append(targets, target)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		s.sweepChannelQuota(ctx, target.Channel, target.Quota)
	}
}

// sweepChannelQuota removes the channel's oldest safe segments until its
// indexed bytes fit the quota.
func (s *Service) sweepChannelQuota(ctx context.Context, channel id.ID, quota int64) {
	for {
		if ctx.Err() != nil {
			return
		}
		var used int64
		if err := s.DB.Pool.QueryRow(ctx, `SELECT coalesce(sum(size_bytes),0) FROM recording_segments WHERE channel_id=$1 AND state='ready'`, channel).Scan(&used); err != nil {
			if ctx.Err() == nil {
				slog.Warn("quota usage unavailable", "channel", string(channel), "error", err)
			}
			return
		}
		if used <= quota {
			return
		}
		candidates, err := s.oldestSafeSegmentsForChannel(ctx, channel, watermarkBatch)
		if err != nil || len(candidates) == 0 {
			// Nothing removable: the remaining bytes are protected, pinned or
			// awaiting an archive copy. Quota is a housekeeping limit, not a
			// reason to break those promises — so the channel simply stays over
			// its quota and the UI reports it.
			return
		}
		deleted := 0
		for _, candidate := range candidates {
			outcome, err := s.deleteOne(ctx, "", candidate.ID, "quota")
			if err != nil {
				return
			}
			if outcome.State == deleteStateDeleted {
				deleted++
			}
		}
		if deleted == 0 {
			return
		}
	}
}

// oldestSafeSegmentsForChannel mirrors oldestSafeSegments with the channel as
// the scope. The safety predicate is identical on purpose: quota must not be a
// way around the archive safe-copy gate or a playback lease.
func (s *Service) oldestSafeSegmentsForChannel(ctx context.Context, channel id.ID, limit int) ([]watermarkCandidate, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT s.id FROM recording_segments s
		WHERE s.channel_id=$1 AND s.state='ready' AND NOT s.protected
		AND s.end_at < clock_timestamp() - $2::interval
		AND NOT EXISTS(SELECT 1 FROM recording_export_sources es JOIN recording_exports e ON e.id=es.export_id WHERE es.segment_id=s.id AND e.state IN ('queued','running'))
		AND NOT EXISTS(SELECT 1 FROM recording_archive_copies c WHERE c.segment_id=s.id AND c.state='uploading')
		AND NOT EXISTS(SELECT 1 FROM recording_playback_leases l WHERE l.segment_id=s.id AND l.lease_expires_at>clock_timestamp())
		AND (
			NOT EXISTS(SELECT 1 FROM archive_policies ap WHERE ap.channel_id=s.channel_id)
			OR EXISTS(SELECT 1 FROM recording_archive_copies c JOIN archive_policies ap ON ap.channel_id=s.channel_id
				WHERE c.segment_id=s.id AND c.state='verified'
				AND (c.cloud_expires_at IS NULL OR c.cloud_expires_at>clock_timestamp())
				AND s.end_at < clock_timestamp() - make_interval(hours => (SELECT archive_local_retention_hours FROM sites WHERE id=(SELECT site_id FROM channels WHERE id=$1))))
		)
		ORDER BY s.end_at LIMIT $3`, channel, FreshnessDelay.String(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]watermarkCandidate, 0, limit)
	for rows.Next() {
		var candidate watermarkCandidate
		if err := rows.Scan(&candidate.ID); err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}
