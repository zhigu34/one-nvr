package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Health distinguishes a responsive process from its required database dependency.
func Health(check func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, status int, state string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": state})
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { respond(w, http.StatusOK, "alive") })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if check == nil || check(ctx) != nil {
			respond(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		respond(w, http.StatusOK, "ready")
	})
	return mux
}
