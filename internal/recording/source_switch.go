package recording

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/storage"
)

func (s *Service) changePhase(ctx context.Context, e *channel.Execution, w *channel.SwitchWork, phase string) error {
	var deadline *time.Time
	err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `UPDATE source_switches SET phase=$2,fencing_token=$3,phase_deadline=CASE WHEN $2 IN ('starting_new','rolling_back') THEN clock_timestamp()+interval '30 seconds' ELSE phase_deadline END WHERE id=$1 AND state='running' RETURNING phase_deadline`, w.ID, phase, e.Lease.FencingToken).Scan(&deadline)
	})
	if err == nil {
		w.Phase = phase
		w.Deadline = deadline
	}
	return err
}

func (s *Service) switchSession(ctx context.Context, e *channel.Execution, w channel.SwitchWork, revision id.ID, role string) (physicalSession, error) {
	var out physicalSession
	err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanSession(tx.QueryRow(ctx, "SELECT "+physicalColumns+" FROM stream_sessions WHERE switch_id=$1 AND operation_role=$2 ORDER BY created_at DESC LIMIT 1 FOR UPDATE", w.ID, role))
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		session, err := id.New()
		if err != nil {
			return err
		}
		var generation int64
		if err := tx.QueryRow(ctx, `UPDATE channels SET source_generation=greatest(source_generation,(SELECT coalesce(max(generation),0) FROM stream_sessions WHERE channel_id=$1))+1 WHERE id=$1 RETURNING source_generation`, e.ChannelID).Scan(&generation); err != nil {
			return err
		}
		purpose := "main"
		if role == "candidate_sub" || role == "rollback_sub" {
			purpose = "sub"
		}
		key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(session)}
		if _, err := tx.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,vhost,app,stream,purpose,switch_id,operation_role,proxy_key) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, session, e.ChannelID, revision, generation, key.VHost, key.App, key.Stream, purpose, w.ID, role, key.VHost+"/"+key.App+"/"+key.Stream); err != nil {
			return err
		}
		out = physicalSession{ID: session, ChannelID: e.ChannelID, RevisionID: revision, Key: key, State: "starting"}
		return nil
	})
	return out, err
}

func (s *Service) connectSwitchStream(ctx context.Context, e *channel.Execution, ss physicalSession, kind string) error {
	if ss.State == "closed" || ss.State == "failed" {
		return zlm.ErrProxyAbsent
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	snapshot, err := s.Media.Inspect(ctx, ss.Key)
	if err != nil && !errors.Is(err, zlm.ErrStreamAbsent) {
		return sourceFailure("inspect", err)
	}
	if errors.Is(err, zlm.ErrStreamAbsent) {
		network, err := s.FreshNetwork(ctx)
		if err != nil {
			return err
		}
		input, err := s.Sources.PrivateConnection(ctx, e.ChannelID, ss.RevisionID, kind, network)
		if err != nil {
			return sourceFailure("credentials", err)
		}
		input.Key = ss.Key
		if err := e.Check(ctx); err != nil {
			return err
		}
		if _, err := s.Media.AddProxy(ctx, input); err != nil {
			return sourceFailure("add_proxy", err)
		}
	} else if snapshot.Key != ss.Key {
		return zlm.ErrMediaOperation
	}
	if err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE stream_sessions SET state='active',started_at=coalesce(started_at,clock_timestamp()),error_code=NULL WHERE id=$1 AND state IN ('starting','active')", ss.ID)
		return err
	}); err != nil {
		return err
	}
	privateURL, err := probe.InternalURL(ss.Key, s.ProbeToken)
	if err != nil {
		return err
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	video, err := s.Probe.FirstFrame(ctx, privateURL)
	if err != nil {
		return sourceFailure("first_frame", err)
	}
	if !video.FirstFrame {
		return sourceFailure("first_frame", probe.ErrProbeFailed)
	}
	return e.Check(ctx)
}

