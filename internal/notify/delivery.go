package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

// HandleWebhook delivers one queued notification to one channel. It runs in the
// worker, never in the request or ingestion path, so a receiver that is down or
// slow delays only the retry it is already handling.
func (s *Service) HandleWebhook(ctx context.Context, lease jobs.Lease) (jobs.Result, error) {
	var payload deliveryPayload
	if err := json.Unmarshal(lease.Payload, &payload); err != nil {
		// The payload was written by this program; if it cannot be read, no
		// retry will help.
		return jobs.Result{}, &jobs.PermanentFailure{Code: "notification_payload_invalid"}
	}
	attempt, err := s.prepareDelivery(ctx, lease, payload)
	if err != nil {
		return jobs.Result{}, err
	}
	if attempt.message == nil {
		// Already delivered, or the channel is gone. Either way there is nothing
		// left to do and nothing worth retrying.
		return jobs.Result{}, nil
	}
	adapter, err := s.registry.Build(attempt.config)
	if err != nil {
		s.markDelivery(ctx, payload, StateExhausted, "channel_kind_unavailable", 0)
		return jobs.Result{}, &jobs.PermanentFailure{Code: "notification_channel_kind_unavailable"}
	}

	window, cancel := context.WithTimeout(ctx, DeliverTimeout)
	defer cancel()
	sendErr := adapter.Send(window, *attempt.message)
	if sendErr == nil {
		s.markDelivery(ctx, payload, StateSucceeded, "", 0)
		return jobs.Result{}, nil
	}
	if permanent := IsPermanent(sendErr); permanent {
		s.markDelivery(ctx, payload, StateExhausted, sendErr.Error(), StatusOf(sendErr))
		return jobs.Result{}, &jobs.PermanentFailure{Code: "notification_refused"}
	}
	// Retryable: the state says so on the way out, and the job layer decides
	// whether another attempt remains. A failure here never touches the event,
	// the recording or any other delivery (EVT-05).
	state := StateFailed
	if lease.Attempt > 1 {
		state = StateRetrying
	}
	s.markDelivery(ctx, payload, state, sendErr.Error(), StatusOf(sendErr))
	return jobs.Result{}, sendErr
}

type deliveryAttempt struct {
	message *Message
	config  ChannelConfig
}

// prepareDelivery reads the frozen message back and confirms the delivery is
// still worth making. A notification whose event has since expired takes its
// delivery rows with it (ON DELETE CASCADE), and a channel switched off after
// the queue was written is not told either.
func (s *Service) prepareDelivery(ctx context.Context, lease jobs.Lease, payload deliveryPayload) (deliveryAttempt, error) {
	var out deliveryAttempt
	var raw []byte
	var state string
	err := s.DB.Pool.QueryRow(ctx, `SELECT n.message,d.state FROM notifications n
		JOIN notification_deliveries d ON d.notification_id=n.id AND d.channel_id=$2
		WHERE n.id=$1`, payload.NotificationID, payload.ChannelID).Scan(&raw, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if state == StateSucceeded {
		// A retry that lost its race with a success must not send twice.
		return out, nil
	}
	var message Message
	if err := json.Unmarshal(raw, &message); err != nil {
		return out, err
	}
	config, err := s.channelConfig(ctx, payload.ChannelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		return out, err
	}
	out.message = &message
	out.config = config
	return out, nil
}

// markDelivery records what the attempt did. Every write is best effort against
// the passed context: losing the status update must not turn a completed send
// into a retried one, so a failure here is logged and the job's own state stays
// authoritative for the attempt count.
func (s *Service) markDelivery(ctx context.Context, payload deliveryPayload, state, reason string, status int) {
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var delivered any
	if state == StateSucceeded {
		delivered = time.Now().UTC()
	}
	if _, err := s.DB.Pool.Exec(write, `UPDATE notification_deliveries
		SET state=$3,last_error=$4,last_http_status=nullif($5,0),delivered_at=COALESCE($6,delivered_at),updated_at=clock_timestamp()
		WHERE notification_id=$1 AND channel_id=$2`,
		payload.NotificationID, payload.ChannelID, state, reason, status, delivered); err != nil {
		slog.Warn("notification delivery state not recorded", "error", err, "state", state)
	}
}

// RaiseDeliveryStates fixes up deliveries whose job has ended without the
// worker recording it: a job that exhausted its attempts is terminal even
// though nothing wrote that down. The jobs table cannot know that a given kind
// means a notification, which is why this reconciliation exists.
func (s *Service) ReconcileDeliveries(ctx context.Context) {
	if s.DB == nil {
		return
	}
	_, err := s.DB.Pool.Exec(ctx, `UPDATE notification_deliveries d
		SET state='exhausted',updated_at=clock_timestamp()
		WHERE d.state IN ('sending','failed','retrying')
			AND EXISTS(SELECT 1 FROM jobs j WHERE j.kind=$1 AND j.object_id=d.notification_id AND j.state='failed')`,
		JobKindWebhook)
	if err != nil && ctx.Err() == nil {
		slog.Warn("notification delivery reconciliation unavailable", "error", err)
	}
}

// DeliveryView is one delivery as the operator reads it: the channel, the state
// in EVT-05's words, and what the receiver said.
type DeliveryView struct {
	ChannelID      id.ID      `json:"channel_id"`
	ChannelName    string     `json:"channel_name"`
	ChannelKind    string     `json:"channel_kind"`
	NotificationID id.ID      `json:"notification_id"`
	State          string     `json:"state"`
	Error          string     `json:"error,omitempty"`
	HTTPStatus     *int       `json:"http_status,omitempty"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	// RuleName comes from the stored message rather than a live join, so a
	// renamed rule does not rewrite what was reported.
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

// DeliveriesForEvent lists what was attempted for one event, for the detail
// drawer. It reads the frozen message, so the list agrees with the inbox even
// after the rule or channel has been edited.
func (s *Service) DeliveriesForEvent(ctx context.Context, eventID id.ID) ([]DeliveryView, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT d.channel_id,c.name,c.kind,d.notification_id,d.state,d.last_error,
		d.last_http_status,d.delivered_at,d.updated_at,n.message,n.severity
		FROM notification_deliveries d
		JOIN notifications n ON n.id=d.notification_id
		JOIN notification_channels c ON c.id=d.channel_id
		WHERE n.event_id=$1
		ORDER BY d.updated_at DESC`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeliveryView{}
	for rows.Next() {
		var view DeliveryView
		var raw []byte
		var status *int
		if err := rows.Scan(&view.ChannelID, &view.ChannelName, &view.ChannelKind, &view.NotificationID,
			&view.State, &view.Error, &status, &view.DeliveredAt, &view.UpdatedAt, &raw, &view.Severity); err != nil {
			return nil, err
		}
		view.HTTPStatus = status
		var message Message
		if err := json.Unmarshal(raw, &message); err == nil {
			view.Title = message.Title
		}
		out = append(out, view)
	}
	return out, rows.Err()
}
