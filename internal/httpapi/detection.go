package httpapi

import (
	"net/http"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/detection"
)

// detectionConfigs handles GET /api/v1/detection/configs (AI-01).
func (r *router) detectionConfigs(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Detection == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	configs, err := r.d.Detection.List(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Items []detection.Config `json:"items"`
	}{configs})
}

// updateDetectionConfig handles PUT /api/v1/detection/configs/{channel_id}.
func (r *router) updateDetectionConfig(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Detection == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	channelID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		Enabled bool     `json:"enabled"`
		Stream  string   `json:"stream"`
		FPS     int      `json:"fps"`
		Labels  []string `json:"labels"`
		Version int64    `json:"version"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	config, err := r.d.Detection.Upsert(q.Context(), p, detection.Config{
		ChannelID: channelID, Enabled: in.Enabled, Stream: in.Stream, FPS: in.FPS, Labels: in.Labels, Version: in.Version,
	})
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, config)
}

// publishDetection handles POST /api/v1/detection/publish: render + reload.
func (r *router) publishDetection(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Detection == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	revision, err := r.d.Detection.Publish(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, revision)
}

// detectionRuntime handles GET /api/v1/detection/runtime (AI-07).
func (r *router) detectionRuntime(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if r.d.Detection == nil {
		fail(w, q, auth.ErrNotFound)
		return
	}
	revision, err := r.d.Detection.LatestRevision(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	runtimeSnapshot, err := r.d.Detection.Runtime(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, struct {
		Runtime  detection.RuntimeSnapshot `json:"runtime"`
		Revision *detection.Revision       `json:"revision"`
		Labels   []string                  `json:"supported_labels"`
	}{runtimeSnapshot, revision, detection.SupportedLabels})
}
