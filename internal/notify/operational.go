package notify

// Operational notifications: telling someone the system is broken.
//
// This is a second producer beside detection hits, and it exists because the
// product could notice a camera was offline and had no way to say so. The
// message layer, the adapters and the delivery queue are all reused; what is new
// is where the facts come from (abnormal_events, not a rule hit) and the rule for
// how often (start and recovery only, never per sweep).
//
// The "start and recovery only" promise is kept in the database: abnormal_events
// already holds exactly one firing row per (kind, subject), and the announcement
// columns record that its opening and its closing were told. A sweep that re-reads
// the same firing row every ten seconds therefore cannot send twice, and a worker
// restart cannot re-send an alert the operator already received.

import (
	"context"
	"encoding/json"
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

// OperationalMessage is one fault, rendered for a delivery adapter.
//
// It deliberately reuses Message rather than introducing a second wire shape: a
// receiver that already handles a detection alert should not have to learn a
// second document. The fields that only make sense for a detection (category,
// score, zone, snapshot) are left empty, and the title/body carry the fault.
type OperationalFault struct {
	EventID     id.ID
	Kind        string
	Severity    string
	Subject     string
	ChannelID   *id.ID
	Summary     string
	Detail      string
	StartedAt   time.Time
	EndedAt     *time.Time
	Occurrences int
	Resolved    bool
}

// OperationalKinds lists the fault kinds an operator can subscribe to, in the
// order the UI offers them: what loses footage first, then what degrades the
// system, then what is merely informational.
func OperationalKinds() []string {
	return []string{
		"camera_offline",
		"stream_degraded",
		"recording_stalled",
		"storage_unwritable",
		"capacity_low",
		"detector_unavailable",
		"broker_unavailable",
		"config_publish_failed",
		"queue_backlog",
	}
}

// Subscription is one (kind, destination, minimum severity) row.
type Subscription struct {
	ID          id.ID  `json:"id"`
	Kind        string `json:"kind"`
	ChannelID   id.ID  `json:"channel_id"`
	ChannelName string `json:"channel_name,omitempty"`
	ChannelKind string `json:"channel_kind,omitempty"`
	MinSeverity string `json:"min_severity"`
	Enabled     bool   `json:"enabled"`
}

// SubscriptionInput replaces the whole set for a kind.
type SubscriptionInput struct {
	Kind        string `json:"kind"`
	ChannelID   id.ID  `json:"channel_id"`
	MinSeverity string `json:"min_severity"`
}

// ListSubscriptions returns every operational subscription.
func (s *Service) ListSubscriptions(ctx context.Context, p auth.Principal) ([]Subscription, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT o.id,o.kind,o.channel_id,c.name,c.kind,o.min_severity,o.enabled
		FROM operational_notification_subscriptions o
		JOIN notification_channels c ON c.id=o.channel_id
		WHERE c.site_id=$1 ORDER BY o.kind,c.name`, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		var item Subscription
		if err := rows.Scan(&item.ID, &item.Kind, &item.ChannelID, &item.ChannelName, &item.ChannelKind, &item.MinSeverity, &item.Enabled); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SetSubscriptions replaces the destinations for one fault kind.
//
// Replacing rather than patching: the operator edits the list for a kind as a set
// ("camera offline goes to these two"), and a partial update would leave a
// destination nobody meant to keep.
func (s *Service) SetSubscriptions(ctx context.Context, p auth.Principal, kind string, in []SubscriptionInput) error {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return err
	}
	known := false
	for _, candidate := range OperationalKinds() {
		if candidate == kind {
			known = true
			break
		}
	}
	if !known {
		return faultInvalidKind(kind)
	}
	for _, item := range in {
		if item.MinSeverity != "critical" && item.MinSeverity != "warning" && item.MinSeverity != "info" {
			return faultInvalidSeverity(item.MinSeverity)
		}
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM operational_notification_subscriptions WHERE kind=$1
			AND channel_id IN (SELECT id FROM notification_channels WHERE site_id=$2)`, kind, s.siteID); err != nil {
			return err
		}
		for _, item := range in {
			// The destination must belong to this site: a subscription pointing at
			// another site's channel would deliver its faults there.
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_channels WHERE id=$1 AND site_id=$2)`,
				item.ChannelID, s.siteID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrChannelNotFound
			}
			subID, err := id.New()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO operational_notification_subscriptions(id,kind,channel_id,min_severity)
				VALUES($1,$2,$3,$4) ON CONFLICT (kind,channel_id) DO UPDATE SET min_severity=EXCLUDED.min_severity, enabled=true`,
				subID, kind, item.ChannelID, item.MinSeverity); err != nil {
				return err
			}
		}
		details, _ := json.Marshal(map[string]any{"kind": kind, "count": len(in)})
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "notification.operational_subscribed", Details: details})
	})
}

