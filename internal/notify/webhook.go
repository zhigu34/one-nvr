package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// webhook posts the message as JSON to an operator-configured endpoint.
//
// The body is the message document itself — the same one stored in
// notifications.message — so a receiver that stores what it got has the same
// facts the inbox shows, with no second schema to keep in step.
type webhook struct {
	url    string
	secret []byte
	client *http.Client
}

func newWebhook(in ChannelConfig) (Channel, error) {
	url := strings.TrimSpace(in.URL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("webhook needs an http or https endpoint")
	}
	if len(in.Secret) == 0 {
		return nil, fmt.Errorf("webhook needs a signing secret")
	}
	return &webhook{
		url:    url,
		secret: in.Secret,
		client: &http.Client{Timeout: deliveryHTTPTimeout},
	}, nil
}

func (w *webhook) Kind() string { return "webhook" }

// Headers a receiver uses. They are named here so the API documentation, the
// tests and the adapter cannot disagree about them.
const (
	HeaderDelivery  = "X-OneNVR-Delivery"
	HeaderSignature = "X-OneNVR-Signature"
	HeaderEvent     = "X-OneNVR-Event"
)

func (w *webhook) Send(ctx context.Context, m Message) error {
	body, err := json.Marshal(m)
	if err != nil {
		// The message was built by this program; failing to serialize it is a
		// defect, not a receiver problem, and retrying cannot fix it.
		return &PermanentError{Reason: "message is not serializable: " + err.Error()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Reason: "webhook request could not be built"}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(HeaderDelivery, string(m.DeliveryID))
	request.Header.Set(HeaderEvent, string(m.EventID))
	// The signature covers the exact bytes sent, so a receiver hashes what it
	// received and compares — no canonicalization rules to agree on.
	request.Header.Set(HeaderSignature, "sha256="+w.sign(body))
	// Retries must be able to reach a receiver that only just came back.
	request.Header.Set("User-Agent", "one-nvr-notify/1")

	response, err := w.client.Do(request)
	if err != nil {
		// A transport failure says nothing about the message: retry.
		return fmt.Errorf("webhook unreachable: %w", err)
	}
	defer response.Body.Close()
	// The body is read and discarded so the connection can be reused; only a
	// bounded prefix is kept, because it is what the operator sees as the
	// reason a receiver refused.
	excerpt, _ := io.ReadAll(io.LimitReader(response.Body, 512))

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		// The receiver is overloaded or broken right now; the same message may
		// well be accepted later.
		return &DeliveryError{Status: response.StatusCode, Reason: reason(response.StatusCode, excerpt)}
	default:
		// 4xx: the receiver looked at this message and refused it. Repeating it
		// changes nothing, and the operator should be told promptly.
		return &PermanentError{Status: response.StatusCode, Reason: reason(response.StatusCode, excerpt)}
	}
}

func (w *webhook) sign(body []byte) string {
	mac := hmac.New(sha256.New, w.secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// DeliveryError is a retryable refusal that carries what the far end said, so
// the failure the operator sees names a status instead of "failed".
type DeliveryError struct {
	Status int
	Reason string
}

func (e *DeliveryError) Error() string { return e.Reason }

// StatusOf reports the HTTP status behind a delivery failure, or zero when the
// failure never reached a receiver.
func StatusOf(err error) int {
	var delivery *DeliveryError
	if errors.As(err, &delivery) {
		return delivery.Status
	}
	var permanent *PermanentError
	if errors.As(err, &permanent) {
		return permanent.Status
	}
	return 0
}

func reason(status int, excerpt []byte) string {
	text := strings.TrimSpace(string(excerpt))
	if len(text) > 200 {
		text = text[:200]
	}
	if text == "" {
		return "receiver answered " + strconv.Itoa(status)
	}
	return "receiver answered " + strconv.Itoa(status) + ": " + text
}
