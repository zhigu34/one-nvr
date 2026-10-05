package recording

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
)

func (s *Service) observation(ctx context.Context, e *channel.Execution, ss physicalSession, kind, state, reason string, evidence any) error {
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		observation, err := id.New()
		if err != nil {
			return err
		}
		var revision, session *id.ID
		if ss.RevisionID != "" {
			revision = &ss.RevisionID
		}
		if ss.ID != "" {
			session = &ss.ID
		}
		_, err = tx.Exec(ctx, `INSERT INTO source_observations(id,channel_id,source_revision_id,session_id,kind,state,reason_code,evidence,observed_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp(),clock_timestamp()+interval '30 seconds')`, observation, e.ChannelID, revision, session, kind, state, reason, raw)
		return err
	})
}
func (s *Service) runtimeGap(ctx context.Context, e *channel.Execution, ss physicalSession, reason string, observed bool) error {
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		gap, err := id.New()
		if err != nil {
			return err
		}
		evidence := "unknown"
		if observed {
			evidence = "observed"
		}
		_, err = tx.Exec(ctx, `INSERT INTO recording_gaps(id,channel_id,source_revision_id,run_id,start_at,reason_code,evidence) SELECT $1,$2,$3,r.id,CASE WHEN $5='observed' THEN clock_timestamp() ELSE coalesce(r.last_completion_at,r.started_at,r.created_at) END,$4,$5 FROM recording_runs r WHERE r.channel_id=$2 AND r.source_revision_id=$3 AND r.purpose='continuous' AND NOT EXISTS(SELECT 1 FROM recording_gaps WHERE channel_id=$2 AND end_at IS NULL) ORDER BY r.created_at DESC LIMIT 1`, gap, e.ChannelID, ss.RevisionID, reason, evidence)
		return err
	})
}
func (s *Service) closeRuntimeGaps(ctx context.Context, e *channel.Execution) error {
	return e.WithinTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE recording_gaps SET end_at=clock_timestamp() WHERE channel_id=$1 AND end_at IS NULL", e.ChannelID)
		return err
	})
}
func (s *Service) completionStatus(ctx context.Context, ss physicalSession) (string, string, error) {
	var last, started *time.Time
	err := s.DB.Pool.QueryRow(ctx, "SELECT last_completion_at,started_at FROM recording_runs WHERE stream_session_id=$1 AND state='recording' ORDER BY created_at DESC LIMIT 1", ss.ID).Scan(&last, &started)
	if err != nil {
		return "unknown", "recorder_observation_missing", err
	}
	now := time.Now().UTC()
	if last != nil && last.After(now.Add(-90*time.Second)) {
		return "healthy", "recent_completed_segment", nil
	}
	if started != nil && started.After(now.Add(-90*time.Second)) {
		return "unknown", "first_completion_pending", nil
	}
	return "unknown", "completion_stale", nil
}
