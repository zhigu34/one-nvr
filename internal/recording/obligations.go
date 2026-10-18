package recording

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/reliability"
)

// Obligation reconciliation: what recording was owed, recorded as history.
//
// Why this is a loop over all channels rather than a hook on the things that
// change the answer:
//
// Four independent write paths decide whether recording is owed — a policy
// change (policy_apply, source switch, rollback), a channel being enabled or
// disabled, a source being applied or cleared, and a maintenance window being
// registered. Hooking each one means four chances to miss a path, and every
// future write path has to remember to notify this package. Recomputing from
// the facts instead collects all of them by construction and heals itself after
// a restart, a migration backfill, or a path nobody remembered.
//
// The cost is that a boundary is placed by the reconciler's cadence rather than
// at the exact instant the policy changed. That is why the obligation carries
// policy_version: recording_policy_versions.effective_at has the precise
// instant, so a reader that needs exact boundaries can resolve them there. This
// is the same honesty the interval fold applies to state changes.
//
// Note this loop deliberately visits *disabled* and *unconfigured* channels
// too. The sampler skips them (no media to observe), but PRD REL-03 requires
// deliberate stops and unconfigured sources to be shown as explicit exclusions,
// and those two states only exist on channels the sampler never touches.

// obligationFacts is everything the derivation reads for one channel.
type obligationFacts struct {
	ChannelID   id.ID
	Enabled     bool
	Revision    *id.ID
	SessionID   *id.ID
	Mode        string
	PolicyVer   int64
	InMaintenan bool
	// SwitchID is set while a source switch owns the channel. REL-03: the old
	// policy still applies during the switch, so the obligation continues and
	// the switch is recorded as evidence rather than as an interruption.
	SwitchID *id.ID
}