func (s *Service) stopPhysical(ctx context.Context, e *channel.Execution, ss physicalSession) error {
	var handle Handle
	err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id,pool_id,work_relative_path FROM recording_runs WHERE stream_session_id=$1 AND state IN ('starting','recording','stopping') FOR UPDATE`, ss.ID).Scan(&handle.RunID, &handle.PoolID, &handle.RelativePath)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		handle.SessionID = ss.ID
		handle.Key = ss.Key
		return nil
	})
	if err != nil {
		return err
	}
	if handle.RunID != "" {
		if err := s.Stop(ctx, e, handle); err != nil {
			return err
		}
	}
	if err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE stream_sessions SET state='closing' WHERE id=$1 AND state NOT IN ('closed','failed')", ss.ID)
		return err
	}); err != nil {
		return err
	}
	return s.closeTestSession(ctx, e, ss)
}
func (s *Service) stopSwitchSessions(ctx context.Context, e *channel.Execution, w channel.SwitchWork, old bool) error {
	query := "SELECT " + physicalColumns + " FROM stream_sessions WHERE channel_id=$1 AND purpose IN ('main','sub') AND state NOT IN ('closed','failed') AND switch_id=$2 AND operation_role IN ('candidate_main','candidate_sub') ORDER BY created_at"
	if old {
		query = "SELECT " + physicalColumns + " FROM stream_sessions WHERE channel_id=$1 AND purpose IN ('main','sub') AND state NOT IN ('closed','failed') AND ($2::uuid IS NULL OR switch_id IS DISTINCT FROM $2) ORDER BY created_at"
	}
	var switchID *id.ID
	if w.ID != "" {
		switchID = &w.ID
	}
	rows, err := s.DB.Pool.Query(ctx, query, e.ChannelID, switchID)
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
		if err := s.stopPhysical(ctx, e, ss); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) verifySwitchTarget(ctx context.Context, e *channel.Execution, w channel.SwitchWork, rollback bool) (physicalSession, error) {
	if w.Kind == "policy_apply" || w.Kind == "pool_switch" {
		return s.verifyRecordingTarget(ctx, e, w, rollback)
	}
	revision, pool, mode, prefix := w.NewRevision, w.NewPool, w.DesiredMode, "candidate_"
	if rollback {
		revision, pool, mode, prefix = w.OldRevision, w.OldPool, w.OldMode, "rollback_"
	}
	if revision == nil {
		return physicalSession{}, nil
	}
	main, err := s.switchSession(ctx, e, w, *revision, prefix+"main")
	if err != nil {
		return main, err
	}
	if err := s.connectSwitchStream(ctx, e, main, "main"); err != nil {
		return main, err
	}
	if mode == "continuous" {
		if pool == nil {
			return main, storage.ErrMediaProof
		}
		var ready bool
		if err := s.DB.Pool.QueryRow(ctx, "SELECT count(*)=3 FROM storage_pool_checks WHERE pool_id=$1 AND state='healthy' AND expires_at>clock_timestamp()", pool).Scan(&ready); err != nil {
			return main, err
		}
		if !ready {
			if err := e.Check(ctx); err != nil {
				return main, err
			}
			if err := s.CheckPool(ctx, *pool); err != nil {
				return main, err
			}
		}
		capacity, err := s.capacityFor(ctx, e.ChannelID, main.RevisionID, *pool)
		if err != nil {
			return main, err
		}
		if !capacity.Known {
			if err := s.sampleBitratePair(ctx, e, main, ""); err != nil {
				return main, err
			}
		}
		if _, err := s.Start(ctx, e, StartInput{SessionID: main.ID, PoolID: *pool}); err != nil {
			return main, err
		}
	} else if mode == "none" {
		if err := e.Check(ctx); err != nil {
			return main, err
		}
		snapshot, err := s.Media.Inspect(ctx, main.Key)
		if err != nil {
			return main, err
		}
		if snapshot.Recording {
			return main, zlm.ErrMediaOperation
		}
	} else {
		return main, storage.ErrMediaProof
	}
	return main, nil
}

func (s *Service) trySwitchTarget(ctx context.Context, e *channel.Execution, w channel.SwitchWork, rollback bool) (physicalSession, error) {
	if w.Deadline == nil {
		return physicalSession{}, ErrPublicationConflict
	}
	window, cancel := context.WithDeadline(ctx, *w.Deadline)
	defer cancel()
	var ss physicalSession
	var last error
	for window.Err() == nil {
		ss, last = s.verifySwitchTarget(window, e, w, rollback)
		if last == nil {
			return ss, nil
		}
		// A lost owner cannot decide to rollback or commit. The next owner resumes
		// the same persisted session/run and the same original deadline.
		if err := e.Check(ctx); err != nil {
			return ss, err
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-window.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if err := e.Check(ctx); err != nil {
		return ss, err
	}
	return ss, context.DeadlineExceeded
}

func (s *Service) observeSwitch(ctx context.Context, e *channel.Execution, w channel.SwitchWork, ss physicalSession, rollback bool) error {
	revision := w.NewRevision
	if rollback {
		revision = w.OldRevision
	}
	var sub *physicalSession
	subState, subReason := "not_configured", "sub_not_configured"
	if revision != nil {
		// A sub failure is a visible degradation, never a main rollback trigger.
		network, err := s.FreshNetwork(ctx)
		if err != nil {
			return err
		}
		input, err := s.Sources.PrivateConnection(ctx, e.ChannelID, *revision, "sub", network)
		if err == nil {
			_ = input // decrypted settings are reloaded at the last connection boundary.
			prefix := "candidate_"
			if rollback {
				prefix = "rollback_"
			}
			candidate, err := s.switchSession(ctx, e, w, *revision, prefix+"sub")
			if err != nil {
				return err
			}
			sub = &candidate
			subState, subReason = "unavailable", "sub_source_unavailable"
			window, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = s.connectSwitchStream(window, e, candidate, "sub")
			cancel()
			if err == nil {
				subState, subReason = "healthy", "decoded_first_frame"
			} else {
				subReason = subFailureReason(err)
				if err := e.Check(ctx); err != nil {
					return err
				}
				if err := s.stopPhysical(ctx, e, candidate); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, zlm.ErrInvalidMediaInput) {
			return err
		}
	}
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		mainState, mainReason := "healthy", "decoded_first_frame"
		if revision == nil {
			mainState, mainReason = "not_configured", "source_not_configured"
		}
		mode := w.DesiredMode
		if rollback {
			mode = w.OldMode
		}
		recState, recReason := "healthy", "recorder_verified"
		if revision == nil || mode == "none" {
			recState, recReason = "disabled", "recording_disabled"
		}
		var mainID, subID *id.ID
		if ss.ID != "" {
			mainID = &ss.ID
		}
		if sub != nil {
			subID = &sub.ID
		}
		for _, o := range []struct {
			kind, state, reason string
			session             *id.ID
		}{{"main", mainState, mainReason, mainID}, {"sub", subState, subReason, subID}, {"recording", recState, recReason, mainID}} {
			obs, err := id.New()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,session_id,kind,state,reason_code,observed_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,clock_timestamp(),clock_timestamp()+interval '30 seconds')`, obs, e.ChannelID, revision, o.session, o.kind, o.state, o.reason); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Service) finishSwitch(ctx context.Context, e *channel.Execution, w *channel.SwitchWork, phase, reason string) error {
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		state := "succeeded"
		current, pool, mode := w.NewRevision, w.NewPool, w.DesiredMode
		if phase == "rolled_back" || phase == "failed" {
			state = phase
			current, pool, mode = w.OldRevision, w.OldPool, w.OldMode
		}
		if _, err := tx.Exec(ctx, "UPDATE channels SET current_revision_id=$2,desired_revision_id=$2,storage_pool_id=$3,version=version+1 WHERE id=$1", e.ChannelID, current, pool); err != nil {
			return err
		}
		if w.Kind != "clear" {
			if _, err := tx.Exec(ctx, "UPDATE recording_policies SET mode=$2,version=version+1 WHERE channel_id=$1 AND mode IS DISTINCT FROM $2", e.ChannelID, mode); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE source_switches SET state=$2,phase=$3,finished_at=clock_timestamp(),error_code=NULLIF($4,''),fencing_token=$5 WHERE id=$1 AND state='running'", w.ID, state, phase, reason, e.Lease.FencingToken); err != nil {
			return err
		}
		if phase != "failed" {
			if _, err := tx.Exec(ctx, "UPDATE recording_gaps SET end_at=clock_timestamp() WHERE switch_id=$1 AND end_at IS NULL", w.ID); err != nil {
				return err
			}
		} else {
			for _, kind := range []string{"main", "recording"} {
				obs, _ := id.New()
				if _, err := tx.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,kind,state,reason_code,observed_at,expires_at) VALUES($1,$2,$3,$4,'unavailable','source_rollback_failed',clock_timestamp(),clock_timestamp()+interval '30 seconds')`, obs, e.ChannelID, current, kind); err != nil {
					return err
				}
			}
		}
		w.State = state
		w.Phase = phase
		w.ErrorCode = reason
		return nil
	})
}

func switchJobResult(w channel.SwitchWork) (jobs.Result, error) {
	if w.State != "succeeded" {
		code := w.ErrorCode
		if code == "" {
			code = "source_change_failed"
		}
		return jobs.Result{}, &jobs.PermanentFailure{Code: code}
	}
	raw, err := json.Marshal(struct {
		SwitchID id.ID  `json:"switch_id"`
		State    string `json:"state"`
	}{w.ID, w.State})
	return jobs.Result{Payload: raw}, err
}

func (s *Service) ExecuteSourceChange(ctx context.Context, lease jobs.Lease) (jobs.Result, error) {
	if s.Sources == nil || s.Media == nil || s.Probe == nil || s.FreshNetwork == nil {
		return jobs.Result{}, ErrPublicationUnavailable
	}
	task, err := channel.DecodeSourceTask(lease.Payload)
	if err != nil {
		return jobs.Result{}, err
	}
	e, err := channel.NewExecution(ctx, s.DB, lease, task.ChannelID)
	if err != nil {
		return jobs.Result{}, err
	}
	defer e.Close()
	ctx = e.Context()
	w, err := s.Sources.PrepareChange(ctx, e)
	if err != nil {
		return jobs.Result{}, err
	}
	if w.State != "running" {
		return switchJobResult(w)
	}
	if w.Kind == "policy_apply" || w.Kind == "pool_switch" {
		return s.executeRecordingChange(ctx, e, w)
	}
	if w.Phase == "testing" {
		if w.Kind == "apply" && w.DesiredMode == "continuous" {
			if w.NewPool == nil {
				return jobs.Result{}, storage.ErrMediaProof
			}
			var ready bool
			if err := s.DB.Pool.QueryRow(ctx, "SELECT count(*)=3 FROM storage_pool_checks WHERE pool_id=$1 AND state='healthy' AND expires_at>clock_timestamp()", w.NewPool).Scan(&ready); err != nil {
				return jobs.Result{}, err
			}
			if !ready {
				if err := e.Check(ctx); err != nil {
					return jobs.Result{}, err
				}
				if err := s.CheckPool(ctx, *w.NewPool); err != nil {
					return jobs.Result{}, err
				}
			}
			if w.NewRevision == nil {
				return jobs.Result{}, ErrPublicationConflict
			}
			capacity, err := s.capacityFor(ctx, e.ChannelID, *w.NewRevision, *w.NewPool)
			if err != nil {
				return jobs.Result{}, err
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
		}
		if w.Kind == "apply" && w.OldRevision != nil && w.NewRevision != nil && *w.OldRevision == *w.NewRevision {
			if err := s.finishSwitch(ctx, e, &w, "committed", ""); err != nil {
				return jobs.Result{}, err
			}
			return switchJobResult(w)
		}
		if err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			gap, err := id.New()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO recording_gaps(id,channel_id,source_revision_id,switch_id,start_at,reason_code) VALUES($1,$2,$3,$4,clock_timestamp(),'source_switch')`, gap, e.ChannelID, w.OldRevision, w.ID); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, "UPDATE source_switches SET phase='stopping_old',fencing_token=$2 WHERE id=$1", w.ID, e.Lease.FencingToken)
			return err
		}); err != nil {
			return jobs.Result{}, err
		}
		w.Phase = "stopping_old"
	}
	if w.Phase == "stopping_old" {
		if err := s.stopSwitchSessions(ctx, e, w, true); err != nil {
			return jobs.Result{}, err
		}
		if w.Kind == "clear" {
			if err := s.observeSwitch(ctx, e, w, physicalSession{}, false); err != nil {
				return jobs.Result{}, err
			}
			if err := s.finishSwitch(ctx, e, &w, "committed", ""); err != nil {
				return jobs.Result{}, err
			}
			return switchJobResult(w)
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
		ss, err := s.trySwitchTarget(ctx, e, w, false)
		if err == nil {
			if err := s.observeSwitch(ctx, e, w, ss, false); err != nil {
				return jobs.Result{}, err
			}
			if err := s.finishSwitch(ctx, e, &w, "committed", ""); err != nil {
				return jobs.Result{}, err
			}
			return switchJobResult(w)
		}
		if err := e.Check(ctx); err != nil {
			return jobs.Result{}, err
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return jobs.Result{}, err
		}
		if err := s.changePhase(ctx, e, &w, "rolling_back"); err != nil {
			return jobs.Result{}, err
		}
	}
	if w.Phase == "rolling_back" {
		if err := s.stopSwitchSessions(ctx, e, w, false); err != nil {
			return jobs.Result{}, err
		}
		ss, err := s.trySwitchTarget(ctx, e, w, true)
		if err == nil {
			if err := s.observeSwitch(ctx, e, w, ss, true); err != nil {
				return jobs.Result{}, err
			}
			if err := s.finishSwitch(ctx, e, &w, "rolled_back", "new_source_unavailable"); err != nil {
				return jobs.Result{}, err
			}
		} else {
			if err := e.Check(ctx); err != nil {
				return jobs.Result{}, err
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				return jobs.Result{}, err
			}
			// Failed rollback sessions still have cleanup receipts. Do not mark a
			// terminal failure while an unconfirmed recorder or proxy can remain live.
			rows, err := s.DB.Pool.Query(ctx, "SELECT "+physicalColumns+" FROM stream_sessions WHERE switch_id=$1 AND state NOT IN ('closed','failed')", w.ID)
			if err != nil {
				return jobs.Result{}, err
			}
			var all []physicalSession
			for rows.Next() {
				ss, err := scanSession(rows)
				if err != nil {
					rows.Close()
					return jobs.Result{}, err
				}
				all = append(all, ss)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return jobs.Result{}, err
			}
			for _, ss := range all {
				if err := s.stopPhysical(ctx, e, ss); err != nil {
					return jobs.Result{}, err
				}
			}
			if err := s.finishSwitch(ctx, e, &w, "failed", "source_rollback_failed"); err != nil {
				return jobs.Result{}, err
			}
		}
		return switchJobResult(w)
	}
	return jobs.Result{}, ErrPublicationConflict
}
