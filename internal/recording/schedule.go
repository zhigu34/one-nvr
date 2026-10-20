package recording

// PRD REC-02: weekly schedule storage and evaluation. The pure rule lives in
// schedule_rule.go; this file is the database side.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// ErrScheduleOverlap reports a window that collides with another on the same day.
var ErrScheduleOverlap = errors.New("schedule_overlap")

// ChannelSchedule is one channel's whole week, as the operator entered it.
type ChannelSchedule struct {
	ChannelID id.ID            `json:"channel_id"`
	Windows   []ScheduleWindow `json:"windows"`
	Timezone  string           `json:"timezone"`
	// Active is evaluated server-side so the UI and the recorder cannot disagree
	// about whether recording is owed at this instant.
	Active bool `json:"active"`
}

// siteZone loads the timezone of the site a channel belongs to. A schedule is
// meaningless without it, so a missing or invalid zone is an error rather than a
// silent UTC fallback — recording at the wrong wall clock is worse than not
// recording.
//
// It reads the zone through the channel rather than from s.siteID because the
// API constructs a bare recording.Service (no site id) for the read endpoints:
// going through the channel keeps one implementation for both processes instead
// of a second lookup shape that only the API needs.
func (s *Service) siteZone(ctx context.Context, channelID id.ID) (*time.Location, string, error) {
	var name string
	if err := s.DB.Pool.QueryRow(ctx,
		"SELECT s.timezone FROM channels c JOIN sites s ON s.id=c.site_id WHERE c.id=$1",
		channelID).Scan(&name); err != nil {
		return nil, "", err
	}
	zone, err := time.LoadLocation(name)
	if err != nil {
		return nil, name, err
	}
	return zone, name, nil
}

// GetSchedule returns one channel's week in operator form.
func (s *Service) GetSchedule(ctx context.Context, p auth.Principal, channelID id.ID) (ChannelSchedule, error) {
	rows, err := s.scheduleRows(ctx, channelID, p)
	if err != nil {
		return ChannelSchedule{}, err
	}
	_, zoneName, err := s.siteZone(ctx, channelID)
	if err != nil {
		return ChannelSchedule{}, err
	}
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return ChannelSchedule{}, err
	}
	return ChannelSchedule{
		ChannelID: channelID,
		// Only include windows that are actually active. A channel with no rows
		// is "never scheduled", which is a different thing from "scheduled
		// nowhere" and must not be shown as an empty editor with a saved state.
		Windows:  MergeOvernightWindows(rows),
		Timezone: zoneName,
		Active:   ScheduleActiveAt(rows, zone, time.Now()),
	}, nil
}

// scheduleRows reads the stored rows without merging, which is what evaluation
// needs: merging is a display concern and would make the tail belong to the
// wrong weekday if fed back into the evaluator.
func (s *Service) scheduleRows(ctx context.Context, channelID id.ID, p auth.Principal) ([]ScheduleWindow, error) {
	var out []ScheduleWindow
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Playback, tx); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT weekday,start_minute,end_minute FROM recording_schedules
			WHERE channel_id=$1 ORDER BY weekday,start_minute`, channelID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w ScheduleWindow
			if err := rows.Scan(&w.Weekday, &w.StartMinute, &w.EndMinute); err != nil {
				return err
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// ScheduleActiveForChannel is the scheduler's read: is this channel inside a
// scheduled window right now? It deliberately takes no principal — it runs in
// the worker, whose authority is the site, not a user.
func (s *Service) ScheduleActiveForChannel(ctx context.Context, channelID id.ID) (bool, error) {
	zone, _, err := s.siteZone(ctx, channelID)
	if err != nil {
		return false, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT weekday,start_minute,end_minute FROM recording_schedules WHERE channel_id=$1`, channelID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	windows := make([]ScheduleWindow, 0, 8)
	for rows.Next() {
		var w ScheduleWindow
		if err := rows.Scan(&w.Weekday, &w.StartMinute, &w.EndMinute); err != nil {
			return false, err
		}
		windows = append(windows, w)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	// No rows means never scheduled. Asking the evaluator with an empty list
	// would answer "not active", which is the same answer — but skipping the
	// call keeps the common case for unscheduled channels off the hot path.
	if len(windows) == 0 {
		return false, nil
	}
	return ScheduleActiveAt(windows, zone, time.Now()), nil
}