// DispatchOperational sends the faults that have not been announced yet.
//
// Two passes in one sweep: newly firing rows (the opening) and newly resolved
// rows (the recovery). Both are bounded, because a site coming back from a long
// outage can have a backlog and the first sweep must not send a thousand messages.
func (s *Service) DispatchOperational(ctx context.Context) {
	if s.DB == nil {
		return
	}
	s.dispatchFiring(ctx)
	s.dispatchResolved(ctx)
}

const operationalBatch = 50

func (s *Service) dispatchFiring(ctx context.Context) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,kind,severity,subject,channel_id,summary,detail,started_at,occurrences
		FROM abnormal_events
		WHERE state='firing' AND announced_at IS NULL
		ORDER BY started_at LIMIT $1`, operationalBatch)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("operational dispatch scan unavailable", "error", err)
		}
		return
	}
	var faults []OperationalFault
	for rows.Next() {
		var item OperationalFault
		if err := rows.Scan(&item.EventID, &item.Kind, &item.Severity, &item.Subject, &item.ChannelID,
			&item.Summary, &item.Detail, &item.StartedAt, &item.Occurrences); err != nil {
			rows.Close()
			return
		}
		faults = append(faults, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	for _, fault := range faults {
		if ctx.Err() != nil {
			return
		}
		s.deliverOperational(ctx, fault)
	}
}

func (s *Service) dispatchResolved(ctx context.Context) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,kind,severity,subject,channel_id,summary,detail,started_at,ended_at,occurrences
		FROM abnormal_events
		WHERE state='resolved' AND resolved_announced_at IS NULL
		ORDER BY ended_at LIMIT $1`, operationalBatch)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("operational resolution scan unavailable", "error", err)
		}
		return
	}
	var faults []OperationalFault
	for rows.Next() {
		var item OperationalFault
		if err := rows.Scan(&item.EventID, &item.Kind, &item.Severity, &item.Subject, &item.ChannelID,
			&item.Summary, &item.Detail, &item.StartedAt, &item.EndedAt, &item.Occurrences); err != nil {
			rows.Close()
			return
		}
		item.Resolved = true
		faults = append(faults, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return
	}
	for _, fault := range faults {
		if ctx.Err() != nil {
			return
		}
		s.deliverOperational(ctx, fault)
	}
}

