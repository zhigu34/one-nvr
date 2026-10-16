package reliability

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Obligations: the recording-completeness denominator (PRD REL-03).
//
// The question this answers is "how much recording was owed", which is a
// different question from the one intervals answer ("was the stream up"). The
// two deliberately disagree in one direction: while the recorder is down, the
// source is broken or the pool is unwritable, the stream intervals say
// *unavailable* and the obligation says *still owed*. That is not a
// contradiction — it is the point. PRD REL-03 requires failures to stay in the
// denominator, because a denominator that shrinks whenever something breaks
// makes availability rise exactly when the site is doing worse.

// Obligation is one stretch of "recording was required, or explicitly not", as
// derived from the facts in effect during it.
type Obligation struct {
	ChannelID id.ID
	// SourceRevisionID is the source in effect for this stretch. REL-03 wants
	// the obligation to name source_id and source version so a later camera
	// swap does not retroactively change what was owed.
	SourceRevisionID *id.ID
	SessionID        *id.ID
	// PolicyVersion is the recording_policy_versions row this was derived from.
	PolicyVersion int64
	// Source is where the requirement comes from: continuous, or the reserved
	// planned/manual vocabulary.
	Source string
	// Exclusion empty means recording WAS required. A non-empty value names why
	// it was not, in the same vocabulary intervals use.
	Exclusion string
	// Evidence records what the derivation saw (switch id, revision, the facts
	// behind an exclusion), so the verdict can be audited rather than trusted.
	Evidence map[string]any
	// ObservedAt is when the derivation ran. It becomes the new obligation's
	// start, so the boundary carries the reconciler's cadence as its honest
	// resolution — the exact policy change instant lives in
	// recording_policy_versions.effective_at for a reader that needs it.
	ObservedAt time.Time
}

// ObligationView is one obligation as the API reports it.
type ObligationView struct {
	ID               id.ID          `json:"id"`
	SourceRevisionID *id.ID         `json:"source_revision_id,omitempty"`
	PolicyVersion    int64          `json:"policy_version"`
	Source           string         `json:"source"`
	Exclusion        string         `json:"exclusion,omitempty"`
	Started          time.Time      `json:"started_at"`
	Ended            *time.Time     `json:"ended_at"`
	Evidence         map[string]any `json:"evidence,omitempty"`
	DurationMS       int64          `json:"duration_ms"`
}

// FoldObligation records the current verdict for one channel, extending the
// open obligation when nothing changed and closing-and-opening when something
// did.
//
// Modelled on the interval fold, including the partial-unique-index upsert, so
// two workers reconciling the same channel cannot open two obligations for one
// state. The comparison key is (source, exclusion): a policy version bump that
// does not change the mode is not a boundary, because the obligation it
// describes is identical and splitting it would only fragment the timeline.
func (s *Service) FoldObligation(ctx context.Context, in Obligation) error {
	if s.DB == nil || in.ChannelID == "" || in.Source == "" || in.ObservedAt.IsZero() {
		return nil
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		return s.FoldObligationTx(ctx, tx, in)
	})
}

