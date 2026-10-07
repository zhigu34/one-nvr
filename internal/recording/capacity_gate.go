package recording

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

type filesystemCapacity struct {
	Line, Free int64
	Known      bool
}

func (s *Service) capacityFor(ctx context.Context, ch, revision, pool id.ID) (filesystemCapacity, error) {
	var out filesystemCapacity
	var filesystem string
	var peers, identities int
	if err := s.DB.Pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT nullif(filesystem_id,'')),coalesce(min(nullif(filesystem_id,'')),''),coalesce(min(free_bytes),0) FROM storage_pool_checks WHERE pool_id=$1 AND service IN ('api','worker') AND state='healthy' AND expires_at>clock_timestamp()`, pool).Scan(&peers, &identities, &filesystem, &out.Free); err != nil {
		return out, err
	}
	if peers != 2 || identities != 1 || filesystem == "" {
		return out, fault.New(409, "capacity_unknown", "缺少存储池的新鲜容量证据")
	}
	expected := map[id.ID]id.ID{ch: revision}
	rows, err := s.DB.Pool.Query(ctx, `SELECT c.id,CASE WHEN sw.id IS NOT NULL THEN sw.new_revision_id ELSE c.current_revision_id END,CASE WHEN sw.id IS NOT NULL THEN sw.new_pool_id ELSE c.storage_pool_id END FROM channels c JOIN recording_policies p ON p.channel_id=c.id LEFT JOIN source_switches sw ON sw.channel_id=c.id AND sw.state IN ('queued','running') WHERE c.enabled AND EXISTS(SELECT 1 FROM storage_pools target_pool WHERE target_pool.id=CASE WHEN sw.id IS NOT NULL THEN sw.new_pool_id ELSE c.storage_pool_id END AND target_pool.enabled) AND CASE WHEN sw.id IS NOT NULL THEN sw.desired_mode ELSE p.mode END='continuous'`)
	if err != nil {
		return out, err
	}
	type target struct {
		ch             id.ID
		revision, pool *id.ID
	}
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.ch, &t.revision, &t.pool); err != nil {
			rows.Close()
			return out, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, t := range targets {
		if t.ch == ch || t.revision == nil || t.pool == nil {
			continue
		}
		var fs string
		if err := s.DB.Pool.QueryRow(ctx, `SELECT coalesce((SELECT nullif(filesystem_id,'') FROM storage_pool_checks WHERE pool_id=$1 AND service='worker'),(SELECT nullif(filesystem_id,'') FROM storage_pool_checks WHERE pool_id=$1 AND service='api'),'')`, t.pool).Scan(&fs); err != nil {
			return out, err
		}
		if fs == filesystem || *t.pool == pool {
			expected[t.ch] = *t.revision
		}
	}
	samples := make([]BitrateSample, 0, len(expected)*4)
	now := time.Now().UTC()
	for target, source := range expected {
		samples = append(samples, BitrateSample{ChannelID: target})
		rows, err := s.DB.Pool.Query(ctx, `SELECT b.bytes_per_second,b.valid,b.observed_at FROM recording_bitrate_samples b JOIN stream_sessions ss ON ss.id=b.stream_session_id WHERE b.channel_id=$1 AND b.source_revision_id=$2 AND (ss.purpose='main' OR ss.operation_role='test_main') AND b.observed_at>clock_timestamp()-interval '5 minutes' ORDER BY b.observed_at DESC LIMIT 128`, target, source)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			sample := BitrateSample{ChannelID: target, Kind: "stream"}
			if err := rows.Scan(&sample.BytesPerSecond, &sample.Valid, &sample.ObservedAt); err != nil {
				rows.Close()
				return out, err
			}
			samples = append(samples, sample)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
		rows, err = s.DB.Pool.Query(ctx, `SELECT size_bytes,extract(epoch FROM end_at-start_at)::double precision,end_at FROM recording_segments WHERE channel_id=$1 AND source_revision_id=$2 AND state='ready' AND end_at>clock_timestamp()-interval '5 minutes' ORDER BY end_at DESC LIMIT 16`, target, source)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var size int64
			var duration float64
			sample := BitrateSample{ChannelID: target, Kind: "segment", Valid: true}
			if err := rows.Scan(&size, &duration, &sample.ObservedAt); err != nil {
				rows.Close()
				return out, err
			}
			if duration > 0 {
				rate := math.Ceil(float64(size) / duration)
				if rate >= float64(math.MaxInt64) {
					sample.BytesPerSecond = math.MaxInt64
				} else {
					sample.BytesPerSecond = int64(rate)
				}
			}
			samples = append(samples, sample)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	out.Line, out.Known = safetyLine(now, samples)
	return out, nil
}

func (s *Service) capacityBlock(ctx context.Context, e *channel.Execution, reason string) error {
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO recording_capacity_blocks(channel_id,reason_code) VALUES($1,$2) ON CONFLICT(channel_id) DO UPDATE SET reason_code=CASE WHEN EXCLUDED.reason_code='bitrate_unknown' AND recording_capacity_blocks.reason_code IN ('low_space','pool_unavailable') THEN recording_capacity_blocks.reason_code ELSE EXCLUDED.reason_code END,healthy_samples=0,last_healthy_at=NULL,blocked_at=clock_timestamp()`, e.ChannelID, reason)
		return err
	})
}
func (s *Service) checkStartCapacity(ctx context.Context, e *channel.Execution, ss physicalSession, pool id.ID) error {
	capacity, err := s.capacityFor(ctx, e.ChannelID, ss.RevisionID, pool)
	if err != nil {
		return err
	}
	if !capacity.Known {
		if err := s.capacityBlock(ctx, e, "bitrate_unknown"); err != nil {
			return err
		}
		return fault.New(409, "bitrate_unknown", "缺少间隔至少十秒的新鲜主流码率证据")
	}
	if capacity.Free < capacity.Line {
		if err := s.capacityBlock(ctx, e, "low_space"); err != nil {
			return err
		}
		return fault.New(409, "low_space", "存储空间低于录像安全线")
	}
	ready := true
	err = e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var reason string
		err := tx.QueryRow(ctx, "SELECT reason_code FROM recording_capacity_blocks WHERE channel_id=$1 FOR UPDATE", e.ChannelID).Scan(&reason)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if reason == "low_space" || reason == "pool_unavailable" {
			recovery := capacity.Line
			if recovery <= math.MaxInt64/2 {
				recovery *= 2
			} else {
				recovery = math.MaxInt64
			}
			if capacity.Free < recovery {
				ready = false
				_, err := tx.Exec(ctx, "UPDATE recording_capacity_blocks SET healthy_samples=0,last_healthy_at=NULL,blocked_at=clock_timestamp() WHERE channel_id=$1", e.ChannelID)
				return err
			}
			var healthy int
			if err := tx.QueryRow(ctx, `UPDATE recording_capacity_blocks SET healthy_samples=least(2,healthy_samples+CASE WHEN last_healthy_at IS NULL OR last_healthy_at<=clock_timestamp()-interval '10 seconds' THEN 1 ELSE 0 END),last_healthy_at=CASE WHEN last_healthy_at IS NULL OR last_healthy_at<=clock_timestamp()-interval '10 seconds' THEN clock_timestamp() ELSE last_healthy_at END,blocked_at=clock_timestamp() WHERE channel_id=$1 RETURNING healthy_samples`, e.ChannelID).Scan(&healthy); err != nil {
				return err
			}
			if healthy < 2 {
				ready = false
				return nil
			}
		}
		_, err = tx.Exec(ctx, "DELETE FROM recording_capacity_blocks WHERE channel_id=$1", e.ChannelID)
		return err
	})
	if err != nil {
		return err
	}
	if !ready {
		return fault.New(409, "capacity_recovery_wait", "空间恢复需达到两倍安全线并连续确认两次")
	}
	return nil
}
