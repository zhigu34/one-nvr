package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// ErrScheduleRequired and ErrScheduleFormat are their own faults, not the
// generic "参数无效": a scheduled policy without a time is the one validation
// failure an operator hits by clicking save a moment before filling the box, and
// that message does not say which field is missing.
var (
	ErrScheduleRequired = fault.New(422, "archive_schedule_required",
		"定时上传需要在「时刻」填写具体时间，例如 02:00 或 02:00,14:30")
	ErrScheduleFormat = fault.New(422, "archive_schedule_invalid",
		"「时刻」格式应为 HH:MM，多个时刻用逗号分隔，例如 02:00,14:30")
)

// Archive modes (ARC-08). manual is the zero policy: uploads happen only when
// someone selects segments; scheduled fires at fixed site-zone times; immediate
// enqueues each segment inside its publication transaction.
const (
	ModeManual    = "manual"
	ModeScheduled = "scheduled"
	ModeImmediate = "immediate"
)

// sweepBatch bounds one scheduled trigger. ARC-08: one trigger only enqueues;
// a backlog keeps draining through the ordinary job retries afterwards.
const sweepBatch = 100

// digits reports whether s is all ASCII digits, so "ab:cd" cannot pass as a time.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Policy is one channel's archive binding.
type Policy struct {
	ChannelID id.ID  `json:"channel_id"`
	TargetID  id.ID  `json:"target_id"`
	Mode      string `json:"mode"`
	Schedule  string `json:"schedule"`
}

// ListPolicies returns every configured binding (admin only, like targets).
func (s *Service) ListPolicies(ctx context.Context, p auth.Principal) ([]Policy, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT channel_id,target_id,mode,schedule FROM archive_policies ORDER BY channel_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Policy, 0)
	for rows.Next() {
		var item Policy
		if err := rows.Scan(&item.ChannelID, &item.TargetID, &item.Mode, &item.Schedule); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// UpsertPolicy binds one channel to one target and mode. Deleting the binding
// (mode manual with an empty schedule) returns the channel to batch-1
// behaviour; submitted tasks are never cancelled by it (ARC-08).
func (s *Service) UpsertPolicy(ctx context.Context, p auth.Principal, in Policy) (Policy, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return Policy{}, err
	}
	if in.Mode != ModeManual && in.Mode != ModeScheduled && in.Mode != ModeImmediate {
		return Policy{}, auth.ErrInvalid
	}
	if in.Mode == ModeScheduled {
		if strings.TrimSpace(in.Schedule) == "" {
			return Policy{}, ErrScheduleRequired
		}
		for _, fire := range strings.Split(in.Schedule, ",") {
			// HH:MM, and neither half may be blank: "2:00" and "02:0" are what a
			// hand-typed time looks like before it is finished.
			if len(fire) != 5 || fire[2] != ':' || !digits(fire[:2]) || !digits(fire[3:]) {
				return Policy{}, ErrScheduleFormat
			}
		}
	} else if in.Schedule != "" {
		return Policy{}, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM channels WHERE id=$1)`, in.ChannelID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return auth.ErrNotFound
		}
		if in.Mode == ModeManual {
			// A manual binding still names a target so the upload dialog can
			// default to it; the row exists but nothing auto-enqueues.
			if in.TargetID == "" {
				_, err := tx.Exec(ctx, `DELETE FROM archive_policies WHERE channel_id=$1`, in.ChannelID)
				if err != nil {
					return err
				}
				return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "archive.policy_removed", ObjectID: in.ChannelID})
			}
		}
		var targetExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM archive_targets WHERE id=$1)`, in.TargetID).Scan(&targetExists); err != nil {
			return err
		}
		if !targetExists {
			return ErrNoEnabledTarget
		}
		if _, err := tx.Exec(ctx, `INSERT INTO archive_policies(channel_id,target_id,mode,schedule) VALUES($1,$2,$3,$4)
			ON CONFLICT (channel_id) DO UPDATE SET target_id=$2,mode=$3,schedule=$4,updated_at=now()`, in.ChannelID, in.TargetID, in.Mode, in.Schedule); err != nil {
			return err
		}
		detailsRaw, _ := json.Marshal(map[string]any{"mode": in.Mode, "target_id": in.TargetID, "schedule": in.Schedule})
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "archive.policy_updated", ObjectID: in.ChannelID, Details: detailsRaw})
	})
	if err != nil {
		return Policy{}, err
	}
	return in, nil
}