// FoldObligationTx folds inside a transaction the caller owns, for callers that
// must make the obligation atomic with the fact that changed it.
func (s *Service) FoldObligationTx(ctx context.Context, tx pgx.Tx, in Obligation) error {
	if in.ChannelID == "" || in.Source == "" || in.ObservedAt.IsZero() {
		return nil
	}
	observed := in.ObservedAt.UTC()

	var current struct {
		id            id.ID
		source        string
		exclusion     *string
		policyVersion int64
		started       time.Time
		lastObserved  time.Time
	}
	err := tx.QueryRow(ctx, `SELECT id,source,exclusion,policy_version,started_at,last_observed_at
		FROM recording_obligations
		WHERE channel_id=$1 AND ended_at IS NULL FOR UPDATE`, in.ChannelID).
		Scan(&current.id, &current.source, &current.exclusion, &current.policyVersion, &current.started, &current.lastObserved)
	switch {
	case err == nil:
		open := ""
		if current.exclusion != nil {
			open = *current.exclusion
		}
		if current.source == in.Source && open == in.Exclusion {
			// Same verdict: the obligation continues. Refresh the policy version
			// and the last-observed instant so a reader can tell the reconciler
			// was still running, without splitting the stretch.
			evidence, err := marshalEvidence(in.Evidence)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE recording_obligations
				SET policy_version=$2,source_revision_id=$3,session_id=$4,evidence=$5,last_observed_at=$6,updated_at=clock_timestamp()
				WHERE id=$1`, current.id, in.PolicyVersion, in.SourceRevisionID, in.SessionID, evidence, observed)
			return err
		}
		// The verdict changed. Unlike the interval fold, this closes the old
		// obligation at *this* observation's instant rather than at the last one
		// that supported it, and the difference is deliberate.
		//
		// An interval answers "how long was the state X", so closing late would
		// silently shorten every outage by a sample period. An obligation is a
		// denominator: its stretches have to tile the timeline with no hole,
		// because any instant belonging to neither an owed stretch nor an
		// exclusion is time the completeness figure cannot account for at all.
		// Closing at the previous observation and opening the new stretch at
		// this one would leave exactly that hole, one reconciler tick wide.
		//
		// So the uncertain stretch between the two observations is attributed to
		// the verdict that was in force when it began. The exact instant of the
		// change is still recoverable from
		// recording_policy_versions.effective_at, which this row names.
		closed := observed
		if closed.Before(current.started) {
			closed = current.started
		}
		if _, err = tx.Exec(ctx, `UPDATE recording_obligations SET ended_at=$2,updated_at=clock_timestamp() WHERE id=$1`, current.id, closed); err != nil {
			return err
		}
	case errors.Is(err, pgx.ErrNoRows):
		// Nothing open: this is the first verdict recorded for this channel.
	default:
		return err
	}

	evidence, err := marshalEvidence(in.Evidence)
	if err != nil {
		return err
	}
	obligationID, err := id.New()
	if err != nil {
		return err
	}
	var exclusion any
	if in.Exclusion != "" {
		exclusion = in.Exclusion
	}
	_, err = tx.Exec(ctx, `INSERT INTO recording_obligations
			(id,channel_id,source_revision_id,session_id,policy_version,source,exclusion,started_at,last_observed_at,evidence)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$9)`,
		obligationID, in.ChannelID, in.SourceRevisionID, in.SessionID, in.PolicyVersion, in.Source, exclusion, observed, evidence)
	return err
}

func marshalEvidence(evidence map[string]any) ([]byte, error) {
	if len(evidence) == 0 {
		return []byte(`{}`), nil
	}
	return json.Marshal(evidence)
}

// CurrentObligation returns the open obligation for a channel, if any. The
// reconciler uses it to avoid rewriting an unchanged verdict, and tests use it
// to assert what the fold decided.
func (s *Service) CurrentObligation(ctx context.Context, channelID id.ID) (*ObligationView, error) {
	if s.DB == nil {
		return nil, nil
	}
	var out ObligationView
	var exclusion *string
	var evidence []byte
	err := s.DB.Pool.QueryRow(ctx, `SELECT id,source_revision_id,policy_version,source,exclusion,started_at,evidence
		FROM recording_obligations WHERE channel_id=$1 AND ended_at IS NULL`, channelID).
		Scan(&out.ID, &out.SourceRevisionID, &out.PolicyVersion, &out.Source, &exclusion, &out.Started, &evidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if exclusion != nil {
		out.Exclusion = *exclusion
	}
	if len(evidence) > 0 {
		_ = json.Unmarshal(evidence, &out.Evidence)
	}
	return &out, nil
}

// ListObligations returns one channel's obligation timeline inside a window.
//
// An obligation that overlaps the window at all is returned even when it
// started before it, because the time owed inside the window depends on it.
func (s *Service) ListObligations(ctx context.Context, channelID id.ID, from, to time.Time) ([]ObligationView, error) {
	out := []ObligationView{}
	if s.DB == nil || channelID == "" || !to.After(from) {
		return out, nil
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,source_revision_id,policy_version,source,exclusion,started_at,ended_at,evidence,
			GREATEST(0,EXTRACT(EPOCH FROM (COALESCE(ended_at,$3::timestamptz) - started_at))*1000)::bigint
		FROM recording_obligations
		WHERE channel_id=$1 AND started_at < $3::timestamptz AND COALESCE(ended_at,$3::timestamptz) > $2::timestamptz
		ORDER BY started_at,id`, channelID, from.UTC(), to.UTC())
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item ObligationView
		var exclusion *string
		var evidence []byte
		if err := rows.Scan(&item.ID, &item.SourceRevisionID, &item.PolicyVersion, &item.Source, &exclusion, &item.Started, &item.Ended, &evidence, &item.DurationMS); err != nil {
			return out, err
		}
		if exclusion != nil {
			item.Exclusion = *exclusion
		}
		if len(evidence) > 0 {
			_ = json.Unmarshal(evidence, &item.Evidence)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
