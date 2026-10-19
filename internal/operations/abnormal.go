package operations

// PRD EVT-04: abnormal events.
//
// The product is expected to notice when something is broken without being asked.
// Every condition here was chosen because it has actually happened on this site:
// a detector that stopped pulling a stream and stayed dead for ten hours while
// every page looked normal; a media server whose config became unreadable; a pool
// that stopped accepting writes. In each case the only way to find out was to
// read container logs.
//
// Two rules shape the implementation:
//
//   - One open row per (kind, subject). A condition re-evaluated every ten seconds
//     must update its row, not append. EVT-04 states this directly, and the
//     partial unique index enforces it rather than trusting the writer.
//   - Resolution keeps the history. "It recovered" is information an operator
//     needs the next morning; deleting the row would erase the fact that anything
//     happened.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

// Abnormal event kinds (EVT-04).
const (
	KindCameraOffline     = "camera_offline"
	KindStreamDegraded    = "stream_degraded"
	KindRecordingStalled  = "recording_stalled"
	KindStorageUnwritable = "storage_unwritable"
	KindCapacityLow       = "capacity_low"
	KindDetectorDown      = "detector_unavailable"
	KindBrokerDown        = "broker_unavailable"
	KindConfigPublishFail = "config_publish_failed"
	KindQueueBacklog      = "queue_backlog"
)

