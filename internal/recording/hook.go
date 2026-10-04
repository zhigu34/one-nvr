package recording

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

func NewHookHandler(service *Service, token string, probeTokens ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		reject := func(status int) {
			w.WriteHeader(status)
			w.Write([]byte(`{"code":-1,"msg":"recording completion unavailable"}`))
		}
		if token == "" || !hmac.Equal([]byte(r.URL.Query().Get("token")), []byte(token)) {
			reject(403)
			return
		}
		if r.Method != "POST" || !validHookPath(r.URL.Path) {
			reject(404)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 65536)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				reject(413)
			} else {
				reject(400)
			}
			return
		}
		if r.URL.Path != "/on_record_mp4" {
			service.handleAccessHook(w, r, raw, probeTokens)
			return
		}
		var input struct {
			VHost         string  `json:"vhost"`
			App           string  `json:"app"`
			Stream        string  `json:"stream"`
			MediaServerID string  `json:"mediaServerId"`
			FilePath      string  `json:"file_path"`
			Size          int64   `json:"file_size"`
			Start         float64 `json:"start_time"`
			Duration      float64 `json:"time_len"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&input); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				reject(413)
			} else {
				reject(400)
			}
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				reject(413)
			} else {
				reject(400)
			}
			return
		}
		if math.IsNaN(input.Start) || math.IsInf(input.Start, 0) || input.Start <= 0 || input.Start > 32503680000 || math.IsNaN(input.Duration) || math.IsInf(input.Duration, 0) || input.Duration <= 0 || input.Duration > 86400 {
			reject(400)
			return
		}
		seconds, fraction := math.Modf(input.Start)
		c := Completion{Key: zlm.StreamKey{VHost: input.VHost, App: input.App, Stream: input.Stream}, MediaServerID: input.MediaServerID, FilePath: input.FilePath, Size: input.Size, StartTime: time.Unix(int64(seconds), int64(fraction*1e9)).UTC(), Duration: time.Duration(input.Duration * float64(time.Second))}
		if err := service.Accept(r.Context(), c); err != nil {
			if errors.Is(err, ErrCompletionInvalid) {
				reject(400)
			} else {
				reject(503)
			}
			return
		}
		w.Write([]byte(`{"code":0,"msg":"success"}`))
	})
}

func validHookPath(path string) bool {
	switch path {
	case "/on_record_mp4", "/on_play", "/on_http_access", "/on_stream_none_reader", "/on_publish":
		return true
	}
	return false
}
func (s *Service) handleAccessHook(w http.ResponseWriter, r *http.Request, raw []byte, tokens []string) {
	deny := func() {
		w.WriteHeader(403)
		w.Write([]byte(`{"code":-1,"msg":"media access denied","err":"media access denied","second":0}`))
	}
	if r.URL.Path == "/on_http_access" || r.URL.Path == "/on_publish" {
		deny()
		return
	}
	var in struct {
		VHost  string `json:"vhost"`
		App    string `json:"app"`
		Stream string `json:"stream"`
		Server string `json:"mediaServerId"`
		Params string `json:"params"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var extra any
	if decoder.Decode(&in) != nil || decoder.Decode(&extra) != io.EOF || in.Server != "one-nvr-"+string(s.siteID) || s.siteID == "" {
		deny()
		return
	}
	key := zlm.StreamKey{VHost: in.VHost, App: in.App, Stream: in.Stream}
	if key.Validate() != nil {
		deny()
		return
	}
	if r.URL.Path == "/on_stream_none_reader" {
		w.Write([]byte(`{"code":0,"close":false}`))
		return
	}
	params, err := url.ParseQuery(in.Params)
	if err != nil || len(tokens) != 1 || tokens[0] == "" || len(params["probe_token"]) != 1 || !hmac.Equal([]byte(params.Get("probe_token")), []byte(tokens[0])) || s.DB == nil || s.DB.Pool == nil {
		deny()
		return
	}
	var active bool
	err = s.DB.Pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM stream_sessions ss JOIN channels c ON c.id=ss.channel_id WHERE c.site_id=$1 AND ss.vhost=$2 AND ss.app=$3 AND ss.stream=$4 AND ss.state IN ('starting','active'))`, s.siteID, key.VHost, key.App, key.Stream).Scan(&active)
	if err != nil || !active {
		deny()
		return
	}
	w.Write([]byte(`{"code":0,"msg":"success"}`))
}
