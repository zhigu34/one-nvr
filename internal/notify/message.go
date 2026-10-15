// Package notify turns a rule hit into a message and hands that message to
// whatever channels the rule is bound to.
//
// The split is deliberate: this file knows *what happened* and nothing about
// how anyone is told. A channel adapter receives a finished Message and does
// only the carrying — POST it, mail it, paste it into a chat webhook. Adding a
// channel therefore adds an adapter and never touches the facts below, which is
// what keeps a delivery mechanism from becoming part of the event model.
package notify

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

// Message is one notification's frozen content. Every field is a fact observed
// when the hit was raised, so a later rename of a channel, a rule or a zone
// cannot rewrite what was already told — the same reasoning that froze
// event_rule_hits.reason.
//
// Channel adapters must treat this as read-only input. A receiver that needs a
// different shape renders it from these fields; it does not ask this layer to
// grow a field for its own formatting.
type Message struct {
	// DeliveryID is minted once per notification and carried unchanged through
	// every retry, so a receiver can deduplicate (EVT-05). It is also the
	// signature's nonce: the same body with the same id verifies the same way.
	DeliveryID id.ID `json:"delivery_id"`

	EventID  id.ID  `json:"event_id"`
	SiteName string `json:"site_name"`
	// ChannelID is the stable channel identity; ChannelNo is what an operator
	// reads in the message. Both travel: the link needs the former, the text
	// needs the latter.
	ChannelID   id.ID  `json:"channel_id"`
	ChannelNo   int    `json:"channel_no"`
	ChannelName string `json:"channel_name"`

	// Category is the detector's label (person, car, ...); CategoryLabel is the
	// site's word for it, because "person" is not what an operator reads.
	Category      string  `json:"category"`
	CategoryLabel string  `json:"category_label"`
	Score         float64 `json:"score"`

	// Zone is the zone that qualified the hit, empty for a rule that does not
	// name one.
	Zone     string `json:"zone,omitempty"`
	RuleID   id.ID  `json:"rule_id"`
	RuleName string `json:"rule_name"`
	Severity string `json:"severity"`

	// Times are carried twice on purpose: RFC3339 in UTC for a machine, and the
	// site-local rendering for a person. A receiver that formats its own
	// timestamps uses the first and must not re-derive the site's zone.
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	StartedLocal string     `json:"started_local"`
	EndedLocal   string     `json:"ended_local,omitempty"`
	Timezone     string     `json:"timezone"`

	// SnapshotURL and ReplayURL are absolute paths on this site. A receiver may
	// fetch or link them; both honour the same authorization as the UI, so a
	// link is not a bypass.
	SnapshotURL string `json:"snapshot_url,omitempty"`
	ReplayURL   string `json:"replay_url,omitempty"`

	// Title and Body are the renderable summary, for channels that cannot lay
	// out fields themselves (a mail subject, a chat message). They are derived
	// from the facts above, never the other way round.
	Title string `json:"title"`
	Body  string `json:"body"`
}

// categoryLabel maps a detector label to the word the UI uses. Kept in step
// with the events page's labels; an unknown label falls back to itself rather
// than disappearing.
var categoryLabels = map[string]string{
	"person":     "人",
	"car":        "车",
	"truck":      "卡车",
	"bus":        "巴士",
	"motorcycle": "摩托车",
	"bicycle":    "自行车",
	"dog":        "狗",
	"cat":        "猫",
}

var severityLabels = map[string]string{"low": "低", "medium": "中", "high": "高"}

// CategoryLabel returns the operator-facing word for a detector label.
func CategoryLabel(category string) string {
	if label, ok := categoryLabels[category]; ok {
		return label
	}
	return category
}

// SeverityLabel returns the operator-facing word for a severity.
func SeverityLabel(severity string) string {
	if label, ok := severityLabels[severity]; ok {
		return label
	}
	return severity
}

// MessageInput carries the facts a hit already holds. The caller assembles it
// from the event, the matched rule and the site; this layer does not read the
// database itself, so the message can be unit tested without one.
type MessageInput struct {
	EventID     id.ID
	ChannelID   id.ID
	ChannelNo   int
	ChannelName string
	SiteName    string
	Category    string
	Score       float64
	Zone        string
	RuleID      id.ID
	RuleName    string
	Severity    string
	StartedAt   time.Time
	EndedAt     *time.Time
	// Location is the site's timezone; nil means the local renderings are
	// omitted rather than shown in the wrong zone.
	Location *time.Location
	// PublicURL is the site's external address. Links become absolute when it
	// is set, because a receiver that is not this browser (a chat client, a
	// mail body) cannot resolve a bare path.
	PublicURL string
	// SnapshotAvailable reports whether a picture can actually be served for
	// this event right now. A link to a missing picture is worse than no link.
	SnapshotAvailable bool
}

