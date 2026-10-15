package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
)

func fixedID(t *testing.T, seed string) id.ID {
	t.Helper()
	// Deterministic ids keep the assertions about links readable; the values
	// only need to be well-formed UUIDs.
	switch seed {
	case "event":
		return "11111111-1111-4111-8111-111111111111"
	case "rule":
		return "22222222-2222-4222-8222-222222222222"
	case "channel":
		return "33333333-3333-4333-8333-333333333333"
	case "delivery":
		return "44444444-4444-4444-8444-444444444444"
	}
	t.Fatalf("unknown seed %q", seed)
	return ""
}

func shanghai(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func baseInput(t *testing.T) MessageInput {
	t.Helper()
	started := time.Date(2026, 10, 15, 1, 30, 0, 0, time.UTC)
	return MessageInput{
		EventID:     fixedID(t, "event"),
		ChannelID:   fixedID(t, "channel"),
		ChannelNo:   3,
		ChannelName: "北门",
		SiteName:    "办公楼",
		Category:    "person",
		Score:       0.93,
		Zone:        "门口",
		RuleID:      fixedID(t, "rule"),
		RuleName:    "非工作时间有人",
		Severity:    "high",
		StartedAt:   started,
		Location:    shanghai(t),
		PublicURL:   "https://nvr.example.com",
	}
}

func TestMessageCarriesFrozenFacts(t *testing.T) {
	in := baseInput(t)
	in.SnapshotAvailable = true
	m, err := buildMessage(in, fixedID(t, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	if m.DeliveryID != fixedID(t, "delivery") {
		t.Fatalf("delivery id must be the one minted for this notification, got %v", m.DeliveryID)
	}
	// The operator reads a channel number and the site's word for the category,
	// not a UUID and not "person".
	if m.ChannelNo != 3 || m.CategoryLabel != "人" || m.Severity == "" {
		t.Fatalf("message lost its operator-facing facts: %+v", m)
	}
	if m.StartedLocal != "2026-10-15 09:30:00" {
		t.Fatalf("start must be rendered in the site's zone, got %q", m.StartedLocal)
	}
	// A machine reading the message gets the canonical instant too.
	if !m.StartedAt.Equal(in.StartedAt) {
		t.Fatalf("start must stay the canonical UTC instant, got %v", m.StartedAt)
	}
	if !strings.Contains(m.Title, "CH03") || !strings.Contains(m.Title, "北门") {
		t.Fatalf("title must name the channel plainly, got %q", m.Title)
	}
	if !strings.Contains(m.Title, "办公楼") {
		t.Fatalf("title must carry the site name so aggregated channels stay distinguishable, got %q", m.Title)
	}
	if !strings.Contains(m.Body, "规则 非工作时间有人") || !strings.Contains(m.Body, "置信 93%") {
		t.Fatalf("body must state why this was told, got %q", m.Body)
	}
	// Links are absolute: a chat or mail receiver cannot resolve a bare path.
	if !strings.HasPrefix(m.SnapshotURL, "https://nvr.example.com/") || !strings.HasPrefix(m.ReplayURL, "https://nvr.example.com/") {
		t.Fatalf("links must be absolute when the site knows its address: %q %q", m.SnapshotURL, m.ReplayURL)
	}
	// The replay link uses the channel's identity, not its number: the
	// recordings page resolves it against the channel list.
	if !strings.Contains(m.ReplayURL, string(in.ChannelID)) {
		t.Fatalf("replay link must carry the channel id, got %q", m.ReplayURL)
	}
	if !strings.Contains(m.ReplayURL, "at=") {
		t.Fatalf("replay link must carry the moment, got %q", m.ReplayURL)
	}
}

func TestOngoingEventDoesNotFabricateAnEnd(t *testing.T) {
	in := baseInput(t)
	in.SnapshotAvailable = true
	m, err := buildMessage(in, fixedID(t, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	if m.EndedAt != nil || m.EndedLocal != "" {
		t.Fatalf("an event that has not ended must not carry an end: %v %q", m.EndedAt, m.EndedLocal)
	}
	// Saying so is required: silence would read as "it ended and we did not say
	// when", and using the current time would invent a fact.
	if !strings.Contains(m.Body, "进行中") {
		t.Fatalf("an ongoing event must say it is ongoing, got %q", m.Body)
	}
}

func TestAbsentSnapshotDoesNotProduceADeadLink(t *testing.T) {
	in := baseInput(t)
	in.SnapshotAvailable = false
	m, err := buildMessage(in, fixedID(t, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	if m.SnapshotURL != "" {
		t.Fatalf("a picture that cannot be served must not be linked, got %q", m.SnapshotURL)
	}
}

func TestUnknownZoneOmitsLocalRenderingsRatherThanGuessing(t *testing.T) {
	in := baseInput(t)
	in.Location = nil
	m, err := buildMessage(in, fixedID(t, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	if m.StartedLocal != "" || m.Timezone != "" {
		t.Fatalf("without a site zone the local text must be omitted, got %q %q", m.StartedLocal, m.Timezone)
	}
	// The canonical instant still travels, so a receiver can format its own.
	if !strings.Contains(m.Body, m.StartedAt.Format(time.RFC3339)) {
		t.Fatalf("body must fall back to the canonical instant, got %q", m.Body)
	}
}

func TestMessageRejectsIncompleteFacts(t *testing.T) {
	cases := map[string]func(*MessageInput){
		"no event":          func(in *MessageInput) { in.EventID = "" },
		"no rule":           func(in *MessageInput) { in.RuleID = "" },
		"no channel number": func(in *MessageInput) { in.ChannelNo = 0 },
		"no start":          func(in *MessageInput) { in.StartedAt = time.Time{} },
	}
	for name, mutate := range cases {
		in := baseInput(t)
		mutate(&in)
		if _, err := buildMessage(in, fixedID(t, "delivery")); err == nil {
			t.Fatalf("%s: a message without it would be meaningless", name)
		}
	}
}

// The stored form and the wire form must be one document: a receiver reading
// the JSON column and one reading a webhook body cannot be allowed to drift.
func TestStoredJSONMatchesTheWireShape(t *testing.T) {
	in := baseInput(t)
	in.SnapshotAvailable = true
	m, err := buildMessage(in, fixedID(t, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	// Spot-check the contract a receiver codes against.
	for _, key := range []string{"delivery_id", "event_id", "channel_id", "channel_no", "category_label", "started_at", "title", "body"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("message JSON must expose %q, got keys %v", key, keysOf(decoded))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
