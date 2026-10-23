package httpapi

import (
	"net/http"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/detection"
	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/id"
)

// channelZones handles GET/PUT/DELETE for one channel's detection zones.
// Zones are definitions handed to the detector: this layer stores the drawing,
// the detector performs the zone semantics.
func (r *router) listChannelZones(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Detection == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	zones, err := r.d.Detection.ListZones(q.Context(), p, channelID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []detection.Zone `json:"items"`
	}{zones})
}

func (r *router) saveChannelZone(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Detection == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	var in struct {
		ID          id.ID             `json:"id"`
		ChannelID   id.ID             `json:"channel_id"`
		Name        string            `json:"name"`
		Coordinates []detection.Point `json:"coordinates"`
		Objects     []string          `json:"objects"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	zone, err := r.d.Detection.UpsertZone(q.Context(), p, detection.Zone{
		ID: in.ID, ChannelID: in.ChannelID, Name: in.Name, Coordinates: in.Coordinates, Objects: in.Objects,
	})
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, zone)
}

func (r *router) deleteChannelZone(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Detection == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	zoneID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	if err := r.d.Detection.DeleteZone(q.Context(), p, channelID, zoneID); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct{}{})
}

// detectionFrame proxies one still of the frame the detector analyzes (AI-02).
// The zone canvas draws on exactly this image, so a polygon the operator draws
// is relative to the pixels the detector classifies.
func (r *router) detectionFrame(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Detection == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	data, contentType, err := r.d.Detection.Frame(q.Context(), p, channelID)
	if err != nil {
		fail(w, q, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// channelRules handles the business rules that run on top of detection events.
func (r *router) listChannelRules(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	rules, err := r.d.Events.ListRules(q.Context(), p, channelID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []events.Rule `json:"items"`
	}{rules})
}

func (r *router) saveChannelRule(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	var in events.Rule
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	rule, err := r.d.Events.UpsertRule(q.Context(), p, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, rule)
}

func (r *router) deleteChannelRule(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	ruleID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	if err := r.d.Events.DeleteRule(q.Context(), p, channelID, ruleID); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct{}{})
}

// markRuleHitFalsePositive records the operator's feedback on one hit.
func (r *router) markRuleHitFalsePositive(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	hitID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		FalsePositive *bool  `json:"false_positive"`
		Note          string `json:"note"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if in.FalsePositive == nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	if err := r.d.Events.MarkFalsePositive(q.Context(), p, hitID, in.Note, *in.FalsePositive); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct{}{})
}

// Detection exclusions: time-of-day windows in which a region stops reporting.
//
// Their own endpoints rather than a flag on channel_zones: a zone says where to
// look, an exclusion says where to stay quiet, and the two are edited for
// different reasons and by different people.
func (r *router) listDetectionExclusions(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	items, err := r.d.Events.ListExclusions(q.Context(), p, channelID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []events.ExclusionView `json:"items"`
	}{items})
}

func (r *router) saveDetectionExclusion(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	var in events.ExclusionInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	view, err := r.d.Events.SaveExclusion(q.Context(), p, channelID, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, view)
}

func (r *router) deleteDetectionExclusion(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := id.Parse(q.URL.Query().Get("channel_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	ruleID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	expected, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Events.DeleteExclusion(q.Context(), p, channelID, ruleID, expected); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 204, nil)
}