// buildMessage freezes one hit into a Message. It is a pure function so the
// content can be asserted directly, without a database or a channel.
func buildMessage(in MessageInput, deliveryID id.ID) (Message, error) {
	if in.EventID == "" || in.RuleID == "" || in.ChannelNo < 1 {
		return Message{}, fmt.Errorf("notification message needs an event, a rule and a channel number")
	}
	if in.StartedAt.IsZero() {
		return Message{}, fmt.Errorf("notification message needs a start time")
	}
	m := Message{
		DeliveryID:    deliveryID,
		EventID:       in.EventID,
		SiteName:      in.SiteName,
		ChannelID:     in.ChannelID,
		ChannelNo:     in.ChannelNo,
		ChannelName:   in.ChannelName,
		Category:      in.Category,
		CategoryLabel: CategoryLabel(in.Category),
		Score:         in.Score,
		Zone:          in.Zone,
		RuleID:        in.RuleID,
		RuleName:      in.RuleName,
		Severity:      in.Severity,
		StartedAt:     in.StartedAt.UTC(),
		SnapshotURL:   "/api/v1/events/" + string(in.EventID) + "/snapshot",
	}
	// The replay link points ten seconds before the event, matching the events
	// page: the operator wants the run-up to what was flagged, not the frame.
	// `channel` is the channel's identifier, not its number — the recordings
	// page resolves it against the channel list.
	replayAt := in.StartedAt.Add(-10 * time.Second)
	if in.ChannelID != "" {
		m.ReplayURL = "/recordings?channel=" + string(in.ChannelID) + "&at=" + replayAt.UTC().Format(time.RFC3339)
	}
	if !in.SnapshotAvailable {
		m.SnapshotURL = ""
	}
	// Absolute when the site knows its own address: a link that only works
	// inside the site's own browser is not usable by a chat or mail receiver.
	if base := strings.TrimRight(in.PublicURL, "/"); base != "" {
		if m.SnapshotURL != "" {
			m.SnapshotURL = base + m.SnapshotURL
		}
		if m.ReplayURL != "" {
			m.ReplayURL = base + m.ReplayURL
		}
	}
	if in.EndedAt != nil {
		end := in.EndedAt.UTC()
		m.EndedAt = &end
	}
	if in.Location != nil {
		m.Timezone = in.Location.String()
		m.StartedLocal = in.StartedAt.In(in.Location).Format("2006-01-02 15:04:05")
		if in.EndedAt != nil {
			m.EndedLocal = in.EndedAt.In(in.Location).Format("2006-01-02 15:04:05")
		}
	}
	m.Title, m.Body = render(m)
	return m, nil
}

// render derives the operator-facing summary. An ongoing event says so instead
// of borrowing the current time as an end, which would fabricate a fact.
func render(m Message) (string, string) {
	channel := fmt.Sprintf("CH%02d", m.ChannelNo)
	if m.ChannelName != "" {
		channel += " " + m.ChannelName
	}
	title := fmt.Sprintf("%s %s %s", channel, m.CategoryLabel, SeverityLabel(m.Severity))
	parts := []string{title}
	if m.Score > 0 {
		parts = append(parts, fmt.Sprintf("置信 %.0f%%", m.Score*100))
	}
	if m.Zone != "" {
		parts = append(parts, "区域 "+m.Zone)
	}
	if m.RuleName != "" {
		parts = append(parts, "规则 "+m.RuleName)
	}
	if m.StartedLocal != "" {
		when := m.StartedLocal
		if m.EndedLocal != "" {
			when += " → " + m.EndedLocal
		} else {
			when += "（进行中）"
		}
		parts = append(parts, when)
	} else {
		parts = append(parts, m.StartedAt.Format(time.RFC3339))
	}
	// The site name is what tells one installation's alert from another in a
	// channel that aggregates several; a receiver inside one site may ignore it.
	if m.SiteName != "" {
		title = m.SiteName + " · " + title
	}
	return title, strings.Join(parts, " · ")
}

// MarshalJSON is provided so the stored form is exactly the wire form: a
// receiver reading the JSON column and a receiver reading a webhook body see
// the same document, with no second serializer to drift.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	return json.Marshal(plain(m))
}