// Severities, ordered. The worklist sorts critical first because a detector that
// is down blinds every camera at once, while one camera being offline is a
// smaller loss that the operator can triage in any order.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// AbnormalEvent is one row as the API returns it.
type AbnormalEvent struct {
	ID          id.ID      `json:"id"`
	Kind        string     `json:"kind"`
	Severity    string     `json:"severity"`
	Subject     string     `json:"subject"`
	ChannelID   *id.ID     `json:"channel_id"`
	ChannelNo   *int       `json:"channel_no"`
	ChannelName string     `json:"channel_name"`
	State       string     `json:"state"`
	Summary     string     `json:"summary"`
	Detail      string     `json:"detail"`
	Occurrences int        `json:"occurrences"`
	StartedAt   time.Time  `json:"started_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	EndedAt     *time.Time `json:"ended_at"`
}

// finding is one condition observed during a sweep.
type finding struct {
	Kind     string
	Severity string
	Subject  string
	Channel  *id.ID
	Summary  string
	Detail   string
}

// AbnormalSweep evaluates every signal and reconciles the incident table.
//
// One sweep rather than a loop per condition: several signals come from the same
// component probes, and evaluating them together means a condition that clears
// and another that starts are recorded against the same instant instead of two
// instants a few seconds apart.
func (s *Service) AbnormalSweep(ctx context.Context) {
	if s.DB == nil {
		return
	}
	findings, unknown, err := s.abnormalFindings(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("abnormal event sweep unavailable", "error", err)
		}
		return
	}
	if err := s.reconcileAbnormal(ctx, findings, unknown); err != nil && ctx.Err() == nil {
		slog.Warn("abnormal event reconciliation failed", "error", err)
	}
}

// reconcileAbnormal applies one sweep to the table.
//
// The write is per finding, inside one transaction, and deliberately does not
// delete anything: a condition that stopped being reported within this sweep is
// closed with an end time, and a condition that is simply absent from the sweep
// (because its window is longer, or its probe did not run) is left alone.
// Closing on absence would make one slow probe look like a recovery.
func (s *Service) reconcileAbnormal(ctx context.Context, findings []finding, unknown []string) error {
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		now := time.Now().UTC()
		seen := make(map[string]bool, len(findings))
		// A subject in `unknown` was still evaluated but could not be judged, so
		// its existing verdict stands. Without this the sweep would resolve the
		// incident the moment its probe went quiet.
		kept := make(map[string]bool, len(unknown))
		for _, subject := range unknown {
			kept[subject] = true
		}
		for _, item := range findings {
			key := item.Kind + "\x00" + item.Subject
			seen[key] = true
			if err := upsertAbnormal(ctx, tx, item, now); err != nil {
				return err
			}
		}
		// Conditions in the table that this sweep would have reported but did
		// not are resolved. Restricted to the kinds the sweep covers: a kind
		// produced by another path (a publish failure recorded at publish time)
		// must not be closed just because this sweep does not evaluate it.
		kinds := abnormalSweepKinds()
		rows, err := tx.Query(ctx, `SELECT id,kind,subject FROM abnormal_events WHERE state='firing' AND kind=ANY($1)`, kinds)
		if err != nil {
			return err
		}
		type open struct {
			id      id.ID
			kind    string
			subject string
		}
		var firing []open
		for rows.Next() {
			var item open
			if err := rows.Scan(&item.id, &item.kind, &item.subject); err != nil {
				rows.Close()
				return err
			}
			firing = append(firing, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range firing {
			if seen[item.kind+"\x00"+item.subject] {
				continue
			}
			// Component incidents are keyed by the component name; a component
			// whose probe is stale keeps its row.
			if kept[item.subject] {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE abnormal_events SET state='resolved',ended_at=$2,updated_at=clock_timestamp() WHERE id=$1 AND state='firing'`, item.id, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// upsertAbnormal writes one condition, reusing the open row when it exists.
//
// Occurrences increment only when the row is touched by a new sweep, not on every
// re-read: the number answers "how many times did we notice this", which is what
// tells an operator whether a condition is flapping.
func upsertAbnormal(ctx context.Context, tx pgx.Tx, item finding, now time.Time) error {
	// The channel column is left null when the condition is not about one camera;
	// attaching the site-wide ones to an arbitrary channel would put a detector
	// outage into every camera's history.
	eventID, err := id.New()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO abnormal_events(id,kind,severity,subject,channel_id,state,summary,detail,occurrences,started_at,last_seen_at)
		VALUES($1,$2,$3,$4,$5,'firing',$6,$7,1,$8,$8)
		ON CONFLICT(kind,subject) WHERE state='firing' DO UPDATE SET
			severity=EXCLUDED.severity,
			summary=EXCLUDED.summary,
			detail=EXCLUDED.detail,
			occurrences=abnormal_events.occurrences+1,
			last_seen_at=EXCLUDED.last_seen_at,
			updated_at=clock_timestamp()`,
		eventID, item.Kind, item.Severity, item.Subject, item.Channel, item.Summary, item.Detail, now)
	return err
}

// RecordPublishFailure records a failed detection publication (EVT-04).
//
// Called from the publish path rather than from a sweep: the failure is an
// instant, and a sweep would either miss a failure that was fixed before it ran
// or report one that never happened. The row is resolved by the next successful
// publication of the same channel set.
func (s *Service) RecordPublishFailure(ctx context.Context, detail string) {
	if s.DB == nil {
		return
	}
	item := finding{
		Kind:     KindConfigPublishFail,
		Severity: SeverityWarning,
		Subject:  "detection",
		Summary:  "检测配置发布失败",
		Detail:   detail,
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		return upsertAbnormal(ctx, tx, item, time.Now().UTC())
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("could not record publish failure", "error", err)
	}
}

// ClearPublishFailure closes the publish incident after a successful one.
func (s *Service) ClearPublishFailure(ctx context.Context) {
	if s.DB == nil {
		return
	}
	_, err := s.DB.Pool.Exec(ctx, `UPDATE abnormal_events SET state='resolved',ended_at=clock_timestamp(),updated_at=clock_timestamp()
		WHERE kind=$1 AND state='firing'`, KindConfigPublishFail)
	if err != nil && ctx.Err() == nil {
		slog.Warn("could not clear publish failure", "error", err)
	}
}

// ListAbnormal returns the worklist, firing first and most severe first.
func (s *Service) ListAbnormal(ctx context.Context, p auth.Principal, includeResolved bool) ([]AbnormalEvent, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	query := `SELECT a.id,a.kind,a.severity,a.subject,a.channel_id,a.state,a.summary,a.detail,a.occurrences,a.started_at,a.last_seen_at,a.ended_at,
			c.channel_no,c.channel_name
		FROM abnormal_events a LEFT JOIN channels c ON c.id=a.channel_id`
	if !includeResolved {
		query += ` WHERE a.state='firing'`
	}
	// Severity order is spelled out rather than alphabetical: 'critical' sorts
	// before 'warning' by luck, but 'info' would sort between them.
	query += ` ORDER BY (a.state='firing') DESC,
		CASE a.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
		a.last_seen_at DESC LIMIT 200`
	rows, err := s.DB.Pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AbnormalEvent, 0, 16)
	for rows.Next() {
		var item AbnormalEvent
		if err := rows.Scan(&item.ID, &item.Kind, &item.Severity, &item.Subject, &item.ChannelID, &item.State,
			&item.Summary, &item.Detail, &item.Occurrences, &item.StartedAt, &item.LastSeenAt, &item.EndedAt,
			&item.ChannelNo, &item.ChannelName); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SweepAbnormalRetention removes resolved incidents past their window.
//
// Open incidents are never removed however old they are: a condition that has
// been firing for a month is the most important row in the table, not stale data.
func (s *Service) SweepAbnormalRetention(ctx context.Context) {
	if s.DB == nil {
		return
	}
	_, err := s.DB.Pool.Exec(ctx, `DELETE FROM abnormal_events WHERE state='resolved' AND ended_at < clock_timestamp()-interval '180 days'`)
	if err != nil && ctx.Err() == nil {
		slog.Warn("abnormal event retention unavailable", "error", err)
	}
}

// abnormalSweepKinds lists what a sweep is responsible for closing. The publish
// failure is excluded because it is opened and closed by the publish path itself.
func abnormalSweepKinds() []string {
	return []string{
		KindCameraOffline, KindStreamDegraded, KindRecordingStalled,
		KindStorageUnwritable, KindCapacityLow,
		KindDetectorDown, KindBrokerDown, KindQueueBacklog,
	}
}

// errAbnormalUnavailable marks a sweep that could not read its inputs. Exported
// for tests that assert the sweep degrades rather than writing a false recovery.
var errAbnormalUnavailable = errors.New("abnormal signals unavailable")
