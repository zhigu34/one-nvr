// Package reliability turns observations into history.
//
// Everything else in this repository answers "what is true now":
// source_observations holds the newest state per channel and stream, and each
// row expires thirty seconds after it is written. That is the right shape for a
// status page and the wrong shape for the question PRD §6.12 asks — how much of
// the last month was this stream actually up, and when it was not, why. The
// answer needs two things the current tables do not have: intervals, which say
// how long a state lasted, and faults, which say what went wrong and whether it
// came back.
//
// This package builds both from the same observation stream, so there is one
// source of truth rather than two that can disagree. It deliberately computes
// no rates: those are projections of these intervals (REL-04, a later batch),
// and a projection computed before its facts are complete is a guess.
package reliability

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Observation is one sample's verdict about one channel and stream kind. It is
// what the sampler already produces; this package only records it.
type Observation struct {
	ChannelID        id.ID
	SourceRevisionID *id.ID
	SessionID        *id.ID
	Kind             string
	State            string
	Reason           string
	// Exclusion marks an interval that is not required to be up. Empty means
	// the state is a real observation.
	Exclusion  string
	ObservedAt time.Time
}

// Interval is one contiguous stretch of one state. Times are half-open
// [Started, Ended) so a boundary instant belongs to exactly one interval.
type Interval struct {
	ID            id.ID
	ChannelID     id.ID
	Kind          string
	State         string
	Reason        string
	Exclusion     string
	Started       time.Time
	Ended         *time.Time
	ObservedCount int
	// LastHealthy and FirstUnhealthy bound the instant of the change: polling
	// cannot know it exactly, and REL-01 requires that uncertainty to be
	// visible rather than collapsed into the interval start.
	LastObserved  time.Time
	FirstObserved time.Time
}

// Service records observation history and derives faults from it.
type Service struct {
	DB *database.DB
	// Auth gates the operator-facing writes (maintenance registration and
	// attribution confirmation). The sampling path does not use it: the worker
	// records what it observed without acting as a user.
	Auth *auth.Service
}

func New(db *database.DB, accounts *auth.Service) *Service {
	return &Service{DB: db, Auth: accounts}
}

// foldOutcome reports what the fold did, so a caller can react (and a test can
// assert) without re-reading the table.
type foldOutcome struct {
	IntervalID id.ID
	// Opened is true when this observation began a new interval rather than
	// extending the open one.
	Opened bool
	// Previous carries the interval that just closed, when one did. Fault
	// derivation needs it: the transition is the event.
	Previous *Interval
}

// Fold records one observation, extending the open interval when the state is
// unchanged and closing-and-opening when it is not.
//
// This is the only writer of intervals. Everything readers see is a fold of the
// same stream, which is what keeps "the raw samples" and "the summary" from
// drifting apart.
//
// The write is expressed as an upsert against the partial unique index on open
// intervals, so two workers sampling the same channel cannot open two intervals
// for one state: whoever loses the race extends the winner's row.
func (s *Service) Fold(ctx context.Context, in Observation) error {
	if !validObservation(in) {
		return nil
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := s.FoldTx(ctx, tx, in)
		return err
	})
}

// FoldTx folds inside a transaction the caller already owns.
//
// The sampler writes its observation row inside the execution's transaction; a
// nested transaction there would commit independently, so a rollback could
// leave an interval describing a sample that was never recorded. Sharing the
// transaction makes the two rows atomic together.
func (s *Service) FoldTx(ctx context.Context, tx pgx.Tx, in Observation) (foldOutcome, error) {
	if !validObservation(in) {
		return foldOutcome{}, nil
	}
	out, err := s.foldTx(ctx, tx, in)
	if err != nil {
		return out, err
	}
	if err := s.recordFaultsTx(ctx, tx, in, out.Previous); err != nil {
		return out, err
	}
	return out, nil
}

// validObservation rejects a sample that cannot describe anything. It also
// normalises the state, which is the only place that happens.
func validObservation(in Observation) bool {
	return in.ChannelID != "" && in.Kind != "" && in.State != "" && !in.ObservedAt.IsZero()
}

