package recording

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/storage"
)

func (s *Service) currentMain(ctx context.Context, e *channel.Execution, revision *id.ID) (physicalSession, error) {
	if revision == nil {
		return physicalSession{}, nil
	}
	return scanSession(s.DB.Pool.QueryRow(ctx, "SELECT "+physicalColumns+" FROM stream_sessions WHERE channel_id=$1 AND source_revision_id=$2 AND purpose='main' AND state='active' ORDER BY generation DESC,created_at DESC LIMIT 1", e.ChannelID, revision))
}
func (s *Service) stopRecorder(ctx context.Context, e *channel.Execution, ss physicalSession) error {
	var h Handle
	err := s.DB.Pool.QueryRow(ctx, "SELECT id,pool_id,work_relative_path FROM recording_runs WHERE stream_session_id=$1 AND state IN ('starting','recording','stopping')", ss.ID).Scan(&h.RunID, &h.PoolID, &h.RelativePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	h.SessionID = ss.ID
	h.Key = ss.Key
	return s.Stop(ctx, e, h)
}
func (s *Service) verifyRecordingTarget(ctx context.Context, e *channel.Execution, w channel.SwitchWork, rollback bool) (physicalSession, error) {
	revision, pool, mode := w.NewRevision, w.NewPool, w.DesiredMode
	if rollback {
		revision, pool, mode = w.OldRevision, w.OldPool, w.OldMode
	}
	ss, err := s.currentMain(ctx, e, revision)
	if mode == "none" && errors.Is(err, pgx.ErrNoRows) {
		return physicalSession{}, nil
	}
	if err != nil {
		return ss, err
	}
	if ss.ID == "" {
		if mode == "none" {
			return ss, nil
		}
		return ss, zlm.ErrStreamAbsent
	}
	if mode == "none" {
		if err := e.Check(ctx); err != nil {
			return ss, err
		}
		snapshot, err := s.Media.Inspect(ctx, ss.Key)
		if err != nil && !errors.Is(err, zlm.ErrStreamAbsent) {
			return ss, err
		}
		if err == nil && snapshot.Recording {
			return ss, zlm.ErrMediaOperation
		}
		return ss, nil
	}
	if pool == nil {
		return ss, storage.ErrMediaProof
	}
	_, err = s.Start(ctx, e, StartInput{SessionID: ss.ID, PoolID: *pool})
	return ss, err
}
func (s *Service) observeRecordingChange(ctx context.Context, e *channel.Execution, w channel.SwitchWork, rollback bool) error {
	state, reason, revision := "healthy", "recorder_verified", w.NewRevision
	mode := w.DesiredMode
	if rollback {
		mode = w.OldMode
		revision = w.OldRevision
	}
	if mode == "none" {
		state, reason = "disabled", "recording_disabled"
	}
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		observation, err := id.New()
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,kind,state,reason_code,observed_at,expires_at) VALUES($1,$2,$3,'recording',$4,$5,clock_timestamp(),clock_timestamp()+interval '30 seconds')`, observation, e.ChannelID, revision, state, reason)
		return err
	})
}

func (s *Service) executeRecordingChange(ctx context.Context, e *channel.Execution, w channel.SwitchWork) (jobs.Result, error) {
	if w.Phase == "testing" {
		samePool := w.OldPool == nil && w.NewPool == nil || w.OldPool != nil && w.NewPool != nil && *w.OldPool == *w.NewPool
		if w.OldMode == w.DesiredMode && samePool {
			if err := s.finishSwitch(ctx, e, &w, "committed", ""); err != nil {
				return jobs.Result{}, err
			}
			return switchJobResult(w)
		}
		if w.DesiredMode == "continuous" {
			if w.NewPool == nil || w.NewRevision == nil {
				return jobs.Result{}, storage.ErrMediaProof
			}
			capacity, err := s.capacityFor(ctx, e.ChannelID, *w.NewRevision, *w.NewPool)
			if err != nil {
				return jobs.Result{}, err
			}
			if !capacity.Known {
				// This job owns the channel, so Monitor cannot refresh its stale
				// samples. Gather real frame/rate evidence before deciding to stop
				// the old recorder; the shared-filesystem capacity gate stays intact.
				ss, err := s.currentMain(ctx, e, w.NewRevision)
				if err != nil {
					return jobs.Result{}, err
				}
				window, cancel := context.WithTimeout(ctx, 20*time.Second)
				refreshErr := s.connectSwitchStream(window, e, ss, "main")
				if refreshErr == nil {
					refreshErr = s.sampleBitratePair(window, e, ss, "")
				}
				cancel()
				if err := e.Check(ctx); err != nil {
					return jobs.Result{}, err
				}
				if refreshErr == nil {
					capacity, err = s.capacityFor(ctx, e.ChannelID, *w.NewRevision, *w.NewPool)
					if err != nil {
						return jobs.Result{}, err
					}
				}
			}
			reason := ""
			if !capacity.Known {
				reason = "bitrate_unknown"
			} else if capacity.Free < capacity.Line {
				reason = "low_space"
			}
			if reason != "" {
				if err := s.finishSwitch(ctx, e, &w, "rolled_back", reason); err != nil {
					return jobs.Result{}, err
				}
				return jobs.Result{}, &jobs.PermanentFailure{Code: reason}
			}
			var proof bool
			if err := s.DB.Pool.QueryRow(ctx, "SELECT count(*)=3 FROM storage_pool_checks WHERE pool_id=$1 AND state='healthy' AND expires_at>clock_timestamp()", w.NewPool).Scan(&proof); err != nil {
				return jobs.Result{}, err
			}
			if !proof {
				return jobs.Result{}, fault.New(409, "pool_unavailable", "目标池缺少新鲜写入证据")
			}
		}
		if err := s.changePhase(ctx, e, &w, "stopping_old"); err != nil {
			return jobs.Result{}, err
		}
	}
	if w.Phase == "stopping_old" {
		ss, err := s.currentMain(ctx, e, w.OldRevision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return jobs.Result{}, err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if err := s.stopMappedRevisionRecorders(ctx, e, w.OldRevision); err != nil {
				return jobs.Result{}, err
			}
		}
		if ss.ID != "" {
			if err := s.stopRecorder(ctx, e, ss); err != nil {
				return jobs.Result{}, err
			}
		}
		if err := s.changePhase(ctx, e, &w, "starting_new"); err != nil {
			return jobs.Result{}, err
		}
	}
	if w.Phase == "starting_new" || w.Phase == "verifying_new" {
		if w.Phase == "starting_new" {
			if err := s.changePhase(ctx, e, &w, "verifying_new"); err != nil {
				return jobs.Result{}, err
			}
		}
		if _, err := s.trySwitchTarget(ctx, e, w, false); err == nil {
			if err := s.observeRecordingChange(ctx, e, w, false); err != nil {
				return jobs.Result{}, err
			}
			if err := s.finishSwitch(ctx, e, &w, "committed", ""); err != nil {
				return jobs.Result{}, err
			}
			return switchJobResult(w)
		} else if err := e.Check(ctx); err != nil {
			return jobs.Result{}, err
		}
		if err := s.changePhase(ctx, e, &w, "rolling_back"); err != nil {
			return jobs.Result{}, err
		}
	}
	if w.Phase == "rolling_back" {
		ss, err := s.currentMain(ctx, e, w.OldRevision)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return jobs.Result{}, err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			if err := s.stopMappedRevisionRecorders(ctx, e, w.OldRevision); err != nil {
				return jobs.Result{}, err
			}
		}
		if ss.ID != "" {
			if err := s.stopRecorder(ctx, e, ss); err != nil {
				return jobs.Result{}, err
			}
		}
		if _, err := s.trySwitchTarget(ctx, e, w, true); err == nil {
			if err := s.observeRecordingChange(ctx, e, w, true); err != nil {
				return jobs.Result{}, err
			}
			if err := s.finishSwitch(ctx, e, &w, "rolled_back", "recording_change_failed"); err != nil {
				return jobs.Result{}, err
			}
		} else {
			if err := e.Check(ctx); err != nil {
				return jobs.Result{}, err
			}
			if ss.ID != "" {
				if err := s.stopRecorder(ctx, e, ss); err != nil {
					return jobs.Result{}, err
				}
			}
			if err := s.finishSwitch(ctx, e, &w, "failed", "recording_rollback_failed"); err != nil {
				return jobs.Result{}, err
			}
		}
		return switchJobResult(w)
	}
	return jobs.Result{}, ErrPublicationConflict
}

// A closed main receipt can still have a durable stop awaiting its final commit.
// Stop only this revision's mapped run(s), retaining its proxy/history identity.
func (s *Service) stopMappedRevisionRecorders(ctx context.Context, e *channel.Execution, revision *id.ID) error {
	if revision == nil {
		return nil
	}
	rows, err := s.DB.Pool.Query(ctx, "SELECT "+physicalColumns+" FROM stream_sessions WHERE channel_id=$1 AND source_revision_id=$2 AND purpose='main' AND id IN (SELECT stream_session_id FROM recording_runs WHERE state IN ('starting','recording','stopping'))", e.ChannelID, revision)
	if err != nil {
		return err
	}
	var all []physicalSession
	for rows.Next() {
		ss, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return err
		}
		all = append(all, ss)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, ss := range all {
		if err := s.stopRecorder(ctx, e, ss); err != nil {
			return err
		}
	}
	return nil
}
