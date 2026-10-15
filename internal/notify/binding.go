package notify

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// SetRuleChannels replaces which destinations a rule tells.
//
// The binding lives on the rule because the rule already expresses *which
// events* matter — its zone, categories, arming window and score floor. Asking
// a second place to say who hears about them would split one decision in two
// and let them disagree.
//
// An empty list is a legitimate choice: the rule still records its hits, it
// just tells no one. That is how an operator keeps evidence of detections
// without being pushed about them.
func (s *Service) SetRuleChannels(ctx context.Context, p auth.Principal, ruleID id.ID, channelIDs []id.ID) error {
	if len(channelIDs) > 32 {
		return auth.ErrInvalid
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		// The rule's own channel decides who may edit it: binding a
		// notification is configuring that channel's detection.
		var channelID id.ID
		if err := tx.QueryRow(ctx, `SELECT channel_id FROM detection_rules WHERE id=$1`, ruleID).Scan(&channelID); err != nil {
			if errIsNoRows(err) {
				return auth.ErrNotFound
			}
			return err
		}
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		// Every named channel must exist; otherwise the binding would be
		// recorded and silently never deliver.
		for _, target := range channelIDs {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_channels WHERE id=$1 AND site_id=$2)`, target, s.siteID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrChannelNotFound
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM rule_notification_channels WHERE rule_id=$1`, ruleID); err != nil {
			return err
		}
		for _, target := range channelIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO rule_notification_channels(rule_id,channel_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, ruleID, target); err != nil {
				return err
			}
		}
		details, _ := json.Marshal(map[string]any{"channels": len(channelIDs), "channel_id": channelID})
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "notification.rule_channels_set", ObjectID: ruleID, Details: details})
	})
}

// RuleChannels lists the destinations one rule tells, for the rule editor.
func (s *Service) RuleChannels(ctx context.Context, p auth.Principal, ruleID id.ID) ([]id.ID, error) {
	out := []id.ID{}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var channelID id.ID
		if err := tx.QueryRow(ctx, `SELECT channel_id FROM detection_rules WHERE id=$1`, ruleID).Scan(&channelID); err != nil {
			if errIsNoRows(err) {
				return auth.ErrNotFound
			}
			return err
		}
		// Reading is part of seeing the rule, which needs playback; writing the
		// binding needs configure. The asymmetry matches the rule endpoints.
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Playback, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT channel_id FROM rule_notification_channels WHERE rule_id=$1 ORDER BY channel_id`, ruleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var target id.ID
			if err := rows.Scan(&target); err != nil {
				return err
			}
			out = append(out, target)
		}
		return rows.Err()
	})
	return out, err
}

func errIsNoRows(err error) bool {
	return err == pgx.ErrNoRows
}
