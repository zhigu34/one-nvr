package notify

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

// HitFacts is what a rule hit contributes to a notification. The events package
// fills it in at match time; this package never reads the event itself, so the
// two stay separable and the message can be asserted without a database.
type HitFacts struct {
	HitID    id.ID
	EventID  id.ID
	RuleID   id.ID
	RuleName string
	Severity string
	// CooldownSeconds is the rule's current setting. Cooldown is an operator
	// preference about how often to be told, not a fact about the event, so it
	// is read now rather than frozen with the hit.
	CooldownSeconds int
	// Message facts, already gathered by the caller (it holds the event row).
	Message MessageInput
}

// Raised reports what happened for one hit, so the caller can tell the operator
// why a rule produced no outbound delivery.
type Raised struct {
	// Notified reports whether this hit produced at least one delivery.
	Notified bool
	// Suppressed is set when the cooldown swallowed the outbound deliveries.
	// The hit itself is still recorded; only telling is postponed.
	Suppressed bool
	// DeliveryIDs are the outbound deliveries queued by this call.
	DeliveryIDs []id.ID
}

// Raise runs inside the event-ingestion transaction. It writes the inbox
// notification for every bound channel and queues outbound deliveries, so an
// event and the fact that somebody was told about it commit together: a crash
// cannot leave a rule hit nobody was ever notified of, nor a notification whose
// hit was rolled back.
//
// The inbox is never subject to the cooldown. The operator asked to know that
// something happened at a moment; suppressing the record of it would defeat
// that, and AI-05 forbids dropping the record regardless. The cooldown governs
// only how often an external channel is pushed.
func (s *Service) Raise(ctx context.Context, tx pgx.Tx, facts HitFacts) (Raised, error) {
	var out Raised
	bound, err := s.boundChannels(ctx, tx, facts.RuleID)
	if err != nil {
		return out, err
	}
	if len(bound) == 0 {
		// A rule nobody bound records its hit and tells no one: the binding is
		// what turns a hit into a delivery.
		return out, nil
	}

	// The outbound channels are the ones the cooldown governs.
	external := make([]boundChannel, 0, len(bound))
	for _, channel := range bound {
		if channel.Kind != "inapp" {
			external = append(external, channel)
		}
	}
	// One cooldown question for the whole hit: the rule asks "have I told this
	// channel recently?", and repeating it per channel would answer with rows
	// this same call just wrote.
	withinCooldown, err := s.withinCooldown(ctx, tx, facts)
	if err != nil {
		return out, err
	}

	message, err := buildMessage(facts.Message, mustID())
	if err != nil {
		return out, err
	}
	// The stored message carries the rule's identity so the inbox can link back
	// without a join through the hit.
	notificationID, err := id.New()
	if err != nil {
		return out, err
	}
	stored, err := json.Marshal(message)
	if err != nil {
		return out, err
	}
	// A hit is told once. A later MQTT update of the same event refreshes the
	// hit's reason (AI-05) and runs this path again; the fence on hit_id turns
	// that into a no-op here rather than a second alert for one thing that
	// happened. Nothing is written when the row already exists, so the delivery
	// inserts below must be skipped with it.
	tag, err := tx.Exec(ctx, `INSERT INTO notifications(id,hit_id,event_id,channel_id,rule_id,delivery_id,message,severity)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(hit_id) WHERE hit_id IS NOT NULL DO NOTHING`,
		notificationID, facts.HitID, facts.EventID, message.ChannelID, facts.RuleID, message.DeliveryID, stored, facts.Severity)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() == 0 {
		out.Notified = true
		return out, nil
	}
	// The inbox row is itself the delivery: no channel row, no queue, nothing to
	// retry. It is recorded as an in-app delivery so "every bound channel has a
	// delivery" stays true without an exception in the reading code.
	if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(id,notification_id,channel_id,state,delivered_at)
		SELECT $1,$2,$3,'succeeded',clock_timestamp() FROM notification_channels WHERE id=$3 AND kind='inapp'
		ON CONFLICT(notification_id,channel_id) DO NOTHING`, mustID(), notificationID, message.ChannelID); err != nil {
		return out, err
	}
	if len(external) == 0 {
		out.Notified = true
		return out, nil
	}
	if withinCooldown {
		// The hit is recorded and the inbox carries it; the repeated push is
		// what the cooldown exists to prevent.
		out.Suppressed = true
		return out, nil
	}

	for _, channel := range external {
		deliveryID, err := id.New()
		if err != nil {
			return out, err
		}
		payload, err := json.Marshal(deliveryPayload{NotificationID: notificationID, ChannelID: channel.ID})
		if err != nil {
			return out, err
		}
		retries := webhookRetries
		// The idempotency key is the notification and channel: a retry of the
		// same delivery must not enqueue a second job, and a new notification
		// always keys differently.
		if _, err := jobs.Enqueue(ctx, tx, jobs.Input{
			Kind:           JobKindWebhook,
			ObjectID:       notificationID,
			IdempotencyKey: "notify:" + string(notificationID) + ":" + string(channel.ID),
			Payload:        payload,
			MaxAttempts:    &retries,
		}); err != nil {
			return out, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(id,notification_id,channel_id,state)
			VALUES($1,$2,$3,'sending') ON CONFLICT(notification_id,channel_id) DO NOTHING`,
			deliveryID, notificationID, channel.ID); err != nil {
			return out, err
		}
		out.DeliveryIDs = append(out.DeliveryIDs, deliveryID)
	}
	out.Notified = true
	return out, nil
}

