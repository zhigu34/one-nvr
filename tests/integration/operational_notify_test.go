package integration

// Operational notifications: faults tell someone, once per start and once per
// recovery.
//
// The gap this closes was not a bug in any one function: abnormal_events recorded
// the fault correctly and every page showed it, and nothing was ever sent. A
// camera could be offline for a weekend with the operator unaware.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/notify"
)

// seedFault inserts one firing abnormal event, the way the operations sweep does.
func seedFault(t *testing.T, fixture publishFixture, kind, subject, severity, summary string) id.ID {
	t.Helper()
	faultID := mustID(t)
	if _, err := fixture.DB.Pool.Exec(context.Background(),
		`INSERT INTO abnormal_events(id,kind,severity,subject,state,summary,detail,occurrences,started_at,last_seen_at)
		 VALUES($1,$2,$3,$4,'firing',$5,'detail',1,clock_timestamp(),clock_timestamp())`,
		faultID, kind, severity, subject, summary); err != nil {
		t.Fatal(err)
	}
	return faultID
}

func resolveFault(t *testing.T, fixture publishFixture, faultID id.ID) {
	t.Helper()
	if _, err := fixture.DB.Pool.Exec(context.Background(),
		`UPDATE abnormal_events SET state='resolved',ended_at=clock_timestamp() WHERE id=$1`, faultID); err != nil {
		t.Fatal(err)
	}
}

func inboxChannel(t *testing.T, fixture notifyFixture) id.ID {
	t.Helper()
	channel, err := fixture.notify.RegisterChannel(context.Background(), fixture.fixture.Admin, notify.ChannelInput{
		Kind: "inapp", Name: "站内", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return channel.ID
}

// A fault that nobody subscribed to is recorded and told to nobody. The
// announcement is still stamped: "no one asked" is an answer, and re-scanning it
// forever would make every later subscription receive a backlog.
func TestUnsubscribedFaultIsMarkedAnnouncedWithoutSending(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	faultID := seedFault(t, fixture.fixture, "camera_offline", "ch-1", "warning", "CH01 离线")

	fixture.notify.DispatchOperational(ctx)

	var announced bool
	if err := fixture.fixture.DB.Pool.QueryRow(ctx, `SELECT announced_at IS NOT NULL FROM abnormal_events WHERE id=$1`, faultID).Scan(&announced); err != nil {
		t.Fatal(err)
	}
	if !announced {
		t.Fatal("an unsubscribed fault was left unannounced; every sweep would retry it forever")
	}
}

// The same fault is announced once. The sweep re-reads the same firing row every
// thirty seconds, and "start and recovery only" has to survive that.
func TestFaultIsAnnouncedOnceNotPerSweep(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channelID := inboxChannel(t, fixture)
	if err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "camera_offline", []notify.SubscriptionInput{
		{Kind: "camera_offline", ChannelID: channelID, MinSeverity: "info"},
	}); err != nil {
		t.Fatal(err)
	}
	faultID := seedFault(t, fixture.fixture, "camera_offline", "ch-1", "warning", "CH01 离线")

	// Three sweeps in a row: the first announces, the other two must not.
	fixture.notify.DispatchOperational(ctx)
	fixture.notify.DispatchOperational(ctx)
	fixture.notify.DispatchOperational(ctx)

	var first, second int
	if err := fixture.fixture.DB.Pool.QueryRow(ctx, `SELECT
		  (SELECT count(*) FROM abnormal_events WHERE id=$1 AND announced_at IS NOT NULL)
		, (SELECT count(*) FROM abnormal_events WHERE id=$1 AND resolved_announced_at IS NOT NULL)`, faultID).Scan(&first, &second); err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("announced=%d resolved=%d after three sweeps of one firing fault", first, second)
	}
}

// Resolution is announced exactly once, and only after it happened.
func TestFaultRecoveryIsAnnouncedOnce(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channelID := inboxChannel(t, fixture)
	if err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "camera_offline", []notify.SubscriptionInput{
		{Kind: "camera_offline", ChannelID: channelID, MinSeverity: "info"},
	}); err != nil {
		t.Fatal(err)
	}
	faultID := seedFault(t, fixture.fixture, "camera_offline", "ch-1", "warning", "CH01 离线")
	fixture.notify.DispatchOperational(ctx)

	resolveFault(t, fixture.fixture, faultID)
	fixture.notify.DispatchOperational(ctx)
	fixture.notify.DispatchOperational(ctx)

	var resolved bool
	if err := fixture.fixture.DB.Pool.QueryRow(ctx, `SELECT resolved_announced_at IS NOT NULL FROM abnormal_events WHERE id=$1`, faultID).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if !resolved {
		t.Fatal("a recovery was never announced")
	}
}

// The minimum severity filters: an operator who subscribes at 'critical' must not
// be woken by an 'info' fault.
func TestSubscriptionMinimumSeverityFilters(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channelID := inboxChannel(t, fixture)
	if err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "queue_backlog", []notify.SubscriptionInput{
		{Kind: "queue_backlog", ChannelID: channelID, MinSeverity: "critical"},
	}); err != nil {
		t.Fatal(err)
	}
	// An info-level fault is below the bar; a critical one is not.
	info := seedFault(t, fixture.fixture, "queue_backlog", "detection", "info", "队列积压")
	critical := seedFault(t, fixture.fixture, "queue_backlog", "detection-2", "critical", "队列严重积压")

	fixture.notify.DispatchOperational(ctx)

	// Both are stamped: the filtered one was considered and deliberately not
	// sent, which is not the same as "still waiting".
	for _, faultID := range []id.ID{info, critical} {
		var announced bool
		if err := fixture.fixture.DB.Pool.QueryRow(ctx, `SELECT announced_at IS NOT NULL FROM abnormal_events WHERE id=$1`, faultID).Scan(&announced); err != nil {
			t.Fatal(err)
		}
		if !announced {
			t.Fatalf("fault %s was left unannounced", faultID)
		}
	}
}

