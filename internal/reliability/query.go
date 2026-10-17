package reliability

import (
	"context"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Reads for the API and, in the next batch, the reliability page. They return
// facts, not rates: a rate computed here would hide its own denominator, and
// PRD REL-04 requires every ratio to travel with its numerator, denominator and
// unjudgeable time so a reader can recompute it.

// IntervalPage is a window of state history for one channel.
type IntervalPage struct {
	Items []IntervalView `json:"items"`
	// MonitoringCoverage and friends are deliberately absent: this batch
	// exposes intervals, and the batch that computes rates will add them with
	// their inputs.
}

// IntervalView is one interval as the API reports it.
type IntervalView struct {
	ID        id.ID      `json:"id"`
	Kind      string     `json:"kind"`
	State     string     `json:"state"`
	Reason    string     `json:"reason"`
	Exclusion string     `json:"exclusion,omitempty"`
	Started   time.Time  `json:"started_at"`
	Ended     *time.Time `json:"ended_at"`
	// The uncertainty band REL-01 requires: polling cannot know the exact
	// instant a state changed, so both bounds travel and the change happened
	// between them.
	FirstObserved time.Time `json:"first_observed_at"`
	LastObserved  time.Time `json:"last_observed_at"`
	ObservedCount int       `json:"observed_count"`
	DurationMS    int64     `json:"duration_ms"`
}

// ListIntervals returns one channel's state history inside a window.
//
// Reading intervals is reading evidence about a channel's media, so it travels
// with the playback grant — the same grant that lets the operator see the
// recordings those intervals describe.
func (s *Service) ListIntervals(ctx context.Context, p auth.Principal, channelID id.ID, from, to time.Time, kinds []string) (IntervalPage, error) {
	out := IntervalPage{Items: []IntervalView{}}
	if s.DB == nil || s.Auth == nil || channelID == "" || !to.After(from) {
		return out, auth.ErrInvalid
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Playback); err != nil {
		return out, err
	}
	// An interval that overlaps the window at all is relevant, even if it
	// started before it: availability inside the window depends on it.
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,kind,state,reason_code,COALESCE(exclusion,''),started_at,ended_at,
			first_observed_at,last_observed_at,observed_count,
			GREATEST(0,EXTRACT(EPOCH FROM (COALESCE(ended_at,$2) - started_at))*1000)::bigint
		FROM reliability_intervals
		WHERE channel_id=$1 AND started_at < $2 AND COALESCE(ended_at,$2) > $3
			AND ($4::text[] IS NULL OR kind = ANY($4))
		ORDER BY started_at DESC,id DESC
		LIMIT 2000`, channelID, to.UTC(), from.UTC(), nullableKinds(kinds))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item IntervalView
		if err := rows.Scan(&item.ID, &item.Kind, &item.State, &item.Reason, &item.Exclusion,
			&item.Started, &item.Ended, &item.FirstObserved, &item.LastObserved, &item.ObservedCount, &item.DurationMS); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}

func nullableKinds(kinds []string) any {
	if len(kinds) == 0 {
		return nil
	}
	return kinds
}

// FaultView is one fault as the API reports it.
type FaultView struct {
	ID        id.ID `json:"id"`
	ChannelID id.ID `json:"channel_id"`
	// ChannelNo is the operator-facing number. It has to travel with the fault
	// because the fault page renders "CH<number>": without it the UI printed
	// CHundefined for every row, which is what the contract already promised
	// but the implementation never sent.
	ChannelNo int `json:"channel_no"`
	// ChannelName is empty for a channel that was never named. Empty is
	// reported as-is rather than substituted: an unnamed channel is not a
	// channel called "unknown".
	ChannelName string     `json:"channel_name,omitempty"`
	Kind        string     `json:"kind"`
	State       string     `json:"state"`
	Started     time.Time  `json:"started_at"`
	Ended       *time.Time `json:"ended_at"`
	DurationMS  *int64     `json:"duration_ms,omitempty"`
	// The automatic attribution, never rewritten. Effective is what a report
	// counts by; both travel so a reader can tell whether an operator has ruled.
	InitialReason     string    `json:"initial_reason"`
	InitialCategory   string    `json:"initial_category"`
	ConfirmedReason   string    `json:"confirmed_reason,omitempty"`
	ConfirmedCategory string    `json:"confirmed_category,omitempty"`
	EffectiveCategory string    `json:"effective_category"`
	Note              string    `json:"note,omitempty"`
	RootEventID       *id.ID    `json:"root_event_id,omitempty"`
	Maintenance       bool      `json:"maintenance"`
	Recovery          string    `json:"recovery,omitempty"`
	Revisions         []byte    `json:"revisions,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// FaultQuery filters a fault list. Zero values mean "no restriction", the same
// convention the event index uses.
type FaultQuery struct {
	ChannelID id.ID
	Category  string
	State     string
	From, To  time.Time
	Limit     int
}

// ListFaults returns faults overlapping a window, newest first.
func (s *Service) ListFaults(ctx context.Context, p auth.Principal, q FaultQuery) ([]FaultView, error) {
	out := []FaultView{}
	if s.DB == nil || s.Auth == nil || q.From.IsZero() || !q.To.After(q.From) {
		return out, auth.ErrInvalid
	}
	if q.Limit < 1 || q.Limit > 500 {
		q.Limit = 200
	}
	if q.ChannelID != "" {
		if err := s.Auth.RequireChannel(ctx, p, q.ChannelID, auth.Playback); err != nil {
			return out, err
		}
	} else {
		// Without a channel the answer spans channels the operator may not be
		// allowed to see, so it is an administrator's view.
		if err := s.Auth.RequireAdmin(ctx, p); err != nil {
			return out, err
		}
	}
	var category any
	if q.Category != "" {
		category = q.Category
	}
	var state any
	if q.State != "" {
		state = q.State
	}
	var channel any
	if q.ChannelID != "" {
		channel = q.ChannelID
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT f.id,f.channel_id,c.channel_no,COALESCE(c.channel_name,''),f.kind,f.state,f.started_at,f.ended_at,f.duration_ms,
			f.initial_reason,f.category,COALESCE(f.confirmed_reason,''),COALESCE(f.confirmed_category,''),
			f.note,f.root_event_id,f.maintenance,COALESCE(f.recovery,''),f.revisions,f.created_at
		FROM fault_events f JOIN channels c ON c.id=f.channel_id
		WHERE f.started_at < $1 AND COALESCE(f.ended_at,$1) > $2
			AND ($3::uuid IS NULL OR f.channel_id=$3)
			AND ($4::text IS NULL OR COALESCE(f.confirmed_category,f.category)=$4)
			AND ($5::text IS NULL OR f.state=$5)
		ORDER BY f.started_at DESC,f.id DESC
		LIMIT $6`, q.To.UTC(), q.From.UTC(), channel, category, state, q.Limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item FaultView
		if err := rows.Scan(&item.ID, &item.ChannelID, &item.ChannelNo, &item.ChannelName, &item.Kind, &item.State, &item.Started, &item.Ended, &item.DurationMS,
			&item.InitialReason, &item.InitialCategory, &item.ConfirmedReason, &item.ConfirmedCategory,
			&item.Note, &item.RootEventID, &item.Maintenance, &item.Recovery, &item.Revisions, &item.CreatedAt); err != nil {
			return out, err
		}
		item.EffectiveCategory = EffectiveCategory(item.InitialCategory, item.ConfirmedCategory)
		out = append(out, item)
	}
	return out, rows.Err()
}

// FaultImpact reports a root incident's real business impact: the union of its
// children, not their sum.
func (s *Service) FaultImpact(ctx context.Context, p auth.Principal, rootID id.ID) (time.Duration, error) {
	if s.DB == nil || s.Auth == nil {
		return 0, auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return 0, err
	}
	return s.ImpactUnion(ctx, rootID)
}
