package httpapi

import (
	"net/http"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/notify"
)

// listNotificationChannels handles GET /api/v1/notification-channels.
func (r *router) listNotificationChannels(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Notify == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channels, err := r.d.Notify.ListChannels(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []notify.ChannelView `json:"items"`
		// Kinds reports what this build can actually deliver, so the UI offers
		// the adapters that exist instead of a hard-coded list that could drift
		// from the registry.
		Kinds []string `json:"kinds"`
	}{channels, r.d.Notify.Registry().Kinds()})
}

// decodeChannelBody is the shared body of register/update. The secret is
// write-only: empty on an update keeps the stored one, so renaming a channel
// does not require re-entering a credential.
func decodeChannelBody(w http.ResponseWriter, q *http.Request) (notify.ChannelInput, bool) {
	var in struct {
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		URL     string `json:"url"`
		Secret  string `json:"secret"`
		Enabled *bool  `json:"enabled"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return notify.ChannelInput{}, false
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return notify.ChannelInput{
		Kind:    in.Kind,
		Name:    in.Name,
		URL:     in.URL,
		Secret:  in.Secret,
		Enabled: enabled,
	}, true
}

// registerNotificationChannel handles POST /api/v1/notification-channels.
func (r *router) registerNotificationChannel(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Notify == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	in, ok := decodeChannelBody(w, q)
	if !ok {
		return
	}
	channel, err := r.d.Notify.RegisterChannel(q.Context(), p, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, channel)
}

// updateNotificationChannel handles PATCH /api/v1/notification-channels/{id}.
func (r *router) updateNotificationChannel(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Notify == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	expected, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	in, ok := decodeChannelBody(w, q)
	if !ok {
		return
	}
	channel, err := r.d.Notify.UpdateChannel(q.Context(), p, channelID, expected, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, channel)
}

// deleteNotificationChannel handles DELETE /api/v1/notification-channels/{id}.
func (r *router) deleteNotificationChannel(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Notify == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	expected, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Notify.DeleteChannel(q.Context(), p, channelID, expected); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Deleted bool `json:"deleted"`
	}{true})
}

// testNotificationChannel handles POST /api/v1/notification-channels/{id}/test:
// one message the operator asked for, answered by what the receiver actually
// replied. A webhook has no read-write-delete probe to run, so this is the only
// honest way to say whether it works.
func (r *router) testNotificationChannel(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Notify == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	result, err := r.d.Notify.TestChannel(q.Context(), p, channelID)
	if err != nil && result == nil {
		fail(w, q, err)
		return
	}
	// A transient failure is reported with the measurement rather than instead
	// of it: the operator needs both "it did not accept" and "it said 500".
	respond(w, q, 200, result)
}

// listEventDeliveries handles GET /api/v1/events/{id}/deliveries: what was
// attempted for one event, for the detail drawer.
func (r *router) listEventDeliveries(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Notify == nil || r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	eventID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	// Reading deliveries is reading evidence about an event, so it travels with
	// the same grant that lets the operator see the event at all.
	if _, err := r.d.Events.Get(q.Context(), p, eventID); err != nil {
		fail(w, q, err)
		return
	}
	deliveries, err := r.d.Notify.DeliveriesForEvent(q.Context(), eventID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []notify.DeliveryView `json:"items"`
	}{deliveries})
}

// ruleChannels handles GET /api/v1/detection/rules/{id}/channels.
func (r *router) ruleChannels(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Notify == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	ruleID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	channels, err := r.d.Notify.RuleChannels(q.Context(), p, ruleID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		ChannelIDs []id.ID `json:"channel_ids"`
	}{channels})
}

// setRuleChannels handles PUT /api/v1/detection/rules/{id}/channels: which
// destinations a rule tells. An empty list means the rule records its hits
// without telling anyone, which is a legitimate configuration.
func (r *router) setRuleChannels(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Notify == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	ruleID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		ChannelIDs []id.ID `json:"channel_ids"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Notify.SetRuleChannels(q.Context(), p, ruleID, in.ChannelIDs); err != nil {
		fail(w, q, err)
		return
	}
	channels, err := r.d.Notify.RuleChannels(q.Context(), p, ruleID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		ChannelIDs []id.ID `json:"channel_ids"`
	}{channels})
}