// deliverOperational sends one fault to every subscribed destination and stamps
// the announcement column.
//
// The stamp happens even when nobody is subscribed: the alternative is to retry
// forever, and "no one asked to be told" is an answer, not a failure. It also
// means an operator who subscribes later does not receive a flood of old faults —
// they get the ones that happen from then on, which is what a subscription means.
func (s *Service) deliverOperational(ctx context.Context, fault OperationalFault) {
	subs, err := s.operationalSubscribers(ctx, fault)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("operational subscribers unavailable", "error", err, "kind", fault.Kind)
		}
		return
	}
	message := operationalMessage(fault)
	for _, sub := range subs {
		if ctx.Err() != nil {
			return
		}
		config, err := s.channelConfig(ctx, sub.ChannelID)
		if err != nil {
			slog.Warn("operational delivery config unavailable", "error", err, "channel", sub.ChannelID)
			continue
		}
		adapter, err := s.registry.Build(config)
		if err != nil {
			slog.Warn("operational delivery adapter unavailable", "error", err, "kind", config.Kind)
			continue
		}
		// Inline delivery rather than a queued job: a fault notice is small, and
		// the abnormal_events row is the durable record — the announcement column
		// is only set after every subscribed destination was attempted, so a
		// failure here is retried by the next sweep rather than lost.
		window, cancel := context.WithTimeout(ctx, DeliverTimeout)
		sendErr := adapter.Send(window, message)
		cancel()
		if sendErr != nil {
			slog.Warn("operational notification failed", "error", sendErr, "kind", fault.Kind, "channel", sub.ChannelID)
			return
		}
	}
	column := "announced_at"
	if fault.Resolved {
		column = "resolved_announced_at"
	}
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE abnormal_events SET `+column+`=clock_timestamp() WHERE id=$1 AND `+column+` IS NULL`, fault.EventID); err != nil {
		if ctx.Err() == nil {
			slog.Warn("operational announcement not recorded", "error", err)
		}
	}
}

// operationalSubscribers resolves the destinations for one fault: every enabled
// subscription for its kind whose minimum severity it meets.
func (s *Service) operationalSubscribers(ctx context.Context, fault OperationalFault) ([]Subscription, error) {
	// Severity ranks: critical > warning > info. A subscription at 'warning'
	// wants warning and critical; one at 'info' wants all three.
	rank := map[string]int{"critical": 3, "warning": 2, "info": 1}
	rows, err := s.DB.Pool.Query(ctx, `SELECT o.id,o.kind,o.channel_id,o.min_severity
		FROM operational_notification_subscriptions o
		JOIN notification_channels c ON c.id=o.channel_id AND c.enabled
		WHERE o.kind=$1 AND o.enabled AND c.site_id=$2
		ORDER BY o.channel_id`, fault.Kind, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subscription{}
	for rows.Next() {
		var item Subscription
		if err := rows.Scan(&item.ID, &item.Kind, &item.ChannelID, &item.MinSeverity); err != nil {
			return nil, err
		}
		if rank[fault.Severity] < rank[item.MinSeverity] {
			continue
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// operationalMessage renders a fault into the same Message shape a detection
// alert uses, so every adapter and every receiver handles one document.
func operationalMessage(fault OperationalFault) Message {
	title := fault.Summary
	if fault.Resolved {
		title = "已恢复 · " + fault.Summary
	}
	if fault.Severity != "" {
		// Operational severities are critical/warning/info, a different vocabulary
		// from a detection's low/medium/high. SeverityLabel knows the detection
		// words, so it would print the raw code here — an operator reading
		// "[critical]" in Chinese UI has to translate it themselves.
		title = "[" + operationalSeverityLabel(fault.Severity) + "] " + title
	}
	var body strings.Builder
	body.WriteString(title)
	body.WriteString(" · ")
	body.WriteString(fault.Detail)
	if fault.Occurrences > 1 {
		fmt.Fprintf(&body, " · 反复 %d 次", fault.Occurrences)
	}
	if fault.Resolved && fault.EndedAt != nil {
		fmt.Fprintf(&body, " · 持续 %s", formatSpan(fault.EndedAt.Sub(fault.StartedAt)))
	}
	message := Message{
		DeliveryID:    operationalDeliveryID(fault),
		EventID:       fault.EventID,
		SiteName:      "系统",
		Category:      "operational",
		CategoryLabel: "运维",
		Severity:      fault.Severity,
		StartedAt:     fault.StartedAt.UTC(),
		StartedLocal:  fault.StartedAt.Format("2006-01-02 15:04:05"),
		Title:         title,
		Body:          body.String(),
	}
	if fault.ChannelID != nil {
		message.ChannelID = *fault.ChannelID
	}
	if fault.EndedAt != nil {
		end := fault.EndedAt.UTC()
		message.EndedAt = &end
		message.EndedLocal = fault.EndedAt.Format("2006-01-02 15:04:05")
	}
	return message
}

// operationalDeliveryID is stable per fault and per phase, so a receiver that
// deduplicates sees one "start" and one "recovered" for the same incident.
func operationalDeliveryID(fault OperationalFault) id.ID {
	phase := "open"
	if fault.Resolved {
		phase = "resolved"
	}
	return id.ID(string(fault.EventID) + ":" + phase)
}

func formatSpan(span time.Duration) string {
	if span < time.Minute {
		return fmt.Sprintf("%d 秒", int(span.Seconds()))
	}
	if span < time.Hour {
		return fmt.Sprintf("%d 分钟", int(span.Minutes()))
	}
	hours := int(span.Hours())
	minutes := int(span.Minutes()) - hours*60
	if minutes == 0 {
		return fmt.Sprintf("%d 小时", hours)
	}
	return fmt.Sprintf("%d 小时 %d 分钟", hours, minutes)
}

// faultInvalidKind and faultInvalidSeverity name what is wrong, the same way the
// channel validator does: "配置不完整" over a long form is not actionable.
func faultInvalidKind(kind string) error {
	return fault.New(422, "operational_kind_unknown", "未知的故障类型："+kind)
}

func faultInvalidSeverity(severity string) error {
	return fault.New(422, "operational_severity_unknown", "未知的严重级别："+severity)
}

// operationalSeverityLabel is the operator's word for a fault severity.
func operationalSeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "严重"
	case "warning":
		return "警告"
	case "info":
		return "提示"
	}
	return severity
}
