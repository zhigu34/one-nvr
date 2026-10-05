package recording

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
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
		if err := s.stopPhysical(ctx, e, ss); err != nil {
			return err
		}
		return s.observation(ctx, e, ss, "sub", "unavailable", "sub_source_unavailable", nil)
	}
	if err != nil {
		return s.observation(ctx, e, ss, "sub", "unknown", "source_observation_failed", nil)
	}
	state, reason, err := s.runtimeFrameStatus(ctx, e, ss, snapshot, "sub")
	if err != nil {
		return err
	}
	if state == "unavailable" {
		if err := s.stopPhysical(ctx, e, ss); err != nil {
			return err
		}
	}
	return s.observation(ctx, e, ss, "sub", state, reason, nil)
}
