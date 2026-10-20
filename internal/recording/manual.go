package recording

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Manual recording bounds (PRD REC-03): 1 to 120 minutes, defaulting to 30.
const (
	ManualMinMinutes     = 1
	ManualMaxMinutes     = 120
	ManualDefaultMinutes = 30
)

// ErrManualWindowMissing reports that no active manual window exists.
var ErrManualWindowMissing = errors.New("manual_recording_absent")

// ManualWindow is one "record now for N minutes" request.
type ManualWindow struct {
	ChannelID        id.ID     `json:"channel_id"`
	RequestedMinutes int       `json:"requested_minutes"`
	StartedAt        time.Time `json:"started_at"`
	ExpiresAt        time.Time `json:"expires_at"`
	RemainingMS      int64     `json:"remaining_ms"`
}

// StartManual opens a manual window for one channel (REC-03).
//
// Pressing record twice replaces the deadline rather than stacking windows: the
// unique index allows one active row per channel, and the old row is closed with
// 'cancelled' so the history still shows what the operator did.
//
// The window does not touch recording_policies. A manual recording is an event,
// not a setting — and per REC-04 it must survive a policy edit made while it
// runs, which is only true if the two live in different places.
func (s *Service) StartManual(ctx context.Context, p auth.Principal, channelID id.ID, minutes int) (ManualWindow, error) {
	if minutes == 0 {
		minutes = ManualDefaultMinutes
	}
	if minutes < ManualMinMinutes || minutes > ManualMaxMinutes {
		return ManualWindow{}, auth.ErrInvalid
	}
	var out ManualWindow
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		// Close any window already running, keeping its row.
		if _, err := tx.Exec(ctx, `UPDATE recording_manual_windows
			SET ended_at=clock_timestamp(),end_reason='cancelled'
			WHERE channel_id=$1 AND ended_at IS NULL`, channelID); err != nil {
			return err
		}
		windowID, err := id.New()
		if err != nil {
			return err
		}
		// expires_at is computed from the same instant as started_at: deriving it
		// at read time from "now + minutes" would let a window that began an hour
		// ago still claim a full duration.
		if err := tx.QueryRow(ctx, `INSERT INTO recording_manual_windows(id,channel_id,requested_minutes,started_at,expires_at,actor_id)
			VALUES($1,$2,$3,clock_timestamp(),clock_timestamp()+make_interval(mins => $3),$4)
			RETURNING channel_id,requested_minutes,started_at,expires_at`,
			windowID, channelID, minutes, p.UserID).Scan(
			&out.ChannelID, &out.RequestedMinutes, &out.StartedAt, &out.ExpiresAt); err != nil {
			return err
		}
		out.RemainingMS = time.Until(out.ExpiresAt).Milliseconds()
		return audit.Append(ctx, tx, audit.Entry{
			ActorID:  p.UserID,
			Action:   "recording.manual_started",
			ObjectID: channelID,
			Details:  mustJSON(map[string]any{"minutes": minutes, "expires_at": out.ExpiresAt.UTC()}),
		})
	})
	return out, err
}

// StopManual ends the active manual window early (REC-03: an operator may stop
// before the deadline). Stopping something that is not running is not an error:
// the desired end state is "not manually recording", and it already holds.
func (s *Service) StopManual(ctx context.Context, p auth.Principal, channelID id.ID) error {
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE recording_manual_windows
			SET ended_at=clock_timestamp(),end_reason='cancelled'
			WHERE channel_id=$1 AND ended_at IS NULL`, channelID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return audit.Append(ctx, tx, audit.Entry{
			ActorID:  p.UserID,
			Action:   "recording.manual_stopped",
			ObjectID: channelID,
		})
	})
}

// ActiveManual reports the channel's running window, if any. It returns
// ErrManualWindowMissing when none is open — the caller turns that into a 404,
// which is a different answer from "a window that happens to be empty".
func (s *Service) ActiveManual(ctx context.Context, p auth.Principal, channelID id.ID) (*ManualWindow, error) {
	var out *ManualWindow
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Playback, tx); err != nil {
			return err
		}
		window, err := activeManualTx(ctx, tx, channelID)
		if err != nil {
			return err
		}
		out = window
		return nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, ErrManualWindowMissing
	}
	return out, nil
}

// activeManualTx reads the running window inside a caller's transaction.
func activeManualTx(ctx context.Context, tx pgx.Tx, channelID id.ID) (*ManualWindow, error) {
	var out ManualWindow
	err := tx.QueryRow(ctx, `SELECT channel_id,requested_minutes,started_at,expires_at
		FROM recording_manual_windows
		WHERE channel_id=$1 AND ended_at IS NULL AND expires_at>clock_timestamp()`,
		channelID).Scan(&out.ChannelID, &out.RequestedMinutes, &out.StartedAt, &out.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrManualWindowMissing
	}
	if err != nil {
		return nil, err
	}
	out.RemainingMS = time.Until(out.ExpiresAt).Milliseconds()
	return &out, nil
}

// manualActive reports whether the channel is inside a manual window, for the
// scheduler's union. The scheduler runs on every channel tick, so this is a
// single indexed lookup rather than a list.
func manualActiveTx(ctx context.Context, tx pgx.Tx, channelID id.ID) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recording_manual_windows
		WHERE channel_id=$1 AND ended_at IS NULL AND expires_at>clock_timestamp())`,
		channelID).Scan(&active)
	return active, err
}

// manualActive is manualActiveTx without a transaction, for callers that are not
// already inside one (the policy-change checks).
func (s *Service) manualActive(ctx context.Context, channelID id.ID) (bool, error) {
	var active bool
	err := s.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM recording_manual_windows
		WHERE channel_id=$1 AND ended_at IS NULL AND expires_at>clock_timestamp())`,
		channelID).Scan(&active)
	return active, err
}

// SweepManualWindows closes windows whose deadline has passed.
//
// The scheduler already ignores expired windows, so this is bookkeeping rather
// than correctness — but "ended_at" is what the history reads, and a window that
// expired without being closed would show as running forever in the audit trail.
func (s *Service) SweepManualWindows(ctx context.Context) {
	if s.DB == nil {
		return
	}
	for {
		tag, err := s.DB.Pool.Exec(ctx, `UPDATE recording_manual_windows
			SET ended_at=expires_at,end_reason='expired'
			WHERE ended_at IS NULL AND expires_at<=clock_timestamp()`)
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("manual recording sweep unavailable", "error", err)
			}
			return
		}
		// One statement closes every expired window; there is no backlog to
		// drain in batches because the set is bounded by the channel count.
		if tag.RowsAffected() == 0 || ctx.Err() != nil {
			return
		}
	}
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}
