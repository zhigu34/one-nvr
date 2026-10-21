package notify

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Channel carries a finished Message to wherever the operator wanted to hear
// about it. Everything a channel needs is in the Message; an implementation must
// not query the event, the rule or the site, because doing so would put delivery
// concerns back into the event model and make one channel's needs everyone's.
//
// The interface is intentionally small. A channel that needs configuration gets
// it when it is constructed (from the channel row), not per send.
type Channel interface {
	// Kind is the value stored in notification_channels.kind and is what the
	// registry is keyed by.
	Kind() string
	// Send delivers one message. Returning an error schedules a retry unless it
	// is a PermanentError, which says the receiver will refuse this message no
	// matter how often it is repeated.
	Send(ctx context.Context, m Message) error
}

// PermanentError marks a delivery the receiver will never accept: a refused
// payload, a malformed endpoint, a revoked credential. Retrying those burns the
// budget and delays the operator learning that nothing was delivered.
type PermanentError struct {
	Reason string
	// Status is the HTTP status when there was one, for diagnosis.
	Status int
}

func (e *PermanentError) Error() string { return e.Reason }

// IsPermanent reports whether an error should stop the retry budget.
func IsPermanent(err error) bool {
	var permanent *PermanentError
	return errors.As(err, &permanent)
}

// Registry resolves a channel kind to its adapter. Adding a channel kind means
// constructing one more adapter here; the message layer, the event ingestion and
// the rule bindings are untouched by it, which is the property this batch is
// built to keep.
type Registry struct {
	factories map[string]func(ChannelConfig) (Channel, error)
}

// ChannelConfig is the stored channel row, decrypted. Secret is empty for kinds
// that do not sign.
type ChannelConfig struct {
	ID     string
	Kind   string
	Name   string
	URL    string
	Secret []byte
	// Email carries the SMTP parameters for kind 'email'. Nil for other kinds,
	// so an adapter cannot read settings that do not exist for it.
	Email *SMTPConfig
	// Recipients are the mailboxes of an email channel, already resolved by the
	// service. Empty for other kinds.
	Recipients []string
	// WeCom carries the group-robot settings for kind 'wecom'. Nil otherwise.
	WeCom *WeComConfig
}

// NewRegistry starts a registry with no kinds. Callers register what they build.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]func(ChannelConfig) (Channel, error){}}
}

// Register adds a channel kind. A duplicate kind is a programming error, not a
// runtime condition: it would make which adapter runs depend on call order.
func (r *Registry) Register(kind string, factory func(ChannelConfig) (Channel, error)) {
	if _, exists := r.factories[kind]; exists {
		panic("notify: channel kind registered twice: " + kind)
	}
	r.factories[kind] = factory
}

// Build constructs the adapter for one configured channel.
func (r *Registry) Build(in ChannelConfig) (Channel, error) {
	factory, ok := r.factories[in.Kind]
	if !ok {
		return nil, fmt.Errorf("no adapter registered for channel kind %q", in.Kind)
	}
	return factory(in)
}

// Kinds lists the registered kinds, for tests and for the API to report what
// this build can deliver.
func (r *Registry) Kinds() []string {
	out := make([]string, 0, len(r.factories))
	for kind := range r.factories {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// DefaultRegistry is what a deployment ships with: the inbox and generic
// webhooks (EVT-05). Later kinds are added by registering them here.
func DefaultRegistry() *Registry {
	registry := NewRegistry()
	registry.Register("inapp", func(in ChannelConfig) (Channel, error) { return InApp{}, nil })
	registry.Register("webhook", func(in ChannelConfig) (Channel, error) { return newWebhook(in) })
	// SMTP mail (EVT-05). Registered here like any other kind: the message layer,
	// the rule bindings and the delivery queue are untouched by it, which is the
	// property the channel interface exists to keep.
	registry.Register("email", func(in ChannelConfig) (Channel, error) { return newEmail(in) })
	// WeCom group robots: one webhook URL, a message layout, optional mentions.
	registry.Register("wecom", func(in ChannelConfig) (Channel, error) { return newWeCom(in) })
	return registry
}

// InApp is the built-in inbox. The notification row *is* the delivery, so
// there is nothing to send: the message is visible the moment it is raised.
// It exists as an adapter rather than a special case so that "every bound
// channel gets a delivery row" stays true without an exception.
type InApp struct{}

func (InApp) Kind() string { return "inapp" }

func (InApp) Send(context.Context, Message) error { return nil }

// deliveryHTTPTimeout bounds one attempt. A receiver that has not answered in
// this long is treated like any other failure and retried later; holding the
// worker open would delay every other delivery.
const deliveryHTTPTimeout = 30 * time.Second