// EnqueueForPublication runs inside the publication transaction: an immediate
// policy pins its copy row together with the segment becoming ready, so a
// crash cannot leave a ready segment that was supposed to upload without its
// reference. Hook failures never fail the publication — the caller logs them.
func (s *Service) EnqueueForPublication(ctx context.Context, tx pgx.Tx, channelID, segmentID id.ID) error {
	var targetID, mode string
	err := tx.QueryRow(ctx, `SELECT p.target_id,p.mode FROM archive_policies p JOIN archive_targets t ON t.id=p.target_id WHERE p.channel_id=$1 AND t.enabled`, channelID).
		Scan(&targetID, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if mode != ModeImmediate {
		return nil
	}
	var channelNo int
	var target string
	var size int64
	if err := tx.QueryRow(ctx, `SELECT c.channel_no,s.target_relative_path,s.size_bytes FROM recording_segments s JOIN channels c ON c.id=s.channel_id WHERE s.id=$1 AND s.channel_id=$2 AND s.state='ready'`, segmentID, channelID).
		Scan(&channelNo, &target, &size); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err = s.enqueueUpload(ctx, tx, id.ID(targetID), segmentID, uploadSource{
		ChannelID: channelID, ChannelNo: channelNo, Target: target, State: "ready", Size: size,
	})
	return err
}

// SweepSchedules runs every worker tick: for each scheduled policy whose site
// clock has reached one of its fire times today and whose ledger row is free,
// claim the slot and enqueue every not-yet-archived ready segment. One trigger
// only enqueues; the job queue drains the backlog (ARC-08).
func (s *Service) SweepSchedules(ctx context.Context) {
	policies, err := s.DB.Pool.Query(ctx, `SELECT p.channel_id,p.target_id,p.schedule,s.timezone FROM archive_policies p
		JOIN archive_targets t ON t.id=p.target_id JOIN channels c ON c.id=p.channel_id JOIN sites s ON s.id=c.site_id
		WHERE p.mode='scheduled' AND t.enabled`)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("archive schedule scan unavailable")
		}
		return
	}
	type scheduled struct {
		ChannelID, TargetID id.ID
		Schedule            string
		Zone                string
	}
	var due []scheduled
	for policies.Next() {
		var item scheduled
		if err := policies.Scan(&item.ChannelID, &item.TargetID, &item.Schedule, &item.Zone); err != nil {
			policies.Close()
			return
		}
		due = append(due, item)
	}
	policies.Close()
	for _, item := range due {
		location, err := time.LoadLocation(item.Zone)
		if err != nil {
			continue
		}
		now := time.Now().In(location)
		fireDate := now.Format("2006-01-02")
		nowHM := now.Format("15:04")
		for _, fire := range strings.Split(item.Schedule, ",") {
			fire = strings.TrimSpace(fire)
			if len(fire) != 5 || fire > nowHM {
				continue
			}
			claimed, err := s.claimScheduleSlot(ctx, item.ChannelID, fireDate, fire)
			if err != nil || !claimed {
				continue
			}
			queued := s.enqueueDueSegments(ctx, item.ChannelID, item.TargetID)
			_, _ = s.DB.Pool.Exec(ctx, `UPDATE archive_schedule_runs SET queued=$2 WHERE policy_id=$1 AND fire_date=$3 AND fire_time=$4`,
				item.ChannelID, queued, fireDate, fire)
			slog.Info("archive schedule fired", "channel", string(item.ChannelID), "fire", fire, "queued", queued)
		}
	}
}

// claimScheduleSlot takes the (policy,date,time) ledger slot; the UNIQUE
// constraint is the multi-instance fence.
func (s *Service) claimScheduleSlot(ctx context.Context, channelID id.ID, fireDate, fireTime string) (bool, error) {
	token, err := id.New()
	if err != nil {
		return false, err
	}
	tag, err := s.DB.Pool.Exec(ctx, `INSERT INTO archive_schedule_runs(id,policy_id,fire_date,fire_time) VALUES($1,$2,$3,$4)
		ON CONFLICT (policy_id,fire_date,fire_time) DO NOTHING`, token, channelID, fireDate, fireTime)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// enqueueDueSegments enqueues every ready segment of the channel that has no
// copy row for the target yet, oldest first, bounded per trigger.
func (s *Service) enqueueDueSegments(ctx context.Context, channelID, targetID id.ID) int {
	queued := 0
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.id,c.channel_no,s.target_relative_path,s.size_bytes FROM recording_segments s JOIN channels c ON c.id=s.channel_id
			WHERE s.channel_id=$1 AND s.state='ready'
			AND NOT EXISTS(SELECT 1 FROM recording_archive_copies ac WHERE ac.segment_id=s.id AND ac.target_id=$2)
			ORDER BY s.start_at LIMIT $3`, channelID, targetID, sweepBatch)
		if err != nil {
			return fmt.Errorf("sweep query: %w", err)
		}
		defer rows.Close()
		// Buffer the candidates first: issuing queries on the same transaction
		// while these rows are open trips pgx's "conn busy" guard.
		type candidate struct {
			SegmentID id.ID
			ChannelNo int
			Target    string
			Size      int64
		}
		var candidates []candidate
		for rows.Next() {
			var item candidate
			if err := rows.Scan(&item.SegmentID, &item.ChannelNo, &item.Target, &item.Size); err != nil {
				return fmt.Errorf("sweep scan: %w", err)
			}
			candidates = append(candidates, item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("sweep rows: %w", err)
		}
		rows.Close()
		for _, item := range candidates {
			enqueued, err := s.enqueueUpload(ctx, tx, targetID, item.SegmentID, uploadSource{
				ChannelID: channelID, ChannelNo: item.ChannelNo, Target: item.Target, State: "ready", Size: item.Size,
			})
			if err != nil {
				return fmt.Errorf("sweep enqueue: %w", err)
			}
			if enqueued {
				queued++
			}
		}
		return nil
	})
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("archive scheduled enqueue unavailable", "error", err)
		}
		return queued
	}
	return queued
}
