package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testChannelConfig(t *testing.T, url string) ChannelConfig {
	t.Helper()
	return ChannelConfig{
		ID:     "55555555-5555-4555-8555-555555555555",
		Kind:   "webhook",
		Name:   "值班群",
		URL:    url,
		Secret: []byte("test-signing-secret"),
	}
}

func sampleMessage(t *testing.T) Message {
	t.Helper()
	m, err := buildMessage(baseInput(t), fixedID(t, "delivery"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWebhookSignsTheExactBytesItSends(t *testing.T) {
	var gotBody []byte
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		var err error
		gotBody, err = readAll(r)
		if err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	channel, err := newWebhook(testChannelConfig(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	message := sampleMessage(t)
	if err := channel.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}

	// A receiver verifies by hashing what it received: the signature must cover
	// those exact bytes, not a re-serialization of the same facts.
	mac := hmac.New(sha256.New, []byte("test-signing-secret"))
	mac.Write(gotBody)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := gotHeaders.Get(HeaderSignature); got != expected {
		t.Fatalf("signature must verify against the received body: got %q want %q", got, expected)
	}
	// The delivery id must be the message's own, because that is what the
	// receiver deduplicates on across retries.
	if got := gotHeaders.Get(HeaderDelivery); got != string(message.DeliveryID) {
		t.Fatalf("delivery header must carry the message's delivery id, got %q", got)
	}
	if got := gotHeaders.Get(HeaderEvent); got != string(message.EventID) {
		t.Fatalf("event header must carry the event id, got %q", got)
	}
	// The body is the message document itself, so a receiver that stores what it
	// got holds the same facts the inbox shows.
	var decoded Message
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("body must be the message document: %v", err)
	}
	if decoded.DeliveryID != message.DeliveryID || decoded.Body != message.Body {
		t.Fatalf("body lost the message content: %+v", decoded)
	}
}

func TestWebhookRetriesWhatMightSucceedLater(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusTooManyRequests} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		channel, err := newWebhook(testChannelConfig(t, server.URL))
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		err = channel.Send(context.Background(), sampleMessage(t))
		if err == nil {
			server.Close()
			t.Fatalf("status %d must fail the attempt", status)
		}
		if IsPermanent(err) {
			t.Fatalf("status %d describes a busy or broken receiver, not a refused message", status)
		}
		if got := StatusOf(err); got != status {
			t.Fatalf("the failure must name the status the receiver answered, got %d want %d", got, status)
		}
		server.Close()
	}
}

func TestWebhookStopsRetryingARefusedMessage(t *testing.T) {
	// 4xx means the receiver looked at this message and refused it. Repeating it
	// would burn the budget and delay telling the operator nothing was sent.
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("refused by receiver"))
		}))
		channel, err := newWebhook(testChannelConfig(t, server.URL))
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		err = channel.Send(context.Background(), sampleMessage(t))
		if !IsPermanent(err) {
			server.Close()
			t.Fatalf("status %d must stop the retry budget, got %v", status, err)
		}
		server.Close()
	}
}

func TestWebhookRejectsAChannelThatCannotDeliver(t *testing.T) {
	cases := map[string]ChannelConfig{
		"no endpoint":    {Kind: "webhook", Name: "x", Secret: []byte("s")},
		"wrong scheme":   {Kind: "webhook", Name: "x", URL: "ftp://example.com", Secret: []byte("s")},
		"no secret":      {Kind: "webhook", Name: "x", URL: "https://example.com"},
		"blank endpoint": {Kind: "webhook", Name: "x", URL: "   ", Secret: []byte("s")},
	}
	for name, config := range cases {
		if _, err := newWebhook(config); err == nil {
			t.Fatalf("%s: a channel that cannot deliver must not be built", name)
		}
	}
}

// This is the property the batch is designed around: a new channel kind is
// added by writing an adapter and registering it. Nothing in the message layer,
// the event ingestion or the rule binding changes — proven here by a channel
// that did not exist when those were written.
func TestANewChannelKindNeedsOnlyAnAdapter(t *testing.T) {
	var received []Message
	registry := NewRegistry()
	registry.Register("inapp", func(ChannelConfig) (Channel, error) { return InApp{}, nil })
	registry.Register("test-sink", func(in ChannelConfig) (Channel, error) {
		return sink{into: &received}, nil
	})

	config := ChannelConfig{ID: "66666666-6666-4666-8666-666666666666", Kind: "test-sink", Name: "sink"}
	channel, err := registry.Build(config)
	if err != nil {
		t.Fatal(err)
	}
	message := sampleMessage(t)
	if err := channel.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if len(received) != 1 {
		t.Fatalf("the adapter must receive the message as built, got %d messages", len(received))
	}
	// The adapter got the whole document, without the message layer knowing it
	// exists.
	if received[0].DeliveryID != message.DeliveryID || received[0].Title != message.Title {
		t.Fatalf("adapter received an incomplete message: %+v", received[0])
	}
	if kinds := registry.Kinds(); len(kinds) != 2 || kinds[0] != "inapp" || kinds[1] != "test-sink" {
		t.Fatalf("registry must report the kinds it can build, got %v", kinds)
	}
	// An unregistered kind is refused rather than silently dropped: a channel
	// row that no build can deliver must be visible, not inert.
	if _, err := registry.Build(ChannelConfig{Kind: "email"}); err == nil {
		t.Fatal("a kind without an adapter must not build")
	}
}

func TestDefaultRegistryShipsTheDocumentedKinds(t *testing.T) {
	registry := DefaultRegistry()
	if kinds := registry.Kinds(); len(kinds) != 2 || kinds[0] != "inapp" || kinds[1] != "webhook" {
		t.Fatalf("EVT-05 ships the inbox and webhooks, got %v", kinds)
	}
	// The inbox needs no endpoint and no secret; it must always build.
	inbox, err := registry.Build(ChannelConfig{Kind: "inapp", Name: "站内"})
	if err != nil {
		t.Fatalf("the built-in inbox must build without configuration: %v", err)
	}
	if err := inbox.Send(context.Background(), sampleMessage(t)); err != nil {
		t.Fatalf("the inbox has nothing to send and must never fail: %v", err)
	}
}

type sink struct{ into *[]Message }

func (s sink) Kind() string { return "test-sink" }

func (s sink) Send(_ context.Context, m Message) error {
	*s.into = append(*s.into, m)
	return nil
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}
