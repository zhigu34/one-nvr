package reliability

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Alert rule administration (PRD REL-05).
//
// Rules default to nothing: an upgraded site has no alert rules, so no new
// alarms appear until an operator asks for them. That is deliberate — the PRD
// says the thresholds are configurable, not that any particular one is on, and a
// release that starts paging people by itself gets its alerts muted within a
// week.

// AlertRuleInput is one rule to create or update.
type AlertRuleInput struct {
	ChannelID        *id.ID   `json:"channel_id"`
	Kind             string   `json:"kind"`
	ThresholdMS      *int64   `json:"threshold_ms"`
	ThresholdPercent *float64 `json:"threshold_percent"`
	Enabled          *bool    `json:"enabled"`
}

// AlertKinds lists the vocabulary this build evaluates, so a client renders the
// rules that exist rather than a copy that drifts.
func AlertKinds() []string {
	return []string{AlertMainDownStreak, AlertRecordingTimeout, AlertDailyCoverageDrop, AlertMonitoringStopped}
}

// validAlertRule rejects a rule this build could never evaluate, at write time
// rather than silently ignoring it at evaluation time.
func validAlertRule(in AlertRuleInput) bool {
	switch in.Kind {
	case AlertMainDownStreak, AlertRecordingTimeout, AlertMonitoringStopped:
		// Duration thresholds. A percentage here would be unevaluatable.
		return in.ThresholdMS != nil && *in.ThresholdMS > 0 && in.ThresholdPercent == nil
	case AlertDailyCoverageDrop:
		return in.ThresholdPercent != nil && *in.ThresholdPercent >= 0 && *in.ThresholdPercent <= 100 && in.ThresholdMS == nil
	}
	return false
}

// SetAlertRule creates or replaces the rule for a channel and kind.
//
// Upsert rather than create-and-delete: a rule has an identity that firing
// alerts point at, and replacing the row would orphan the history that explains
// why an alert fired last month.
func (s *Service) SetAlertRule(ctx context.Context, p auth.Principal, in AlertRuleInput) (AlertRule, error) {
	var out AlertRule
	if s.DB == nil || s.Auth == nil || !validAlertRule(in) {
		return out, auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	if in.ChannelID != nil {
		// A rule about a channel's media is configuration for that channel, so
		// the administrator must also hold the configure grant on it.
		if err := s.Auth.RequireChannel(ctx, p, *in.ChannelID, auth.Configure); err != nil {
			return out, err
		}
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		ruleID, err := id.New()
		if err != nil {
			return err
		}
		// The unique constraints are per (channel, kind) and per site-wide kind,
		// so the conflict target differs by scope. Two statements rather than a
		// clever ON CONFLICT, because a partial unique index cannot be targeted
		// by a plain column list.
		if in.ChannelID == nil {
			if err := tx.QueryRow(ctx, `
				INSERT INTO reliability_alert_rules(id,channel_id,kind,threshold_ms,threshold_percent,enabled,updated_by)
				VALUES($1,NULL,$2,$3,$4,$5,$6)
				ON CONFLICT(kind) WHERE channel_id IS NULL DO UPDATE SET
					threshold_ms=EXCLUDED.threshold_ms,threshold_percent=EXCLUDED.threshold_percent,
					enabled=EXCLUDED.enabled,updated_by=EXCLUDED.updated_by,
					version=reliability_alert_rules.version+1,updated_at=clock_timestamp()
				RETURNING id,channel_id,kind,threshold_ms,threshold_percent,enabled,version`,
				ruleID, in.Kind, in.ThresholdMS, in.ThresholdPercent, enabled, p.UserID).
				Scan(&out.ID, &out.ChannelID, &out.Kind, &out.ThresholdMS, &out.ThresholdPercent, &out.Enabled, &out.Version); err != nil {
				return err
			}
			return nil
		}
		return tx.QueryRow(ctx, `
			INSERT INTO reliability_alert_rules(id,channel_id,kind,threshold_ms,threshold_percent,enabled,updated_by)
			VALUES($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT(channel_id,kind) DO UPDATE SET
				threshold_ms=EXCLUDED.threshold_ms,threshold_percent=EXCLUDED.threshold_percent,
				enabled=EXCLUDED.enabled,updated_by=EXCLUDED.updated_by,
				version=reliability_alert_rules.version+1,updated_at=clock_timestamp()
			RETURNING id,channel_id,kind,threshold_ms,threshold_percent,enabled,version`,
			ruleID, in.ChannelID, in.Kind, in.ThresholdMS, in.ThresholdPercent, enabled, p.UserID).
			Scan(&out.ID, &out.ChannelID, &out.Kind, &out.ThresholdMS, &out.ThresholdPercent, &out.Enabled, &out.Version)
	})
	return out, err
}

// ListAlertRules returns every configured rule.
func (s *Service) ListAlertRules(ctx context.Context, p auth.Principal) ([]AlertRule, error) {
	out := []AlertRule{}
	if s.DB == nil || s.Auth == nil {
		return out, auth.ErrInvalid
	}
	// Thresholds are site configuration, so reading them is an administrator's
	// view rather than a channel-level one.
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	rows, err := s.DB.Pool.Query(ctx, `
		SELECT id,channel_id,kind,threshold_ms,threshold_percent,enabled,version
		FROM reliability_alert_rules ORDER BY channel_id NULLS FIRST,kind`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item AlertRule
		if err := rows.Scan(&item.ID, &item.ChannelID, &item.Kind, &item.ThresholdMS, &item.ThresholdPercent, &item.Enabled, &item.Version); err != nil {
			return out, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// DeleteAlertRule removes a rule and, by cascade, its alerts.
//
// The cascade is deliberate and worth stating: firing alerts have no meaning
// without the rule that defined them, and keeping them would leave rows whose
// threshold no longer exists anywhere.
func (s *Service) DeleteAlertRule(ctx context.Context, p auth.Principal, ruleID id.ID) error {
	if s.DB == nil || s.Auth == nil || ruleID == "" {
		return auth.ErrInvalid
	}
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return err
	}
	tag, err := s.DB.Pool.Exec(ctx, "DELETE FROM reliability_alert_rules WHERE id=$1", ruleID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrNotFound
	}
	return nil
}

// MarkAlertNotified records that a firing alert has been announced.
//
// Called by the delivery path, not the evaluator: the evaluator decides whether
// a threshold is crossed, and only the thing that actually sends decides whether
// a message went out. Marking before sending would lose an alert to a failed
// send; marking after is what makes "notified" mean "delivered".
func (s *Service) MarkAlertNotified(ctx context.Context, alertID id.ID, at time.Time) error {
	if s.DB == nil || alertID == "" {
		return auth.ErrInvalid
	}
	_, err := s.DB.Pool.Exec(ctx, `UPDATE reliability_alerts SET notified_at=$2,updated_at=clock_timestamp()
		WHERE id=$1 AND notified_at IS NULL`, alertID, at.UTC())
	return err
}