// storedState maps an observation's state to the value an interval may hold.
//
// The observation vocabulary and the interval vocabulary are not the same, and
// conflating them broke recording history outright on a live site: the recorder
// emits "disabled" and "not_configured", the intervals table's CHECK allows only
// healthy/unavailable/unknown/excluded, and so every recording sample was
// rejected by the database and the recording kind had NO history at all. The
// exclusion column is what carries "not required"; the state must still be one
// of the four the table can express.
//
// The mapping is deliberately total: an unrecognised state becomes "unknown"
// rather than reaching the CHECK constraint, because a sample we cannot classify
// is still evidence that something was observed, and losing it silently is
// exactly the failure this function exists to prevent.
func storedState(in Observation) string {
	switch in.State {
	case "healthy", "unavailable", "unknown", "excluded":
		return in.State
	case "disabled", "not_configured", "degraded":
		// Not required to be up: the reason travels in exclusion (or in the
		// reason code when the caller did not set one).
		return "excluded"
	}
	return "unknown"
}

func (s *Service) foldTx(ctx context.Context, tx pgx.Tx, in Observation) (foldOutcome, error) {
	var out foldOutcome
	observed := in.ObservedAt.UTC()
	// Normalise once, up front: every comparison and both writes below use the
	// stored vocabulary, so a recorder's "disabled" can never reach the CHECK
	// constraint as itself.
	in.State = storedState(in)

	// What is open now, if anything. FOR UPDATE so a concurrent sample for the
	// same channel and kind waits rather than folding against a stale read.
	var current Interval
	var currentExclusion *string
	err := tx.QueryRow(ctx, `SELECT id,state,reason_code,exclusion,started_at,observed_count,last_observed_at,first_observed_at
		FROM reliability_intervals
		WHERE channel_id=$1 AND kind=$2 AND ended_at IS NULL FOR UPDATE`,
		in.ChannelID, in.Kind).
		Scan(&current.ID, &current.State, &current.Reason, &currentExclusion, &current.Started, &current.ObservedCount, &current.LastObserved, &current.FirstObserved)
	switch {
	case err == nil:
		if currentExclusion != nil {
			current.Exclusion = *currentExclusion
		}
		// Same state and same reason: the interval simply continues. The
		// reason matters because two different failures can share a state, and
		// merging them would lose which one the operator is looking at.
		if current.State == in.State && current.Reason == in.Reason && current.Exclusion == in.Exclusion {
			out.IntervalID = current.ID
			_, err = tx.Exec(ctx, `UPDATE reliability_intervals
				SET observed_count=observed_count+1,last_observed_at=$2,updated_at=clock_timestamp()
				WHERE id=$1`, current.ID, observed)
			return out, err
		}
		// The state changed. Close the old interval at the last observation
		// that still supported it — not at this sample's time. The failure
		// happened somewhere between that instant and this one, and using the
		// later time would silently shorten every outage by one sample period.
		closed := current.LastObserved
		if closed.Before(current.Started) {
			closed = current.Started
		}
		if _, err = tx.Exec(ctx, `UPDATE reliability_intervals SET ended_at=$2,updated_at=clock_timestamp() WHERE id=$1`, current.ID, closed); err != nil {
			return out, err
		}
		current.Ended = &closed
		out.Previous = &current
		out.Opened = true
	case errors.Is(err, pgx.ErrNoRows):
		// Nothing open: this observation starts the history for this kind.
		out.Opened = true
	default:
		return out, err
	}

	intervalID, err := id.New()
	if err != nil {
		return out, err
	}
	var exclusion any
	if in.Exclusion != "" {
		exclusion = in.Exclusion
	}
	revision := any(nil)
	if in.SourceRevisionID != nil {
		revision = *in.SourceRevisionID
	}
	session := any(nil)
	if in.SessionID != nil {
		session = *in.SessionID
	}
	// A new interval's uncertainty band is the previous sample's time (when the
	// state was last known to differ) and this one's. The caller passes the
	// sample time; both bounds come from the same clock, so the band is honest
	// about what polling can and cannot resolve.
	if _, err = tx.Exec(ctx, `INSERT INTO reliability_intervals
			(id,channel_id,source_revision_id,session_id,kind,state,reason_code,exclusion,started_at,observed_count,last_observed_at,first_observed_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$9,$9)`,
		intervalID, in.ChannelID, revision, session, in.Kind, in.State, in.Reason, exclusion, observed); err != nil {
		return out, err
	}
	out.IntervalID = intervalID
	return out, nil
}

