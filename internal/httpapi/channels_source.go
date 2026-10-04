package httpapi

import (
	"net/http"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
)

func (r *router) listSourceRevisions(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	c, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	cursor, limit, err := pagination(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.ListRevisions(q.Context(), p, c, cursor, limit)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) createSourceDraft(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	c, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in channel.DraftInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	var keys []string
	if key := q.Header.Get("Idempotency-Key"); key != "" {
		keys = append(keys, key)
	}
	out, err := r.d.Sources.CreateDraft(q.Context(), p, c, version, in, keys...)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, out)
}
func (r *router) revealSourceCredentials(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	c, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		RevisionID id.ID `json:"revision_id"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if _, err := id.Parse(string(in.RevisionID)); err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	out, err := r.d.Sources.Reveal(q.Context(), p, c, in.RevisionID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
func (r *router) exportSourceConfig(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		ChannelIDs []id.ID `json:"channel_ids"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.Export(q.Context(), p, in.ChannelIDs)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}
