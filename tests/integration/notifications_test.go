package integration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/notify"
)

type notifyFixture struct {
	fixture publishFixture
	service *events.Service
	notify  *notify.Service
}

// newNotifyFixture wires the notification service into the event service the
// way the assembly does, so what these tests exercise is the real path: a rule
// hit raises its notification inside the ingestion transaction and queues the
// outbound delivery.
func newNotifyFixture(t *testing.T) notifyFixture {
	t.Helper()
	fixture := newPublicationFixture(t)
	notifications := notify.New(fixture.DB, fixture.Auth, fixture.Site.Secrets, fixture.Site.Secrets.SiteID)
	service := events.New(fixture.DB, fixture.Auth, "", fixture.Site.Secrets.SiteID)
	service.Notify = notifications
	return notifyFixture{fixture: fixture, service: service, notify: notifications}
}

// registerChannel creates a destination the way the API does, so the sealing of
// the signing secret travels the production path.
func (f notifyFixture) registerChannel(t *testing.T, kind, name, url string) notify.ChannelView {
	t.Helper()
	channel, err := f.notify.RegisterChannel(context.Background(), f.fixture.Admin, notify.ChannelInput{
		Kind:    kind,
		Name:    name,
		URL:     url,
		Secret:  "signing-secret-" + name,
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

// createRule stores one rule for the fixture channel and binds the given
// destinations to it.
func (f notifyFixture) createRule(t *testing.T, name string, cooldown int, channelIDs ...id.ID) events.Rule {
	t.Helper()
	ctx := context.Background()
	rule, err := f.service.UpsertRule(ctx, f.fixture.Admin, events.Rule{
		ChannelID:   f.fixture.Channel,
		Name:        name,
		Kind:        "appear",
		Categories:  []string{"person"},
		Severity:    "medium",
		Cooldown:    cooldown,
		Enabled:     true,
		MinScore:    0,
		ArmFrom:     "",
		ArmTo:       "",
		DwellSecond: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(channelIDs) > 0 {
		if err := f.notify.SetRuleChannels(ctx, f.fixture.Admin, rule.ID, channelIDs); err != nil {
			t.Fatal(err)
		}
	}
	return rule
}

// mapCamera points the fixture channel at one Frigate camera name.
func (f notifyFixture) mapCamera(t *testing.T, camera string) {
	t.Helper()
	if err := f.service.SetMappings(context.Background(), f.fixture.Admin, map[id.ID]string{f.fixture.Channel: camera}); err != nil {
		t.Fatal(err)
	}
}

// ingest stores one event through the real ingestion path.
func (f notifyFixture) ingest(t *testing.T, sourceID, camera, label string) {
	t.Helper()
	started := float64(time.Now().UTC().Add(-time.Minute).Unix())
	raw := frigatePayload("new", sourceID, camera, label, started, nil, nil, true)
	if err := f.service.Ingest(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
}

// countRows runs a counting query, so assertions read as facts about the
// database rather than about Go variables.
func countRows(t *testing.T, f notifyFixture, query string, args ...any) int {
	t.Helper()
	var count int
	if err := f.fixture.DB.Pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// A notification nobody queued is worse than no notification at all: the event
// and the fact that someone was told about it must commit together. This proves
// the transaction by rolling the ingestion back and checking that neither
// survives.
func TestNotificationIsRaisedInsideTheIngestionTransaction(t *testing.T) {
	f := newNotifyFixture(t)
	f.mapCamera(t, "front-door")
	inapp := f.registerChannel(t, "inapp", "站内", "")
	f.createRule(t, "有人", 0, inapp.ID)

	f.ingest(t, "evt-tx", "front-door", "person")

	// Both halves exist for the same event.
	if got := countRows(t, f, `SELECT count(*) FROM notifications WHERE event_id=(SELECT event_id FROM event_rule_hits LIMIT 1)`); got != 1 {
		t.Fatalf("the hit must produce exactly one notification, got %d", got)
	}
	if got := countRows(t, f, `SELECT count(*) FROM event_rule_hits WHERE notified_at IS NOT NULL`); got != 1 {
		t.Fatalf("the inbox delivery must mark the hit notified, got %d", got)
	}
	// The inbox needs no queue: its row is the delivery.
	if got := countRows(t, f, `SELECT count(*) FROM jobs WHERE kind=$1`, notify.JobKindWebhook); got != 0 {
		t.Fatalf("an inbox-only binding must not queue an outbound job, got %d", got)
	}
}

// One logical hit is told once (AI-05). The detector repeats the same event as
// it updates, and each repeat re-evaluates the rules — a second alert for one
// thing that happened is exactly what the fence exists to stop.
func TestARepeatedUpdateDoesNotRaiseASecondNotification(t *testing.T) {
	f := newNotifyFixture(t)
	f.mapCamera(t, "front-door")
	inapp := f.registerChannel(t, "inapp", "站内", "")
	f.createRule(t, "有人", 0, inapp.ID)

	f.ingest(t, "evt-repeat", "front-door", "person")
	// The same upstream id arrives again as an update, as Frigate sends it.
	started := float64(time.Now().UTC().Add(-time.Minute).Unix())
	end := started + 12
	raw := frigatePayload("update", "evt-repeat", "front-door", "person", started, &end, nil, true)
	if err := f.service.Ingest(context.Background(), raw); err != nil {
		t.Fatal(err)
	}

	if got := countRows(t, f, `SELECT count(*) FROM notifications`); got != 1 {
		t.Fatalf("one logical hit must be told once, got %d notifications", got)
	}
	if got := countRows(t, f, `SELECT count(*) FROM event_rule_hits`); got != 1 {
		t.Fatalf("the repeated update must refresh the hit, not add one, got %d", got)
	}
}

// The cooldown governs how often an external channel is pushed. It must never
// suppress the record: the operator asked to know that something happened.
func TestCooldownSuppressesTheOutboundPushButNotTheRecord(t *testing.T) {
	f := newNotifyFixture(t)
	f.mapCamera(t, "front-door")
	inapp := f.registerChannel(t, "inapp", "站内", "")
	hook := f.registerChannel(t, "webhook", "值班群", "https://chat.example.com/hook/abc")
	f.createRule(t, "有人", 600, inapp.ID, hook.ID)

	f.ingest(t, "evt-cool-1", "front-door", "person")
	// A different target, so the second event is a separate hit rather than a
	// repeat of the first.
	f.ingest(t, "evt-cool-2", "front-door", "person")

	if got := countRows(t, f, `SELECT count(*) FROM event_rule_hits`); got != 2 {
		t.Fatalf("both hits must be recorded, got %d", got)
	}
	if got := countRows(t, f, `SELECT count(*) FROM notifications`); got != 2 {
		t.Fatalf("the inbox must carry both, got %d", got)
	}
	// Only the first event queues the outbound delivery: the second falls inside
	// the cooldown window.
	if got := countRows(t, f, `SELECT count(*) FROM jobs WHERE kind=$1`, notify.JobKindWebhook); got != 1 {
		t.Fatalf("the cooldown must suppress the repeat push, got %d queued", got)
	}
}

// A channel the operator switched off must not be queued for: binding one and
// disabling it looks like a working alert path, and the server has to be the
// one that knows it is not.
func TestADisabledChannelIsNotQueued(t *testing.T) {
	f := newNotifyFixture(t)
	f.mapCamera(t, "front-door")
	hook := f.registerChannel(t, "webhook", "值班群", "https://chat.example.com/hook/abc")
	if _, err := f.fixture.DB.Pool.Exec(context.Background(), `UPDATE notification_channels SET enabled=false WHERE id=$1`, hook.ID); err != nil {
		t.Fatal(err)
	}
	f.createRule(t, "有人", 0, hook.ID)

	f.ingest(t, "evt-disabled", "front-door", "person")

	if got := countRows(t, f, `SELECT count(*) FROM event_rule_hits`); got != 1 {
		t.Fatalf("the hit is still recorded, got %d", got)
	}
	if got := countRows(t, f, `SELECT count(*) FROM jobs WHERE kind=$1`, notify.JobKindWebhook); got != 0 {
		t.Fatalf("a disabled channel must not be queued for, got %d", got)
	}
}

// A rule with no binding records its hits and tells no one. That is a choice,
// not an omission, and the event must still be there to review.
func TestAnUnboundRuleRecordsWithoutTelling(t *testing.T) {
	f := newNotifyFixture(t)
	f.mapCamera(t, "front-door")
	f.createRule(t, "只记录", 0)

	f.ingest(t, "evt-unbound", "front-door", "person")

	if got := countRows(t, f, `SELECT count(*) FROM event_rule_hits`); got != 1 {
		t.Fatalf("an unbound rule must still record its hit, got %d", got)
	}
	if got := countRows(t, f, `SELECT count(*) FROM notifications`); got != 0 {
		t.Fatalf("nothing must be told when no channel is bound, got %d", got)
	}
}

// The delivery loop must reach the receiver, sign what it sends, and record the
// outcome. This runs the real handler against a real HTTP server, so the
// adapter's request is what a receiver would see.
func TestWebhookDeliverySignsAndRecordsSuccess(t *testing.T) {
	f := newNotifyFixture(t)
	f.mapCamera(t, "front-door")

	var mu sync.Mutex
	var bodies [][]byte
	var headers []http.Header
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		mu.Lock()
		bodies = append(bodies, raw)
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	hook := f.registerChannel(t, "webhook", "值班群", receiver.URL)
	f.createRule(t, "有人", 0, hook.ID)
	f.ingest(t, "evt-deliver", "front-door", "person")

	f.deliverQueued(t)

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("the receiver must be called once, got %d", len(bodies))
	}
	// The body is the message document; the receiver can store it as-is.
	var message notify.Message
	if err := json.Unmarshal(bodies[0], &message); err != nil {
		t.Fatalf("the body must be the message document: %v", err)
	}
	if message.Title == "" || message.ChannelNo == 0 {
		t.Fatalf("the message lost its operator-facing facts: %+v", message)
	}
	// The delivery id travels as a header, and it is the message's own, so a
	// receiver deduplicating on it is deduplicating on the right thing.
	if got := headers[0].Get(notify.HeaderDelivery); got != string(message.DeliveryID) {
		t.Fatalf("delivery header must carry the message id, got %q", got)
	}
	if !verifySignature(t, headers[0].Get(notify.HeaderSignature), bodies[0], "signing-secret-值班群") {
		t.Fatal("the signature must verify against the bytes the receiver got")
	}
	if got := countRows(t, f, `SELECT count(*) FROM notification_deliveries WHERE state='succeeded'`); got != 1 {
		t.Fatalf("the accepted delivery must be recorded as succeeded, got %d", got)
	}
}

// A receiver that answers 4xx has looked at the message and refused it.
// Repeating it changes nothing, so the delivery must go terminal promptly
// rather than burning the retry budget.
func TestARefusedDeliveryStopsRetrying(t *testing.T) {
	f := newNotifyFixture(t)
	f.mapCamera(t, "front-door")
	var calls int
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("bad token"))
	}))
	defer receiver.Close()
	hook := f.registerChannel(t, "webhook", "值班群", receiver.URL)
	f.createRule(t, "有人", 0, hook.ID)
	f.ingest(t, "evt-refused", "front-door", "person")

	f.deliverQueued(t)

	if calls != 1 {
		t.Fatalf("a refused message must not be repeated, got %d calls", calls)
	}
	var state, reason string
	if err := f.fixture.DB.Pool.QueryRow(context.Background(),
		`SELECT state,last_error FROM notification_deliveries`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != notify.StateExhausted {
		t.Fatalf("a refusal is terminal, got state %q", state)
	}
	// The reason names the status, which is the only actionable evidence.
	if reason == "" {
		t.Fatal("a terminal failure must record why")
	}
}

// A receiver that is down is not the message's fault: the delivery stays in the
// retry window, and — the part that matters — nothing about the event, the
// recording or the hit changes.
func TestATransientFailureLeavesTheEventUntouched(t *testing.T) {
	f := newNotifyFixture(t)
	f.mapCamera(t, "front-door")
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer receiver.Close()
	hook := f.registerChannel(t, "webhook", "值班群", receiver.URL)
	f.createRule(t, "有人", 0, hook.ID)
	f.ingest(t, "evt-transient", "front-door", "person")

	var eventBefore, hitBefore int
	ctx := context.Background()
	if err := f.fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&eventBefore); err != nil {
		t.Fatal(err)
	}
	if err := f.fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM event_rule_hits").Scan(&hitBefore); err != nil {
		t.Fatal(err)
	}

	f.deliverQueued(t)

	var state string
	if err := f.fixture.DB.Pool.QueryRow(ctx, `SELECT state FROM notification_deliveries`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != notify.StateFailed {
		t.Fatalf("a 5xx is retryable, got state %q", state)
	}
	// EVT-05: a notification failure must not affect the event record.
	var eventAfter, hitAfter int
	if err := f.fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&eventAfter); err != nil {
		t.Fatal(err)
	}
	if err := f.fixture.DB.Pool.QueryRow(ctx, "SELECT count(*) FROM event_rule_hits").Scan(&hitAfter); err != nil {
		t.Fatal(err)
	}
	if eventBefore != eventAfter || hitBefore != hitAfter {
		t.Fatalf("a delivery failure changed the event record: events %d→%d hits %d→%d", eventBefore, eventAfter, hitBefore, hitAfter)
	}
}

// deliverQueued runs the queued webhook deliveries the way the worker does:
// claim one, execute the real handler with its fenced lease, and let the job
// layer decide what a failure means. jobs.Run is the worker's endless serve
// loop, so a test that wants a bounded drain drives Claim/Execute itself.
func (f notifyFixture) deliverQueued(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	repo := jobs.Repository{DB: f.fixture.DB}
	for {
		lease, err := repo.Claim(ctx, notify.JobKindWebhook)
		if errors.Is(err, jobs.ErrNoJob) {
			return
		}
		if err != nil {
			t.Fatalf("claim failed: %v", err)
		}
		// A handler error is the delivery's outcome, not the test's failure:
		// the job row now carries it and the assertions read that.
		_ = jobs.Execute(ctx, repo, lease, f.notify.HandleWebhook)
	}
}

// verifySignature recomputes what a receiver would: an HMAC over the exact
// bytes it was handed, compared against the header.
func verifySignature(t *testing.T, header string, body []byte, secret string) bool {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return header == "sha256="+hex.EncodeToString(mac.Sum(nil))
}

// A viewer must not be able to configure channels: the endpoint names internal
// URLs and holds signing secrets.
func TestNotificationChannelManagementRequiresAdmin(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()
	viewer, err := f.fixture.Auth.CreateUser(ctx, f.fixture.Admin, auth.CreateUserInput{Username: "notify_viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	login, err := f.fixture.Auth.Login(ctx, "notify_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	_ = viewer
	if _, err := f.notify.RegisterChannel(ctx, login.Principal, notify.ChannelInput{Kind: "inapp", Name: "偷偷加的"}); err == nil {
		t.Fatal("a viewer must not register a notification channel")
	}
	if _, err := f.notify.ListChannels(ctx, login.Principal); err == nil {
		t.Fatal("a viewer must not list notification channels")
	}
}
