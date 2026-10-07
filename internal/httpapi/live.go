package httpapi

import (
	"github.com/zhigu34/one-nvr/internal/auth"
	"net/http"
)

func (r *router) openLive(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	ch, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		Stream string `json:"stream"`
		SDP    string `json:"sdp"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	out, err := r.d.Live.Open(q.Context(), p, ch, in.Stream, in.SDP)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, out)
}
func (r *router) renewLive(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	lease, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err = r.d.Live.Renew(q.Context(), p, lease); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		ExpiresIn int `json:"expires_in"`
	}{30})
}
func (r *router) closeLive(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	lease, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	if err = r.d.Live.Close(q.Context(), p, lease); err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Closed bool `json:"closed"`
	}{true})
}
