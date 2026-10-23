package events

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// ErrExclusionInvalid is returned for a drawing or window the evaluator cannot
// interpret. Refusing at save time keeps a stored rule always evaluable — a rule
// that cannot be evaluated would silently stop suppressing, and the operator
// would conclude the exclusion does not work.
var ErrExclusionInvalid = fault.New(422, "exclusion_invalid", "排除规则不完整：至少三个顶点，坐标在 0..1，时段两端都要填")

// ErrExclusionConflict is returned when the row changed since it was read.
var ErrExclusionConflict = fault.New(409, "exclusion_version_conflict", "该排除规则已被他人修改，请刷新后重试")

// ExclusionView is the operator-facing shape: the geometry, the window and — as
// the reason a rule is trustworthy — how much it has actually suppressed.
type ExclusionView struct {
	Exclusion
	ChannelID id.ID `json:"channel_id"`
	Enabled   bool  `json:"enabled"`
	Version   int64 `json:"version"`
	// Suppressed24h is how many events this rule has excluded in the last day.
	// Without it an operator cannot tell a narrow rule from one that is eating
	// the channel: both look like "no alarms".
	Suppressed24h int `json:"suppressed_24h"`
}

// ExclusionInput is a create or update. Version 0 means "create".
type ExclusionInput struct {
	Name        string       `json:"name"`
	Coordinates [][2]float64 `json:"coordinates"`
	Categories  []string     `json:"categories"`
	FromClock   string       `json:"from_clock"`
	ToClock     string       `json:"to_clock"`
	Reason      string       `json:"reason"`
	Enabled     bool         `json:"enabled"`
	Version     int64        `json:"version"`
}

// ListExclusions returns a channel's rules with their suppression counts.
func (s *Service) ListExclusions(ctx context.Context, p auth.Principal, channelID id.ID) ([]ExclusionView, error) {
	if s.Auth == nil {
		return nil, auth.ErrNotFound
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Configure); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return nil, auth.ErrNotFound
		}
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT e.id,e.name,e.coordinates,e.categories,
			COALESCE(e.from_clock,''),COALESCE(e.to_clock,''),e.reason,e.enabled,e.version,
			(SELECT count(*) FROM events ev WHERE ev.excluded_rule_id=e.id
			   AND ev.started_at > clock_timestamp() - interval '24 hours')
		FROM detection_exclusions e WHERE e.channel_id=$1 ORDER BY e.name`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ExclusionView, 0)
	for rows.Next() {
		var (
			view      ExclusionView
			rawCoords []byte
			rawCats   []byte
		)
		if err := rows.Scan(&view.ID, &view.Name, &rawCoords, &rawCats,
			&view.FromClock, &view.ToClock, &view.Reason, &view.Enabled, &view.Version,
			&view.Suppressed24h); err != nil {
			return nil, err
		}
		view.ChannelID = channelID
		view.Coordinates = [][2]float64{}
		view.Categories = []string{}
		_ = json.Unmarshal(rawCoords, &view.Coordinates)
		_ = json.Unmarshal(rawCats, &view.Categories)
		out = append(out, view)
	}
	return out, rows.Err()
}

// SaveExclusion creates or replaces one rule.
func (s *Service) SaveExclusion(ctx context.Context, p auth.Principal, channelID id.ID, in ExclusionInput) (ExclusionView, error) {
	if s.Auth == nil {
		return ExclusionView{}, auth.ErrNotFound
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Configure); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return ExclusionView{}, auth.ErrNotFound
		}
		return ExclusionView{}, err
	}
	if len(in.Name) < 1 || len(in.Name) > 64 {
		return ExclusionView{}, ErrExclusionInvalid
	}
	reason := in.Reason
	if reason == "" {
		reason = "时段内区域排除"
	}
	if len(reason) > 64 {
		return ExclusionView{}, ErrExclusionInvalid
	}
	if err := validateExclusion(Exclusion{
		Coordinates: in.Coordinates,
		FromClock:   in.FromClock,
		ToClock:     in.ToClock,
	}); err != nil {
		return ExclusionView{}, ErrExclusionInvalid
	}
	coords, err := json.Marshal(in.Coordinates)
	if err != nil {
		return ExclusionView{}, err
	}
	categories := in.Categories
	if categories == nil {
		categories = []string{}
	}
	cats, err := json.Marshal(categories)
	if err != nil {
		return ExclusionView{}, err
	}
	ruleID, err := id.New()
	if err != nil {
		return ExclusionView{}, err
	}
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if in.Version == 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO detection_exclusions
					(id,channel_id,name,coordinates,categories,from_clock,to_clock,reason,enabled)
				VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9)`,
				ruleID, channelID, in.Name, coords, cats, in.FromClock, in.ToClock, reason, in.Enabled); err != nil {
				return err
			}
			return audit.Append(ctx, tx, audit.Entry{
				ActorID: p.UserID, Action: "detection.exclusion_created", ObjectID: ruleID,
				Details: exclusionAudit(in),
			})
		}
		tag, err := tx.Exec(ctx, `UPDATE detection_exclusions SET
				name=$3,coordinates=$4,categories=$5,from_clock=NULLIF($6,''),to_clock=NULLIF($7,''),
				reason=$8,enabled=$9,version=version+1,updated_at=clock_timestamp()
			WHERE id=$1 AND channel_id=$2 AND version=$10`,
			ruleID, channelID, in.Name, coords, cats, in.FromClock, in.ToClock, reason, in.Enabled, in.Version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrExclusionConflict
		}
		return audit.Append(ctx, tx, audit.Entry{
			ActorID: p.UserID, Action: "detection.exclusion_updated", ObjectID: ruleID,
			Details: exclusionAudit(in),
		})
	})
	if err != nil {
		return ExclusionView{}, err
	}
	list, err := s.ListExclusions(ctx, p, channelID)
	if err != nil {
		return ExclusionView{}, err
	}
	for _, view := range list {
		if view.Name == in.Name {
			return view, nil
		}
	}
	return ExclusionView{}, auth.ErrNotFound
}

// DeleteExclusion removes one rule.
//
// The events it already suppressed keep their `excluded_reason` (the FK is ON
// DELETE SET NULL, so the row survives without the rule). Deleting a rule must
// not retroactively reveal — or hide — what was recorded while it was in force:
// the operator asked for those events to stay out of the list, and that decision
// was made at the time.
func (s *Service) DeleteExclusion(ctx context.Context, p auth.Principal, channelID, ruleID id.ID, expected int64) error {
	if s.Auth == nil {
		return auth.ErrNotFound
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Configure); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			return auth.ErrNotFound
		}
		return err
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM detection_exclusions WHERE id=$1 AND channel_id=$2 AND version=$3`,
			ruleID, channelID, expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM detection_exclusions WHERE id=$1 AND channel_id=$2)`,
				ruleID, channelID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return auth.ErrNotFound
			}
			return ErrExclusionConflict
		}
		return audit.Append(ctx, tx, audit.Entry{
			ActorID: p.UserID, Action: "detection.exclusion_deleted", ObjectID: ruleID,
		})
	})
}

// exclusionAudit records the shape of the rule but not the polygon: a long
// coordinate list is noise in an audit trail whose question is "what changed".
func exclusionAudit(in ExclusionInput) []byte {
	raw, _ := json.Marshal(map[string]any{
		"name":       in.Name,
		"from_clock": in.FromClock,
		"to_clock":   in.ToClock,
		"vertices":   len(in.Coordinates),
		"categories": in.Categories,
		"enabled":    in.Enabled,
	})
	return raw
}
