package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/events"
	"github.com/zhigu34/one-nvr/internal/id"
)

// listEvents handles GET /api/v1/events: EVT-01's filtered, paged index.
func (r *router) listEvents(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	query := events.ListQuery{
		Category:    q.URL.Query().Get("category"),
		HandleState: q.URL.Query().Get("handle_state"),
	}
	if raw := q.URL.Query().Get("channel_id"); raw != "" {
		parsed, err := id.Parse(raw)
		if err != nil {
			fail(w, q, auth.ErrInvalid)
			return
		}
		query.ChannelID = parsed
	}
	if raw := q.URL.Query().Get("source_revision_id"); raw != "" {
		parsed, err := id.Parse(raw)
		if err != nil {
			fail(w, q, auth.ErrInvalid)
			return
		}
		query.SourceRevisionID = parsed
	}
	if raw := q.URL.Query().Get("cursor"); raw != "" {
		parsed, err := id.Parse(raw)
		if err != nil {
			fail(w, q, auth.ErrInvalid)
			return
		}
		query.Cursor = parsed
	}
	start, end, err := eventWindow(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	query.Start, query.End = start, end
	query.Limit, _ = strconv.Atoi(q.URL.Query().Get("limit"))
	if query.Limit < 1 || query.Limit > 200 {
		query.Limit = 50
	}
	page, err := r.d.Events.List(q.Context(), p, query)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, page)
}

func (r *router) getEvent(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	eventID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	event, err := r.d.Events.Get(q.Context(), p, eventID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, event)
}

// handleEvent implements EVT-03: one transition with a note, version-guarded.
func (r *router) handleEvent(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	eventID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		State   string `json:"state"`
		Note    string `json:"note"`
		Version int64  `json:"version"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	event, err := r.d.Events.Handle(q.Context(), p, eventID, in.State, in.Note, in.Version)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, event)
}

// eventSnapshot proxies the upstream snapshot image (EVT-02).
func (r *router) eventSnapshot(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	eventID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	data, contentType, err := r.d.Events.Snapshot(q.Context(), p, eventID)
	if err != nil {
		fail(w, q, err)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// setEventMappings replaces the Frigate camera → channel mapping (admin).
func (r *router) setEventMappings(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	var in struct {
		Mappings map[id.ID]string `json:"mappings"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Events.SetMappings(q.Context(), p, in.Mappings); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct{}{})
}

// eventMappings lists the mapping table.
func (r *router) eventMappings(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Events == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	mappings, err := r.d.Events.Mappings(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, map[string]any{"mappings": mappings})
}

// eventWindow parses start/end epoch-ms query parameters.
func eventWindow(q *http.Request) (time.Time, time.Time, error) {
	start, errStart := strconv.ParseInt(q.URL.Query().Get("start"), 10, 64)
	end, errEnd := strconv.ParseInt(q.URL.Query().Get("end"), 10, 64)
	if errStart != nil || errEnd != nil {
		return time.Time{}, time.Time{}, auth.ErrInvalid
	}
	return time.UnixMilli(start).UTC(), time.UnixMilli(end).UTC(), nil
}