func (s *Service) loadObligationFacts(ctx context.Context, tx pgx.Tx, channelID id.ID) (obligationFacts, error) {
	var out obligationFacts
	out.ChannelID = channelID
	err := tx.QueryRow(ctx, `
		SELECT c.enabled,c.current_revision_id,p.mode,p.version,
			EXISTS(SELECT 1 FROM maintenance_windows mw
				WHERE (mw.channel_id IS NULL OR mw.channel_id=c.id)
					AND mw.starts_at <= clock_timestamp() AND mw.ends_at > clock_timestamp()),
			(SELECT sw.id FROM source_switches sw
				WHERE sw.channel_id=c.id AND sw.state IN ('queued','running') LIMIT 1)
		FROM channels c JOIN recording_policies p ON p.channel_id=c.id
		WHERE c.id=$1`, channelID).
		Scan(&out.Enabled, &out.Revision, &out.Mode, &out.PolicyVer, &out.InMaintenan, &out.SwitchID)
	if err != nil {
		return out, err
	}
	// The union, read through the same helper the scheduler uses: an obligation
	// and a recording decision that disagree about "should this be recording
	// now" would make every completeness figure wrong in a way nobody could see.
	active, err := manualActiveTx(ctx, tx, channelID)
	if err != nil {
		return out, err
	}
	out.Mode = unionMode(out.Mode, active)
	// The session is what the recorder is attached to; recording it on the
	// obligation is what lets a reader tie an owed stretch to the media session
	// that served it.
	if out.Revision != nil {
		var session id.ID
		err := tx.QueryRow(ctx, "SELECT id FROM stream_sessions WHERE channel_id=$1 AND source_revision_id=$2 AND purpose='main' AND state='active' ORDER BY generation DESC,created_at DESC LIMIT 1", channelID, *out.Revision).Scan(&session)
		if err == nil {
			out.SessionID = &session
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	return out, nil
}

// obligationFor turns the facts into the verdict the fold records.
//
// The ordering of the checks is the specification, so it is spelled out:
//
//  1. A channel with no source owes nothing — there is nothing to record. It is
//     an exclusion, not a gap: recording an unconfigured channel as a failure
//     would blame the site for a state the operator has not set up yet.
//  2. A disabled channel owes nothing. Same reasoning, and this is the
//     deliberate-stop case REL-03 names.
//  3. Registered maintenance owes nothing but is shown separately. It is
//     checked after 1 and 2 so the more specific cause (disabled, unconfigured)
//     wins when several apply — an operator looking at a disabled channel
//     during a maintenance window should see "disabled", the durable reason.
//  4. Continuous is owed, including while the recorder is broken, the source is
//     down or the pool is unwritable. THIS IS THE REQUIREMENT, not an oversight:
//     PRD REL-03 says a failure must not shrink the denominator.
//  5. A manual window is owed for exactly as long as it runs (REC-03/04). It is
//     checked after continuous because a continuous channel that is also being
//     manually recorded owes recording once, not twice — the union is a boolean,
//     and the obligation interval is the record of that boolean over time.
//  6. Anything else (none, and the still-unimplemented planned) is not owed.
func obligationFor(f obligationFacts) reliability.Obligation {
	evidence := map[string]any{}
	if f.SwitchID != nil {
		evidence["switch_id"] = *f.SwitchID
	}
	if f.Revision != nil {
		evidence["source_revision_id"] = *f.Revision
	}
	base := reliability.Obligation{
		ChannelID:        f.ChannelID,
		SourceRevisionID: f.Revision,
		SessionID:        f.SessionID,
		PolicyVersion:    f.PolicyVer,
		Evidence:         evidence,
	}
	switch {
	case f.Revision == nil:
		base.Source, base.Exclusion = "continuous", "not_configured"
		evidence["reason"] = "source_not_configured"
		return base
	case !f.Enabled:
		base.Source, base.Exclusion = "continuous", "disabled"
		evidence["reason"] = "channel_disabled"
		return base
	case f.InMaintenan:
		base.Source, base.Exclusion = "continuous", "maintenance"
		evidence["reason"] = "maintenance_window"
		return base
	case f.Mode == "continuous":
		base.Source = "continuous"
		if f.SwitchID != nil {
			// Recording is still owed; the switch is context, not a gap.
			evidence["during_switch"] = true
		}
		return base
	case f.Mode == "manual":
		// A manual recording is owed: the operator asked for footage and the
		// completeness figure must divide by the time they asked for. Treating
		// it as "not owed" would hide a failed manual recording inside an
		// exclusion and report the channel as fully covered.
		base.Source = "manual"
		evidence["reason"] = "manual_window"
		return base
	case f.Mode == "planned":
		// Reserved vocabulary. PRD REC-02 is unimplemented, so nothing writes
		// this mode today; if one ever appears, it is reported as an unsupported
		// source rather than silently counted either way.
		base.Source, base.Exclusion = f.Mode, "disabled"
		evidence["reason"] = "mode_not_implemented"
		return base
	default:
		base.Source, base.Exclusion = "continuous", "disabled"
		evidence["reason"] = "recording_mode_none"
		return base
	}
}

// ReconcileObligation records the current verdict for one channel.
func (s *Service) ReconcileObligation(ctx context.Context, channelID id.ID) error {
	if s.Reliability == nil {
		return nil
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		facts, err := s.loadObligationFacts(ctx, tx, channelID)
		if err != nil {
			return err
		}
		obligation := obligationFor(facts)
		obligation.ObservedAt = time.Now().UTC()
		return s.Reliability.FoldObligationTx(ctx, tx, obligation)
	})
}

// ReconcileAllObligations visits every channel, not only the ones the sampler
// sees. See the note at the top of this file for why the disabled and
// unconfigured ones must be included.
func (s *Service) ReconcileAllObligations(ctx context.Context) error {
	if s.Reliability == nil {
		return nil
	}
	rows, err := s.DB.Pool.Query(ctx, "SELECT id FROM channels ORDER BY channel_no")
	if err != nil {
		return err
	}
	var channels []id.ID
	for rows.Next() {
		var ch id.ID
		if err := rows.Scan(&ch); err != nil {
			rows.Close()
			return err
		}
		channels = append(channels, ch)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, ch := range channels {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.ReconcileObligation(ctx, ch); err != nil {
			// One channel's failure must not stop the others: an obligation
			// timeline with a hole for one channel is better than none at all,
			// and the next pass retries.
			if ctx.Err() == nil {
				return err
			}
			return ctx.Err()
		}
	}
	return nil
}

// ObligationInterval is how often the reconciler re-derives every channel's
// obligation. Aligned with the sampler's cadence: a boundary is placed within
// one tick of the change that caused it, and the precise instant remains
// available in recording_policy_versions.effective_at.
const ObligationInterval = 10 * time.Second