type boundChannel struct {
	ID   id.ID
	Kind string
}

// boundChannels lists the destinations a rule tells, enabled only: a channel
// the operator switched off must not be queued for.
func (s *Service) boundChannels(ctx context.Context, tx pgx.Tx, ruleID id.ID) ([]boundChannel, error) {
	rows, err := tx.Query(ctx, `SELECT c.id,c.kind FROM rule_notification_channels b
		JOIN notification_channels c ON c.id=b.channel_id AND c.enabled
		WHERE b.rule_id=$1 ORDER BY c.created_at,c.id`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []boundChannel{}
	for rows.Next() {
		var entry boundChannel
		if err := rows.Scan(&entry.ID, &entry.Kind); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// withinCooldown asks whether this rule already told this channel recently.
// Zero disables the check, which is how an operator asks to hear about every
// single hit.
//
// The window is measured against notifications already raised for the rule, so
// it answers in terms of "did we tell them", not "did the detector blink" —
// repeated updates to one event therefore do not re-push, which is exactly the
// volume the cooldown is for.
func (s *Service) withinCooldown(ctx context.Context, tx pgx.Tx, facts HitFacts) (bool, error) {
	if facts.CooldownSeconds <= 0 {
		return false, nil
	}
	var recent bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM notifications n
		JOIN notification_deliveries d ON d.notification_id=n.id
		JOIN notification_channels c ON c.id=d.channel_id AND c.kind<>'inapp'
		WHERE n.rule_id=$1 AND n.channel_id=$2
			AND n.created_at > clock_timestamp() - make_interval(secs => $3))`,
		facts.RuleID, facts.Message.ChannelID, facts.CooldownSeconds).Scan(&recent)
	return recent, err
}

// deliveryPayload is what a queued job carries: the two identities needed to
// find the notification and its channel. The message itself is read back from
// the row at send time, so a retry sends the frozen facts rather than a copy
// that could have drifted.
type deliveryPayload struct {
	NotificationID id.ID `json:"notification_id"`
	ChannelID      id.ID `json:"channel_id"`
}

// DeliverTimeout is how long the worker gives one webhook attempt. Exported so
// the assembly and the tests agree with the adapter.
var DeliverTimeout = 30 * time.Second
