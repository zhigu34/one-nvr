package recording

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"log/slog"
	"time"
)

// Persist a replacement physical identity before connecting. Starting intent is
// reusable after interruption; closed generations and their run paths stay immutable.
func (s *Service) runtimeSession(ctx context.Context, e *channel.Execution, revision id.ID, kind string) (physicalSession, error) {
	var out physicalSession
	err := e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanSession(tx.QueryRow(ctx, "SELECT "+physicalColumns+" FROM stream_sessions WHERE channel_id=$1 AND source_revision_id=$2 AND purpose=$3 AND state='starting' ORDER BY generation DESC LIMIT 1 FOR UPDATE", e.ChannelID, revision, kind))
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
		if err := tx.QueryRow(ctx, "UPDATE channels SET source_generation=greatest(source_generation,(SELECT coalesce(max(generation),0) FROM stream_sessions WHERE channel_id=$1))+1 WHERE id=$1 RETURNING source_generation", e.ChannelID).Scan(&generation); err != nil {
			return err
		}
		key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(session)}
		_, err = tx.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,vhost,app,stream,purpose,proxy_key) VALUES($1,$2,$3,$4,$5,$6,$7,$9,$8)`, session, e.ChannelID, revision, generation, key.VHost, key.App, key.Stream, key.VHost+"/"+key.App+"/"+key.Stream, kind)
		out = physicalSession{ID: session, ChannelID: e.ChannelID, RevisionID: revision, Key: key, State: "starting"}
		return err
	})
	return out, err
}
func (s *Service) recoverRuntimeSource(ctx context.Context, e *channel.Execution, revision id.ID) error {
	ss, err := s.runtimeSession(ctx, e, revision, "main")
	if err != nil {
		return err
	}
	window, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if connectErr := s.connectSwitchStream(window, e, ss, "main"); connectErr != nil {
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := s.stopPhysical(ctx, e, ss); err != nil {
			return err
		}
		return s.observation(ctx, e, ss, "main", "unavailable", recoveryFailureReason(connectErr), nil)
	}
	if _, err := s.sampleBitrate(ctx, e, ss, "", true); err != nil {
		return err
	}
	return s.observation(ctx, e, ss, "main", "unknown", "frame_progress_pending", nil)
}

func (s *Service) reconcileRuntimeSub(ctx context.Context, e *channel.Execution, revision id.ID) error {
	var configured bool
	if err := s.DB.Pool.QueryRow(ctx, "SELECT sub_path<>'' FROM source_revisions WHERE id=$1", revision).Scan(&configured); err != nil {
		return err
	}
	if !configured {
		return s.observation(ctx, e, physicalSession{RevisionID: revision}, "sub", "not_configured", "sub_not_configured", nil)
	}
	ss, err := scanSession(s.DB.Pool.QueryRow(ctx, "SELECT "+physicalColumns+" FROM stream_sessions WHERE channel_id=$1 AND source_revision_id=$2 AND purpose='sub' AND state='active' ORDER BY generation DESC LIMIT 1", e.ChannelID, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		ss, err = s.runtimeSession(ctx, e, revision, "sub")
		if err != nil {
			return err
		}
		window, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = s.connectSwitchStream(window, e, ss, "sub")
		cancel()
		if err == nil {
			_, err = s.sampleBitrate(ctx, e, ss, "", true)
			if err != nil {
				return err
			}
			return s.observation(ctx, e, ss, "sub", "unknown", "frame_progress_pending", nil)
		}
		logSubFailure(err)
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := s.stopPhysical(ctx, e, ss); err != nil {
			return err
		}
		return s.observation(ctx, e, ss, "sub", "unavailable", subFailureReason(err), nil)
	}
	if err != nil {
		return err
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	snapshot, err := s.Media.Inspect(ctx, ss.Key)
	if errors.Is(err, zlm.ErrStreamAbsent) {
		// A single missing sample is not evidence the stream is gone. ZLM's
		// registry reports online=false for a moment while a large keyframe is
		// ingested, and VBR encoders legitimately pause between frames; the
		// sampler runs every ten seconds, so one miss is well inside normal.
		//
		// The previous implementation tore the session down here, which is what
		// turned a momentary gap into a rebuild loop: stopPhysical removed the
		// proxy and marked the session closed, the next tick found no active
		// session, created a fresh one and reconnected, and the newly pulled
		// stream had no frames yet — so the cycle repeated every forty seconds,
		// interrupting the picture each time. The grace count comes from the
		// open interval the fold maintains, so it survives a worker restart.
		gaps, gapErr := s.consecutiveGapSamples(ctx, e.ChannelID, "sub", "stream_present_pending")
		if gapErr != nil {
			return gapErr
		}
		if gaps < streamGapGraceSamples {
			return s.observation(ctx, e, ss, "sub", "unknown", "stream_present_pending", nil)
		}
		// Persistent absence: distinguish "the pull is reconnecting" from "the
		// media server dropped the pull". A registered proxy is still ours and
		// will come back on its own; tearing the session down there forced a
		// fresh AddProxy and a fresh handshake for an interruption ZLM was
		// already handling. Only a proxy the media server no longer holds is
		// evidence that the session is finished.
		//
		// A failed query is not evidence either way. Treating it as "leave the
		// session alone" kept a dead sub stream in place until someone noticed:
		// the media server can be unreachable exactly when the pull is gone.
		// This runs on every reconcile, so tearing down here is self-healing —
		// the next pass builds a fresh session.
		registered, presenceErr := s.Media.ProxyRegistered(ctx, zlm.ProxyRef{Key: ss.Key, OpaqueKey: ss.Key.VHost + "/" + ss.Key.App + "/" + ss.Key.Stream})
		if presenceErr == nil && registered && gaps < streamReconnectWaitSamples {
			return s.observation(ctx, e, ss, "sub", "unknown", "stream_present_pending", nil)
		}
		if presenceErr != nil {
			slog.Warn("sub proxy presence unavailable; rebuilding the session",
				"channel", e.ChannelID, "gap_samples", gaps, "error", presenceErr)
		}
		// Either the media server no longer owns the pull, or it has kept the
		// registration far past its own retry ladder without producing frames.
		// Both mean this session is finished: tear down and let the next
		// reconcile build a fresh one.
		if err := s.stopPhysical(ctx, e, ss); err != nil {
			return err
		}
		if err := s.observation(ctx, e, ss, "sub", "unavailable", "sub_source_unavailable", nil); err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return s.observation(ctx, e, ss, "sub", "unknown", "source_observation_failed", nil)
	}
	state, reason, err := s.runtimeFrameStatus(ctx, e, ss, snapshot, "sub")
	if err != nil {
		return err
	}
	// A stalled frame counter restarts the pull without discarding the session:
	// runtimeFrameStatus already reconnects when its sample window has gone
	// stale, and tearing the session down here only guaranteed that the next
	// tick would build another one.
	return s.observation(ctx, e, ss, "sub", state, reason, nil)
}

// streamGapGraceSamples is how many consecutive samples may report the stream
// absent before the pull is restarted.
//
// Three samples at the ten-second cadence is half a minute, which is longer
// than any keyframe ingestion pause and short enough that a genuinely dead
// source is still noticed quickly.
const streamGapGraceSamples = 3

// streamReconnectWaitSamples bounds how long a still-registered proxy may stay
// silent before the session is rebuilt anyway.
//
// A registered proxy usually means the media server is re-pulling the source,
// and waiting lets it recover in place. But "registered" is not a promise of
// recovery: a proxy can keep its registration while the pull never succeeds.
// Twelve samples is two minutes — longer than the media server's whole retry
// ladder (~32s of backoff plus handshakes) with room to spare — after which a
// session that still has no frames is treated as finished so the worker does
// not observe a dead channel forever.
const streamReconnectWaitSamples = 12

// consecutiveGapSamples reports how many samples in a row have observed the
// same gap, read from the open interval the reliability fold maintains.
//
// Reading it from the fold rather than keeping a counter in memory means the
// count survives a worker restart, and means there is exactly one place that
// decides what "consecutive" means.
func (s *Service) consecutiveGapSamples(ctx context.Context, channelID id.ID, kind, reason string) (int, error) {
	if s.Reliability == nil {
		// Without history there is nothing to count; behave as if this is the
		// first gap, which is the safe direction — it only delays a reconnect.
		return 0, nil
	}
	var count int
	err := s.DB.Pool.QueryRow(ctx, `SELECT observed_count FROM reliability_intervals
		WHERE channel_id=$1 AND kind=$2 AND ended_at IS NULL AND reason_code=$3`,
		channelID, kind, reason).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return count, err
}
