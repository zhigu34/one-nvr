package recording

import (
	"context"
	"encoding/json"
	"errors"
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
	last, started, err := s.completionWindow(ctx, ss)
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

func (s *Service) completionWindow(ctx context.Context, ss physicalSession) (last, started *time.Time, err error) {
	err = s.DB.Pool.QueryRow(ctx, "SELECT last_completion_at,started_at FROM recording_runs WHERE stream_session_id=$1 AND state='recording' ORDER BY created_at DESC LIMIT 1", ss.ID).Scan(&last, &started)
	return last, started, err
}

// RecordingOutputGrace is how long a recorder may keep running without landing
// a completed segment before it is stopped and reported. Segments complete
// about once a minute, so three minutes tolerates one delayed or retried
// segment while still catching a recorder that is producing nothing at all —
// the failure an operator cannot see, because ZLM still reports it as running.
const RecordingOutputGrace = 3 * time.Minute

// outputStalledAt decides staleness from the two timestamps a recording run
// carries. A nil pair (no run row at all) counts as stalled: a recorder that
// cannot even name its run is not producing output.
func outputStalledAt(now time.Time, last, started *time.Time) bool {
	newest := time.Time{}
	for _, candidate := range []*time.Time{last, started} {
		if candidate != nil && candidate.After(newest) {
			newest = *candidate
		}
	}
	if newest.IsZero() {
		return true
	}
	return now.Sub(newest) > RecordingOutputGrace
}

// recordingOutputStalled reports whether the newest real output of this
// recorder (a completed segment, or its own start for a brand new run) is older
// than the grace period.
func (s *Service) recordingOutputStalled(ctx context.Context, ss physicalSession) (bool, error) {
	last, started, err := s.completionWindow(ctx, ss)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		return false, err
	}
	return outputStalledAt(time.Now().UTC(), last, started), nil
}