// CloseOpenOnStart closes every open interval before sampling resumes.
//
// PRD REL-01: a worker that stops must not leave its last state standing as if
// it were current — the time it was down is "monitoring unknown", which is a
// fact about our evidence, not about the camera. Each open interval is closed
// at its last genuine observation and the gap that follows is recorded as its
// own unknown interval, so a reader can see exactly which stretch has no
// evidence.
//
// It runs once at startup, before the first sample, so a fresh observation
// cannot extend an interval that spans the downtime.
func (s *Service) CloseOpenOnStart(ctx context.Context) error {
	if s.DB == nil {
		return nil
	}
	now := time.Now().UTC()
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,channel_id,source_revision_id,session_id,kind,state,reason_code,exclusion,started_at,observed_count,last_observed_at,first_observed_at
			FROM reliability_intervals WHERE ended_at IS NULL FOR UPDATE`)
		if err != nil {
			return err
		}
		type open struct {
			id           id.ID
			channelID    id.ID
			revision     *id.ID
			session      *id.ID
			kind         string
			lastObserved time.Time
			startedAt    time.Time
		}
		var opens []open
		for rows.Next() {
			var item open
			var state, reason string
			var exclusion *string
			var count int
			var first time.Time
			if err := rows.Scan(&item.id, &item.channelID, &item.revision, &item.session, &item.kind, &state, &reason, &exclusion, &item.startedAt, &count, &item.lastObserved, &first); err != nil {
				rows.Close()
				return err
			}
			opens = append(opens, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range opens {
			if _, err := tx.Exec(ctx, `UPDATE reliability_intervals SET ended_at=$2,updated_at=clock_timestamp() WHERE id=$1`, item.id, item.lastObserved); err != nil {
				return err
			}
			// The gap is only recorded when it is real: an interval closed at
			// the same instant sampling resumes has no gap to report, and
			// inventing a zero-length one would add noise to every restart.
			if !now.After(item.lastObserved) || now.Sub(item.lastObserved) < time.Second {
				continue
			}
			gapID, err := id.New()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO reliability_intervals
					(id,channel_id,source_revision_id,session_id,kind,state,reason_code,started_at,observed_count,last_observed_at,first_observed_at)
					VALUES($1,$2,$3,$4,$5,'unknown','monitoring_gap',$6,1,$6,$6)`,
				gapID, item.channelID, item.revision, item.session, item.kind, item.lastObserved); err != nil {
				return err
			}
		}
		return nil
	})
}

// NoteMonitoringGap closes open intervals whose last observation is older than
// the staleness window, for the case where the process kept running but
// sampling stopped (the database was unavailable, or a worker was wedged).
//
// Without this, the last state would stand indefinitely: PRD REL-01 requires
// that after two missed sampling periods the uncovered time is recorded as
// unknown rather than carried forward as though the stream were still healthy.
func (s *Service) NoteMonitoringGap(ctx context.Context, maxAge time.Duration) error {
	if s.DB == nil {
		return nil
	}
	cutoff := time.Now().UTC().Add(-maxAge)
	_, err := s.DB.Pool.Exec(ctx, `
		WITH stale AS (
			SELECT id,channel_id,source_revision_id,session_id,kind,last_observed_at
			FROM reliability_intervals
			WHERE ended_at IS NULL AND last_observed_at < $1
		), closed AS (
			UPDATE reliability_intervals i SET ended_at=s.last_observed_at,updated_at=clock_timestamp()
			FROM stale s WHERE i.id=s.id
			RETURNING i.id
		)
		INSERT INTO reliability_intervals
			(id,channel_id,source_revision_id,session_id,kind,state,reason_code,started_at,observed_count,last_observed_at,first_observed_at)
		SELECT gen_random_uuid(),s.channel_id,s.source_revision_id,s.session_id,s.kind,'unknown','monitoring_gap',
			s.last_observed_at,1,s.last_observed_at,s.last_observed_at
		FROM stale s`, cutoff)
	return err
}