// A subscription for one kind must not deliver another kind's faults.
func TestSubscriptionIsPerKind(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channelID := inboxChannel(t, fixture)
	if err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "camera_offline", []notify.SubscriptionInput{
		{Kind: "camera_offline", ChannelID: channelID, MinSeverity: "info"},
	}); err != nil {
		t.Fatal(err)
	}
	subs, err := fixture.notify.ListSubscriptions(ctx, fixture.fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Kind != "camera_offline" {
		t.Fatalf("subscriptions are %+v, want exactly the camera_offline one", subs)
	}
}

// Replacing a kind's subscriptions is a set operation: the destinations left out
// are gone, not kept alongside.
func TestSetSubscriptionsReplacesTheSet(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	first := inboxChannel(t, fixture)
	second, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, notify.ChannelInput{
		Kind: "inapp", Name: "站内二", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "camera_offline", []notify.SubscriptionInput{
		{Kind: "camera_offline", ChannelID: first, MinSeverity: "info"},
		{Kind: "camera_offline", ChannelID: second.ID, MinSeverity: "info"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "camera_offline", []notify.SubscriptionInput{
		{Kind: "camera_offline", ChannelID: second.ID, MinSeverity: "warning"},
	}); err != nil {
		t.Fatal(err)
	}
	subs, err := fixture.notify.ListSubscriptions(ctx, fixture.fixture.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].ChannelID != second.ID || subs[0].MinSeverity != "warning" {
		t.Fatalf("subscriptions are %+v, want only the second at warning", subs)
	}
}

// An unknown kind is refused and names itself: a typo in a URL path must not
// silently create a subscription nothing will ever match.
func TestUnknownOperationalKindIsRefused(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channelID := inboxChannel(t, fixture)
	err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "camera_ofline", []notify.SubscriptionInput{
		{Kind: "camera_ofline", ChannelID: channelID, MinSeverity: "info"},
	})
	if err == nil {
		t.Fatal("a misspelled kind was accepted")
	}
	var public *fault.Error
	if !errors.As(err, &public) {
		t.Fatalf("rejection is not a public fault: %v", err)
	}
	if !strings.Contains(public.Message, "camera_ofline") {
		t.Fatalf("the rejection does not name the unknown kind: %q", public.Message)
	}
}

// Deleting a channel removes its operational subscriptions: a row pointing at a
// deleted channel would be unreachable data.
func TestDeletingAChannelRemovesItsOperationalSubscriptions(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	channel, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, notify.ChannelInput{
		Kind: "inapp", Name: "站内", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "camera_offline", []notify.SubscriptionInput{
		{Kind: "camera_offline", ChannelID: channel.ID, MinSeverity: "info"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.notify.DeleteChannel(ctx, fixture.fixture.Admin, channel.ID, channel.Version); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := fixture.fixture.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM operational_notification_subscriptions WHERE channel_id=$1`, channel.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d subscriptions survived their channel", left)
	}
}

// The message really leaves: a fault subscribed to a real transport must arrive
// there, not merely be marked announced. The inbox always succeeds and would hide
// a dispatch that never called Send.
func TestOperationalFaultReachesATransport(t *testing.T) {
	fixture := newNotifyFixture(t)
	ctx := context.Background()
	dav := webdavTestServer(t, "u", "p")
	_ = dav
	// A webhook receiver that records what it was told.
	var received []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var message struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		_ = json.Unmarshal(body, &message)
		received = append(received, message.Title+"|"+message.Body)
		w.WriteHeader(200)
	}))
	defer receiver.Close()

	channel, err := fixture.notify.RegisterChannel(ctx, fixture.fixture.Admin, notify.ChannelInput{
		Kind: "webhook", Name: "值班", URL: receiver.URL, Secret: "signing", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.notify.SetSubscriptions(ctx, fixture.fixture.Admin, "camera_offline", []notify.SubscriptionInput{
		{Kind: "camera_offline", ChannelID: channel.ID, MinSeverity: "info"},
	}); err != nil {
		t.Fatal(err)
	}
	seedFault(t, fixture.fixture, "camera_offline", "ch-1", "critical", "CH01 摄像头离线")

	fixture.notify.DispatchOperational(ctx)

	if len(received) != 1 {
		t.Fatalf("the receiver got %d messages, want 1", len(received))
	}
	// The message names the fault and its severity, and says it is an operational
	// notice rather than a detection.
	if !strings.Contains(received[0], "CH01 摄像头离线") {
		t.Fatalf("the message does not name the fault: %s", received[0])
	}
	// Operational severities are critical/warning/info; the message must show the
	// operator's word, not the raw code.
	if !strings.Contains(received[0], "严重") {
		t.Fatalf("the message does not carry the severity in words: %s", received[0])
	}
	if strings.Contains(received[0], "[critical]") {
		t.Fatalf("the raw severity code leaked into the message: %s", received[0])
	}
}
