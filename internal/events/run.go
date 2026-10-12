package events

import (
	"context"
	"log/slog"
	"time"
)

// TopicPrefix is the Frigate MQTT namespace of the locked image (0.17): every
// event message arrives on frigate/events.
const TopicPrefix = "frigate/events"

const (
	reconnectMin = 2 * time.Second
	reconnectMax = 60 * time.Second
)

// Run ingests the Frigate event stream until the context ends. A broker or
// network outage is recorded as a stream gap and retried with capped backoff:
// events missed while down must be visible as a hole, never as silence
// (EVT-04/CH-08).
func (s *Service) Run(ctx context.Context, address, username, password string) {
	backoff := reconnectMin
	for ctx.Err() == nil {
		started := time.Now().UTC()
		err := s.consume(ctx, address, username, password)
		if ctx.Err() != nil {
			return
		}
		reason := "mqtt_unavailable"
		if err != nil && err != ErrMQTTUnavailable {
			reason = "stream_error"
		}
		if gapErr := s.NoteStreamGap(ctx, started, reason); gapErr != nil {
			slog.Warn("event stream gap not recorded", "error", gapErr)
		}
		slog.Warn("event stream interrupted", "reason", reason, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < reconnectMax {
			backoff *= 2
			if backoff > reconnectMax {
				backoff = reconnectMax
			}
		}
	}
}

// consume holds one broker session: dial, subscribe, then pump messages. Any
// return means the session is gone and the caller records a gap and retries.
func (s *Service) consume(ctx context.Context, address, username, password string) error {
	client, err := Dial(ctx, address, username, password, "one-nvr-events")
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Subscribe(ctx, TopicPrefix); err != nil {
		return err
	}
	// A successful session resets the backoff by returning only on failure;
	// the caller's doubling restarts from the minimum on the next success.
	for {
		payload, err := client.Receive(ctx)
		if err != nil {
			return err
		}
		if err := s.Ingest(ctx, payload); err != nil && ctx.Err() == nil {
			slog.Warn("event payload rejected", "error", err)
		}
	}
}

// RunExpiry sweeps the 90-day index hourly (EVT-07).
func (s *Service) RunExpiry(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		s.SweepExpiry(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
