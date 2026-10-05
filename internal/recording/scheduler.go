package recording

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

type desiredRecording struct {
	Revision, Pool *id.ID
	Mode           string
	Enabled        bool
}

func (s *Service) Reconcile(ctx context.Context, ch id.ID) error {
	if s.Media == nil || s.Sources == nil {
		return ErrPublicationUnavailable
	}
	e, err := channel.NewRuntimeExecution(ctx, s.DB, ch)
	if err != nil {
		return err
	}
	defer e.Close()
	ctx = e.Context()
	var desired desiredRecording
	if err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT c.current_revision_id,c.storage_pool_id,c.enabled,p.mode FROM channels c JOIN recording_policies p ON p.channel_id=c.id WHERE c.id=$1`, ch).Scan(&desired.Revision, &desired.Pool, &desired.Enabled, &desired.Mode)
	}); err != nil {
		return err
	}
	if !desired.Enabled || desired.Revision == nil {
		return s.stopSwitchSessions(ctx, e, channel.SwitchWork{}, true)
	}
	ss, err := s.currentMain(ctx, e, desired.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.recoverRuntimeSource(ctx, e, *desired.Revision)
	}
	if err != nil {
		return err
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	actual, err := s.Media.Inspect(ctx, ss.Key)
	if err != nil {
		if errors.Is(err, zlm.ErrStreamAbsent) {
			if err := s.runtimeGap(ctx, e, ss, "source_unavailable", true); err != nil {
				return err
			}
			if err := s.observation(ctx, e, ss, "main", "unavailable", "source_unavailable", nil); err != nil {
				return err
			}
			if err := s.stopPhysical(ctx, e, ss); err != nil {
				return err
			}
			return s.recoverRuntimeSource(ctx, e, *desired.Revision)
		}
		if err := s.runtimeGap(ctx, e, ss, "source_observation_unknown", false); err != nil {
			return err
		}
		return s.observation(ctx, e, ss, "main", "unknown", "source_observation_failed", nil)
	}
	state, reason, err := s.runtimeFrameStatus(ctx, e, ss, actual, "main")
	if err != nil {
		return err
	}
	if err := s.observation(ctx, e, ss, "main", state, reason, nil); err != nil {
		return err
	}
	if state == "unavailable" {
		if err := s.runtimeGap(ctx, e, ss, reason, true); err != nil {
			return err
		}
		if err := s.stopPhysical(ctx, e, ss); err != nil {
			return err
		}
		return s.observation(ctx, e, ss, "recording", "unavailable", reason, nil)
	}
	if err := s.reconcileRuntimeSub(ctx, e, *desired.Revision); err != nil {
		return err
	}
	if desired.Mode == "none" {
		if actual.Recording {
			return s.stopRecorder(ctx, e, ss)
		}
		return s.observation(ctx, e, ss, "recording", "disabled", "recording_disabled", nil)
	}
	if desired.Mode != "continuous" {
		return nil
	}
	if desired.Pool == nil {
		return ErrPublicationUnavailable
	}
	root, pool, poolErr := s.Pools.OpenMediaRoot(ctx, *desired.Pool)
	if root != nil {
		root.Close()
	}
	if poolErr != nil || !pool.Enabled {
		reason := "pool_unavailable"
		if poolErr == nil && !pool.Enabled {
			reason = "pool_disabled"
		}
		if err := s.stopRecorder(ctx, e, ss); err != nil {
			return err
		}
		if err := s.capacityBlock(ctx, e, "pool_unavailable"); err != nil {
			return err
		}
		if err := s.runtimeGap(ctx, e, ss, reason, true); err != nil {
			return err
		}
		return s.observation(ctx, e, ss, "recording", "unavailable", reason, nil)
	}
	var poolHealthy bool
	if err := s.DB.Pool.QueryRow(ctx, "SELECT count(*)=2 FROM storage_pool_checks WHERE pool_id=$1 AND service IN ('api','worker') AND state='healthy' AND expires_at>clock_timestamp()", desired.Pool).Scan(&poolHealthy); err != nil {
		return err
	}
	if !poolHealthy {
		var unavailable bool
		if err := s.DB.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM storage_pool_checks WHERE pool_id=$1 AND service IN ('api','worker') AND state='unavailable' AND expires_at>clock_timestamp())", desired.Pool).Scan(&unavailable); err != nil {
			return err
		}
		if unavailable {
			return s.pauseRuntimeRecording(ctx, e, ss, "pool_unavailable")
		}
		if err := s.runtimeGap(ctx, e, ss, "pool_observation_stale", false); err != nil {
			return err
		}
		return s.observation(ctx, e, ss, "recording", "unknown", "pool_observation_stale", nil)
	}
	capacity, err := s.capacityFor(ctx, e.ChannelID, ss.RevisionID, *desired.Pool)
	if err != nil {
		return err
	}
	if capacity.Free < capacity.Line {
		return s.pauseRuntimeRecording(ctx, e, ss, "low_space")
	}
	if actual.Recording {
		// Do not restart a healthy upstream recorder merely because Worker restarted.
		var run id.ID
		if err := s.DB.Pool.QueryRow(ctx, "SELECT id FROM recording_runs WHERE stream_session_id=$1 AND pool_id=$2 AND state IN ('starting','recording')", ss.ID, *desired.Pool).Scan(&run); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrPublicationConflict
			}
			return err
		}
		if err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "UPDATE recording_runs SET state='recording',started_at=coalesce(started_at,clock_timestamp()) WHERE id=$1", run)
			return err
		}); err != nil {
			return err
		}
		state, reason, err := s.completionStatus(ctx, ss)
		if err != nil {
			return err
		}
		if state == "healthy" {
			if err := s.closeRuntimeGaps(ctx, e); err != nil {
				return err
			}
		} else if reason == "completion_stale" {
			if err := s.runtimeGap(ctx, e, ss, reason, false); err != nil {
				return err
			}
		}
		return s.observation(ctx, e, ss, "recording", state, reason, map[string]bool{"recording_flag": true})
	}
	_, err = s.Start(ctx, e, StartInput{SessionID: ss.ID, PoolID: *desired.Pool})
	if err == nil {
		return s.observation(ctx, e, ss, "recording", "unknown", "first_completion_pending", nil)
	}
	var public *fault.Error
	if errors.As(err, &public) {
		state := "unavailable"
		if public.Code == "bitrate_unknown" || public.Code == "capacity_unknown" {
			state = "unknown"
		}
		return s.observation(ctx, e, ss, "recording", state, public.Code, nil)
	}
	return err
}

func (s *Service) pauseRuntimeRecording(ctx context.Context, e *channel.Execution, ss physicalSession, reason string) error {
	if err := s.stopRecorder(ctx, e, ss); err != nil {
		return err
	}
	if err := s.capacityBlock(ctx, e, reason); err != nil {
		return err
	}
	if err := s.runtimeGap(ctx, e, ss, reason, true); err != nil {
		return err
	}
	return s.observation(ctx, e, ss, "recording", "unavailable", reason, nil)
}

func (s *Service) runtimeFrameStatus(ctx context.Context, e *channel.Execution, ss physicalSession, snapshot zlm.StreamSnapshot, kind string) (string, string, error) {
	var frames int64
	var previous time.Time
	err := s.DB.Pool.QueryRow(ctx, "SELECT frames,observed_at FROM recording_bitrate_samples WHERE stream_session_id=$1 ORDER BY observed_at DESC LIMIT 1", ss.ID).Scan(&frames, &previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	if err == nil && snapshot.ObservedAt.Sub(previous) < 10*time.Second {
		return "unknown", "frame_progress_pending", nil
	}
	if err == nil && snapshot.ObservedAt.Sub(previous) <= 30*time.Second {
		valid, err := s.sampleBitrate(ctx, e, ss, "", false)
		if err != nil {
			return "", "", err
		}
		if videoFrames(snapshot) <= frames {
			return "unavailable", "frame_progress_stalled", nil
		}
		if valid {
			return "healthy", "frame_progress_observed", nil
		}
		return "unknown", "bitrate_unknown", nil
	}
	if err := s.connectSwitchStream(ctx, e, ss, kind); err != nil {
		return "unavailable", "source_unavailable", nil
	}
	_, err = s.sampleBitrate(ctx, e, ss, "", true)
	return "unknown", "frame_progress_pending", err
}