// scheduleActiveTx is the transaction-scoped variant used where the policy and
// the schedule must be read at the same instant.
func (s *Service) scheduleActiveTx(ctx context.Context, tx pgx.Tx, channelID id.ID, zone *time.Location, now time.Time) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT weekday,start_minute,end_minute FROM recording_schedules WHERE channel_id=$1`, channelID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	windows := make([]ScheduleWindow, 0, 8)
	for rows.Next() {
		var w ScheduleWindow
		if err := rows.Scan(&w.Weekday, &w.StartMinute, &w.EndMinute); err != nil {
			return false, err
		}
		windows = append(windows, w)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(windows) == 0 {
		return false, nil
	}
	return ScheduleActiveAt(windows, zone, now), nil
}

// PutSchedule replaces one channel's whole week.
//
// Replacement rather than per-window edits: the operator's mental model is "this
// is my week", and a partial update would leave windows from an older intent
// behind with no way to see them. The whole week is validated and written in one
// transaction, so a rejected week changes nothing.
// PutSchedule replaces a channel's weekly schedule.
//
// expectedVersion is the channel version the editor read. Every other channel
// write in this codebase is guarded the same way; this one was not, so two
// operators editing the schedule and the mode at the same time silently
// overwrote each other — the schedule is a full replace, so the loser's whole
// week disappeared rather than one field.
func (s *Service) PutSchedule(ctx context.Context, p auth.Principal, channelID id.ID, windows []ScheduleWindow, expectedVersion int64) (ChannelSchedule, error) {
	// Normalise first: the editor may send a window that wraps past midnight
	// (end > 1440), and storage keeps only single-day rows.
	stored := make([]ScheduleWindow, 0, len(windows))
	// Which group each stored row belongs to. A window that crosses midnight is
	// stored as one row per local day, but it is ONE window to the operator — the
	// editor draws it as a single band. Giving each stored row its own group (the
	// previous behaviour) made the UI read a 22:00-02:00 window as two unrelated
	// slots, and the round trip through the editor split it in two.
	groups := make([]int, 0, len(windows))
	groupCount := 0
	for _, w := range windows {
		if w.EndMinute > 1440 {
			if w.EndMinute-1440 >= 1440 {
				// Wrapping more than a full day is not a window, it is a typo.
				return ChannelSchedule{}, auth.ErrInvalid
			}
			if err := (ScheduleWindow{Weekday: w.Weekday, StartMinute: w.StartMinute, EndMinute: 1440}).Validate(); err != nil {
				return ChannelSchedule{}, auth.ErrInvalid
			}
		} else if err := w.Validate(); err != nil {
			return ChannelSchedule{}, auth.ErrInvalid
		}
		for range SplitOvernight(w) {
			groups = append(groups, groupCount)
		}
		groupCount++
		stored = append(stored, SplitOvernight(w)...)
	}
	// Overlap is rejected here rather than by a constraint: gist exclusion over
	// these columns needs btree_gist, and a migration that installs an extension
	// requires privileges the application role does not own.
	for i := range stored {
		for j := i + 1; j < len(stored); j++ {
			if ScheduleWindowsOverlap(stored[i], stored[j]) {
				return ChannelSchedule{}, ErrScheduleOverlap
			}
		}
	}
	var out ChannelSchedule
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		// The row lock is what makes the comparison meaningful: without it a
		// concurrent writer could commit between the read and the replace.
		var current int64
		if err := tx.QueryRow(ctx, "SELECT version FROM channels WHERE id=$1 FOR UPDATE", channelID).Scan(&current); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		if current != expectedVersion {
			return auth.ErrConflict
		}
		if _, err := tx.Exec(ctx, "DELETE FROM recording_schedules WHERE channel_id=$1", channelID); err != nil {
			return err
		}
		// One window_id per ORIGINAL window, shared by every stored row it became.
		windowIDs := make([]id.ID, groupCount)
		for gi := range windowIDs {
			created, err := id.New()
			if err != nil {
				return err
			}
			windowIDs[gi] = created
		}
		for i, w := range stored {
			rowID, err := id.New()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO recording_schedules(id,channel_id,weekday,start_minute,end_minute) VALUES($1,$2,$3,$4,$5)`,
				rowID, channelID, w.Weekday, w.StartMinute, w.EndMinute); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO recording_schedule_groups(window_id,schedule_id) VALUES($1,$2)`, windowIDs[groups[i]], rowID); err != nil {
				return err
			}
		}
		// Advance the token. Without this the guard compares against a value that
		// never moves, so every write succeeds and the lock buys nothing.
		if _, err := tx.Exec(ctx, "UPDATE channels SET version=version+1 WHERE id=$1", channelID); err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{
			ActorID:  p.UserID,
			Action:   "recording.schedule_updated",
			ObjectID: channelID,
			Details:  mustJSON(map[string]any{"windows": len(stored), "weekdays": WeekdaysOf(stored)}),
		})
	})
	if err != nil {
		return ChannelSchedule{}, err
	}
	out = ChannelSchedule{ChannelID: channelID, Windows: MergeOvernightWindows(stored)}
	if zone, zoneName, err := s.siteZone(ctx, channelID); err == nil {
		out.Timezone = zoneName
		out.Active = ScheduleActiveAt(stored, zone, time.Now())
	}
	return out, nil
}

// WeekdaysOf lists the distinct weekdays a schedule touches, for the audit
// entry. It is a summary, not the schedule itself: the audit trail should show
// that a week changed and which days it covers without becoming a second copy of
// the schedule.
func WeekdaysOf(windows []ScheduleWindow) []int {
	seen := map[int]bool{}
	out := make([]int, 0, 7)
	for _, w := range windows {
		if !seen[w.Weekday] {
			seen[w.Weekday] = true
			out = append(out, w.Weekday)
		}
	}
	return out
}

// modeRecordsNow reports whether a stored policy mode wants recording at this
// instant. It is the single place the switch/policy path asks "should this
// channel be recording right now", so 'planned' cannot be treated as continuous
// there while the scheduler treats it as conditional.
// modeRecordsNow reports whether the channel should be recording right now.
//
// It goes through unionMode, the same helper the scheduler and the obligation
// layer use, so the three cannot disagree. The previous version answered from the
// stored policy alone: a `manual` case that could never be reached (the column
// has no such value) and no knowledge of an active manual window. Applying a
// policy change to a channel that was recording manually therefore decided
// "nothing should be recording", and the switch's stopping phase stopped a
// recording REC-04 says must survive.
func (s *Service) modeRecordsNow(ctx context.Context, channelID id.ID, mode string) (bool, error) {
	manual, err := s.manualActive(ctx, channelID)
	if err != nil {
		return false, err
	}
	scheduled := false
	if mode == "planned" {
		// Only a planned policy consults the schedule; asking for other modes is a
		// wasted query on every policy write.
		if scheduled, err = s.ScheduleActiveForChannel(ctx, channelID); err != nil {
			return false, err
		}
	}
	return unionMode(mode, manual, scheduled) != "none", nil
}
