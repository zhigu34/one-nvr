package recording

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"github.com/zhigu34/one-nvr/internal/storage"
)

type desiredRecording struct {
	Revision, Pool *id.ID
	// Mode is the effective decision for this tick, not the stored setting: the
	// union of the policy mode and any other reason to record (REC-04).
	Mode    string
	Enabled bool
	// EventRecording is the channel's own switch (REC-09). It decides the buffer,
	// never ordinary recording: REC-10 requires the switch to leave continuous
	// and scheduled recording untouched.
	EventRecording bool
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
	// The zone is loaded before the transaction because a schedule is only
	// meaningful in site-local wall clock, and a failed load must not silently
	// fall back to UTC — recording at the wrong hour is worse than not recording.
	zone, _, err := s.siteZone(ctx, ch)
	if err != nil {
		return err
	}
	if err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var policyMode string
		if err := tx.QueryRow(ctx, `SELECT c.current_revision_id,c.storage_pool_id,c.enabled,p.mode,p.event_recording FROM channels c JOIN recording_policies p ON p.channel_id=c.id WHERE c.id=$1`, ch).Scan(&desired.Revision, &desired.Pool, &desired.Enabled, &policyMode, &desired.EventRecording); err != nil {
			return err
		}
		// The union is computed here, inside the same transaction that read the
		// policy, so the two cannot disagree about the same instant.
		active, err := manualActiveTx(ctx, tx, ch)
		if err != nil {
			return err
		}
		// Only a planned channel has a reason to read its schedule: an operator
		// who set continuous or none did not ask for a weekly rule, and an
		// orphaned schedule from a previous mode must not resurrect recording.
		scheduled := false
		if policyMode == "planned" {
			scheduled, err = s.scheduleActiveTx(ctx, tx, ch, zone, time.Now())
			if err != nil {
				return err
			}
		}
		desired.Mode = unionMode(policyMode, active, scheduled)
		return nil
	}); err != nil {
		return err
	}
	if !desired.Enabled || desired.Revision == nil {
		// Stop the event buffer too. Returning here left the row alive, its
		// heartbeat kept fresh by TouchBuffers, so re-enabling the channel found a
		// "live" buffer, skipped the restart — but the proxy had gone with the
		// channel, and every event then assembled from an empty ring, silently.
		if err := s.StopBuffer(ctx, e.ChannelID); err != nil {
			return err
		}
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
			// One absent sample is not proof the source is gone: ZLM reports
			// online=false briefly while a large keyframe is ingested. Record
			// the gap but keep the session, so a stream that comes back reads as
			// one interval instead of a close-and-rebuild cycle.
			if err := s.runtimeGap(ctx, e, ss, "source_unavailable", true); err != nil {
				return err
			}
			gaps, gapErr := s.consecutiveGapSamples(ctx, e.ChannelID, "main", "stream_present_pending")
			if gapErr != nil {
				return gapErr
			}
			if gaps < streamGapGraceSamples {
				return s.observation(ctx, e, ss, "main", "unknown", "stream_present_pending", nil)
			}
			// Persistent absence: distinguish "the pull is reconnecting" from
			// "the media server dropped the pull". A registered proxy is still
			// ours and comes back on its own; only a proxy the media server no
			// longer holds means this session is finished.
			//
			// A failed query is NOT evidence either way, and it must not stop
			// recovery: the media server's API can be briefly unreachable while
			// the stream is genuinely gone, and treating "cannot tell" as
			// "leave it alone" left the channel with no pull at all until a
			// human noticed. Measured on the live site: one ZLM restart with an
			// unreadable config left every channel reporting
			// source_observation_failed indefinitely while nothing was pulled.
			//
			// The gap counter has passed both the grace and reconnect windows by
			// the time this runs, so rebuilding is the safe default: a proxy that
			// was merely slow has already had its full backoff budget.
			registered, presenceErr := s.Media.ProxyRegistered(ctx, zlm.ProxyRef{Key: ss.Key, OpaqueKey: ss.Key.VHost + "/" + ss.Key.App + "/" + ss.Key.Stream})
			if presenceErr == nil && registered && gaps < streamReconnectWaitSamples {
				return s.observation(ctx, e, ss, "main", "unknown", "stream_present_pending", nil)
			}
			if presenceErr != nil {
				// Logged rather than swallowed: the rebuild below is still the
				// right action, but the operator should be able to see that the
				// media server could not be asked about the proxy.
				slog.Warn("media proxy presence unavailable; rebuilding the session",
					"channel", e.ChannelID, "gap_samples", gaps, "error", presenceErr)
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
	// Complete durable stop intents even if StopRecord succeeded before the
	// previous owner lost its DB/context. Never adopt a stopping run as recording.
	var stopping bool
	if err := s.DB.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM recording_runs WHERE stream_session_id=$1 AND state='stopping')", ss.ID).Scan(&stopping); err != nil {
		return err
	}
	if stopping {
		if err := s.stopRecorder(ctx, e, ss); err != nil {
			return err
		}
		actual.Recording = false
	}
	// REC-09/10: the event buffer exists only where ordinary recording does not.
	// When a channel is inside a continuous, scheduled or manual window its
	// footage is already being written, so an event links to those segments and a
	// second recorder would duplicate every byte. When the switch is off, no
	// buffer runs at all — that is the whole promise of the switch.
	//
	// This is evaluated before the recorder branch because the two are mutually
	// exclusive: a channel that just left continuous recording must stop its
	// buffer's absence matter, and one that is buffering must stop as soon as
	// ordinary recording starts.
	buffering := false
	if desired.Mode == "none" {
		if desired.EventRecording {
			started, bufferErr := s.EnsureBuffer(ctx, e, *desired.Revision)
			if bufferErr != nil {
				// The buffer is an enhancement, never a reason to fail the tick:
				// a channel with no cache, a full tmpfs or a media server that
				// refused the call still records normally when its policy says so.
				slog.Warn("event buffer unavailable", "channel", e.ChannelID, "reason", bufferUnavailableCode(bufferErr), "error", bufferErr)
			}
			buffering = started
		}
		// The buffer is stopped below, in the same place the recording decision is
		// made, so an off switch and a mode change share one path.
	}
	if !buffering {
		if err := s.StopBuffer(ctx, e.ChannelID); err != nil {
			slog.Warn("event buffer stop failed", "channel", e.ChannelID, "error", err)
		}
	}
	if desired.Mode == "none" {
		if actual.Recording {
			return s.stopRecorder(ctx, e, ss)
		}
		return s.observation(ctx, e, ss, "recording", "disabled", "recording_disabled", nil)
	}
	// Every reason to record lands on the same path below: capacity, pool health
	// and the recorder start/stop are identical whether the demand came from the
	// policy, a schedule or an operator's manual window. Only the *decision*
	// differs, and that lives in unionMode.
	if desired.Mode != "continuous" && desired.Mode != "planned" && desired.Mode != "manual" {
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
			// A recorder that keeps running without landing a segment is the
			// silent failure mode: the upstream still reports it as recording
			// while nothing reaches the disk. Escalating to a stop is scoped to
			// sites that declined admission-time write proof, because that is
			// who asked for it. The accepted contract for a proof-requiring
			// site is to report the gap and leave the recorder alone: a
			// database outage spools completions, so stopping there would
			// destroy healthy recording.
			proof, err := storage.RequireWriteProof(ctx, s.DB.Pool, s.siteID)
			if err != nil {
				return err
			}
			if !proof {
				stalled, err := s.recordingOutputStalled(ctx, ss)
				if err != nil {
					return err
				}
				if stalled {
					return s.pauseRuntimeRecording(ctx, e, ss, "recording_output_stalled")
				}
			}
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
	// Missing write evidence is a blocked start, not an unexplained failure: it
	// has a named cause and a repair path (the idle-pool probe refreshes it), so
	// it belongs in the observation. Returning it as a bare error left the
	// recorder silent — the obligation said "recording is owed", the UI said
	// "unknown", and only a warning with no detail explained why.
	if errors.Is(err, storage.ErrMediaProof) {
		return s.observation(ctx, e, ss, "recording", "unavailable", "pool_evidence_stale", nil)
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
	if err == nil && snapshot.ObservedAt.Sub(previous) < 10*time.Second && videoFrames(snapshot) <= frames {
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

// unionMode is the REC-04 rule in one place.
//
// Recording happens when *any* source of demand says so. 'none' is the absence
// of every reason, not a mode that cancels the others: an operator who presses
// record on a channel whose policy is none must get a recording, and a policy
// change made while a manual window runs must not end that recording.
//
// The return value is a *decision*, not an echo of the stored mode. A planned
// channel outside its windows therefore returns "none": echoing "planned" back
// would send it down the recorder-start path and record around the clock, which
// is the opposite of what a schedule means. This is why the function cannot be
// replaced by reading recording_policies.mode in the caller.
func unionMode(policyMode string, manualActive, scheduleActive bool) string {
	if policyMode == "continuous" {
		return "continuous"
	}
	if policyMode == "planned" {
		if scheduleActive {
			return "planned"
		}
		// Scheduled but closed: nothing is owed by the policy right now. Fall
		// through as if the mode were 'none' so a manual window can still win.
		policyMode = "none"
	} else if policyMode != "none" {
		// An unknown stored value records nothing; do not treat it as "some
		// reason" just because it is not the string 'none'.
		policyMode = "none"
	}
	if manualActive {
		return "manual"
	}
	return policyMode
}
