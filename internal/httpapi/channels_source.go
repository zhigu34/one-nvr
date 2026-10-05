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

func (r *router) requestSourceTest(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	revisionID, err := id.Parse(q.PathValue("revision_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	var empty struct{}
	if err := decode(w, q, &empty); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.RequestTest(q.Context(), p, channelID, revisionID, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}
func (r *router) sourceTestResult(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	testID, err := id.Parse(q.PathValue("test_id"))
	if err != nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	out, err := r.d.Sources.TestResult(q.Context(), p, channelID, testID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, out)
}

func (r *router) applySource(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in channel.SourceApplyInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	for _, value := range []id.ID{in.RevisionID, in.TestID} {
		if _, err := id.Parse(string(value)); err != nil {
			fail(w, q, auth.ErrInvalid)
			return
		}
	}
	in.ExpectedVersion = version
	out, err := r.d.Sources.RequestApply(q.Context(), p, ch, in, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}
func (r *router) clearSource(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct{}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Sources.RequestClear(q.Context(), p, ch, version, q.Header.Get("Idempotency-Key"))
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 202, out)
}
