package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/capability"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/live"
	"github.com/zhigu34/one-nvr/internal/operations"
	"github.com/zhigu34/one-nvr/internal/recording"
	"github.com/zhigu34/one-nvr/internal/site"
	"github.com/zhigu34/one-nvr/internal/storage"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type Dependencies struct {
	Auth                            *auth.Service
	Site                            *site.Service
	PublicURL                       string
	TrustedProxyToken               string
	HealthCheck                     func(context.Context) error
	Channels                        *channel.Service
	Sources                         *channel.SourceService
	Recordings                      *recording.Service
	Live                            *live.Service
	Storage                         *storage.Service
	TLS                             *tlsmanager.Service
	Operations                      *operations.Service
	Capabilities                    *capability.Service
	FrigateEnabled, OpenListEnabled bool
}
type router struct {
	d             Dependencies
	mux           *http.ServeMux
	origin        string
	secure        bool
	limitMu       sync.Mutex
	protocolMu    sync.Mutex
	protocolReady bool
	limits        map[string]limitWindow
}
type limitWindow struct {
	count   int
	expires time.Time
}

const sessionCookie = "one_nvr_session"
const preAuthCookie = "one_nvr_preauth"

func NewHandler(d Dependencies) http.Handler {
	r := &router{d: d, mux: http.NewServeMux(), origin: canonicalOrigin(d.PublicURL), secure: strings.HasPrefix(d.PublicURL, "https://"), limits: map[string]limitWindow{}}
	if r.origin == "" || d.Auth == nil || d.Site == nil {
		panic("invalid API dependencies")
	}
	if d.Site.Auth == nil {
		d.Site.Auth = d.Auth
	}
	if d.Channels == nil {
		d.Channels = &channel.Service{DB: d.Auth.DB, Auth: d.Auth}
	}
	if d.Sources == nil {
		d.Sources = channel.NewSources(d.Auth.DB, d.Auth, d.Site.Secrets, channel.NetworkPolicy{})
	}
	if d.Operations == nil {
		d.Operations = operations.New(d.Auth.DB, d.Auth, d.FrigateEnabled, d.OpenListEnabled)
	}
	if d.Capabilities == nil {
		d.Capabilities = &capability.Service{Operations: d.Operations, FrigateEnabled: d.FrigateEnabled, OpenListEnabled: d.OpenListEnabled}
	}
	if d.Storage == nil {
		d.Storage = storage.New(d.Auth.DB, d.Auth, []string{"/storage"})
	}
	if d.Recordings == nil {
		d.Recordings = &recording.Service{DB: d.Auth.DB, Auth: d.Auth, Pools: d.Storage}
	}
	r.d = d
	if r.d.Live == nil {
		r.d.Live = &live.Service{DB: d.Auth.DB, Auth: d.Auth}
	}
	r.mux.Handle("/health/", Health(d.HealthCheck))
	r.mux.HandleFunc("GET /api/v1/setup/status", r.setupStatus)
	r.mux.HandleFunc("POST /api/v1/setup", r.anonymous(r.setup))
	r.mux.HandleFunc("POST /api/v1/auth/login", r.anonymous(r.login))
	r.mux.HandleFunc("GET /api/v1/auth/me", r.protected(r.me))
	r.mux.HandleFunc("POST /api/v1/auth/activity", r.protected(r.activity))
	r.mux.HandleFunc("POST /api/v1/auth/logout", r.protected(r.logout))
	r.mux.HandleFunc("POST /api/v1/auth/change-password", r.protected(r.changePassword))
	r.mux.HandleFunc("GET /api/v1/users", r.protected(r.listUsers))
	r.mux.HandleFunc("POST /api/v1/users", r.protected(r.createUser))
	r.mux.HandleFunc("PATCH /api/v1/users/{id}", r.protected(r.updateUser))
	r.mux.HandleFunc("GET /api/v1/users/{id}/channel-grants", r.protected(r.getGrants))
	r.mux.HandleFunc("PUT /api/v1/users/{id}/channel-grants", r.protected(r.setGrants))
	r.mux.HandleFunc("GET /api/v1/site", r.protected(r.currentSite))
	r.mux.HandleFunc("PATCH /api/v1/site", r.protected(r.updateSite))
	r.mux.HandleFunc("POST /api/v1/site/expand", r.protected(r.expandSite))
	r.mux.HandleFunc("GET /api/v1/timezones", r.timezones)
	r.mux.HandleFunc("GET /api/v1/channels", r.protected(r.listChannels))
	r.mux.HandleFunc("GET /api/v1/channels/summary", r.protected(r.channelSummaries))
	r.mux.HandleFunc("POST /api/v1/channels/{id}/live", r.protected(r.openLive))
	r.mux.HandleFunc("POST /api/v1/live-sessions/{id}/renew", r.protectedPassive(r.renewLive))
	r.mux.HandleFunc("DELETE /api/v1/live-sessions/{id}", r.protectedPassive(r.closeLive))
	r.mux.HandleFunc("GET /api/v1/settings/hardware", r.protected(r.hardwareReport))
	r.mux.HandleFunc("GET /api/v1/channel-slots", r.protected(r.channelSlots))
	r.mux.HandleFunc("PATCH /api/v1/channels/{id}", r.protected(r.updateChannel))
	r.mux.HandleFunc("GET /api/v1/channels/{id}/source-revisions", r.protected(r.listSourceRevisions))
	r.mux.HandleFunc("POST /api/v1/channels/{id}/source-revisions", r.protected(r.createSourceDraft))
	r.mux.HandleFunc("POST /api/v1/channels/{id}/source-revisions/{revision_id}/test", r.protected(r.requestSourceTest))
	r.mux.HandleFunc("POST /api/v1/channels/{id}/onvif/probe", r.protected(r.probeChannelONVIF))
	r.mux.HandleFunc("GET /api/v1/channels/{id}/source-tests/{test_id}", r.protected(r.sourceTestResult))
	r.mux.HandleFunc("POST /api/v1/channels/{id}/source/apply", r.protected(r.applySource))
	r.mux.HandleFunc("POST /api/v1/channels/{id}/source/clear", r.protected(r.clearSource))
	r.mux.HandleFunc("GET /api/v1/channels/{id}/source/status", r.protected(r.sourceStatus))
	r.mux.HandleFunc("GET /api/v1/channels/{id}/recording-policy", r.protected(r.recordingPolicy))
	r.mux.HandleFunc("PUT /api/v1/channels/{id}/recording-policy", r.protected(r.setRecordingPolicy))
	r.mux.HandleFunc("PUT /api/v1/recording-policies", r.protected(r.setRecordingPolicies))
	r.mux.HandleFunc("PUT /api/v1/channels/{id}/storage-pool", r.protected(r.bindChannelPool))
	r.mux.HandleFunc("POST /api/v1/channels/{id}/source/credentials/reveal", r.protected(r.revealSourceCredentials))
	r.mux.HandleFunc("POST /api/v1/channels/source-config-export", r.protected(r.exportSourceConfig))
	r.mux.HandleFunc("POST /api/v1/source-imports/preview", r.protected(r.importPreview))
	r.mux.HandleFunc("POST /api/v1/source-imports", r.protected(r.importSubmit))
	r.mux.HandleFunc("GET /api/v1/source-imports/{batch_id}", r.protected(r.importProgress))
	r.mux.HandleFunc("POST /api/v1/source-imports/{batch_id}/test", r.protected(r.importTest))
	r.mux.HandleFunc("POST /api/v1/source-imports/{batch_id}/retry", r.protected(r.importRetry))
	r.mux.HandleFunc("POST /api/v1/source-imports/{batch_id}/cancel", r.protected(r.importCancel))
	r.mux.HandleFunc("GET /api/v1/recordings", r.protected(r.recordings))
	r.mux.HandleFunc("GET /api/v1/recordings/{id}/content", r.protected(r.recordingContent))
	r.mux.HandleFunc("GET /api/v1/storage-pools", r.protected(r.listPools))
	r.mux.HandleFunc("POST /api/v1/storage-pools", r.protected(r.registerPool))
	r.mux.HandleFunc("PATCH /api/v1/storage-pools/{id}", r.protected(r.updatePool))
	r.mux.HandleFunc("DELETE /api/v1/storage-pools/{id}", r.protected(r.deletePool))
	r.mux.HandleFunc("POST /api/v1/storage-pools/{id}/test", r.protected(r.testPool))
	r.mux.HandleFunc("GET /api/v1/settings/tls", r.protected(r.tlsState))
	r.mux.HandleFunc("PATCH /api/v1/settings/tls/watch", r.protected(r.tlsWatch))
	r.mux.HandleFunc("POST /api/v1/settings/tls/import", r.protected(r.tlsImport))
	r.mux.HandleFunc("POST /api/v1/settings/tls/check", r.protected(r.tlsCheck))
	r.mux.HandleFunc("POST /api/v1/settings/tls/{id}/apply", r.protected(r.tlsApply))
	r.mux.HandleFunc("POST /api/v1/settings/tls/rollback", r.protected(r.tlsRollback))
	r.mux.HandleFunc("GET /api/v1/capabilities", r.protected(r.capabilities))
	r.mux.HandleFunc("GET /api/v1/operations/components", r.protected(r.components))
	r.mux.HandleFunc("GET /api/v1/jobs/{id}", r.protected(r.job))
	r.mux.HandleFunc("GET /api/v1/audit-logs", r.protected(r.auditLogs))
	r.mux.HandleFunc("POST /api/v1/archive-tasks", r.protected(r.futureArchive))
	r.mux.HandleFunc("POST /api/v1/detection-rules", r.protected(r.futureIntelligence))
	r.mux.HandleFunc("/", func(w http.ResponseWriter, q *http.Request) { fail(w, q, auth.ErrNotFound) })
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		requestID, err := id.New()
		if err != nil {
			fail(w, q, err)
			return
		}
		q = q.WithContext(context.WithValue(q.Context(), requestKey{}, requestID))
		w.Header().Set("X-Request-ID", string(requestID))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !strings.HasPrefix(q.URL.Path, "/health/") {
			scheme := "http"
			if r.secure {
				scheme = "https"
			}
			r.protocolMu.Lock()
			if !r.protocolReady {
				err = r.d.Auth.ApplyEntryProtocol(q.Context(), scheme)
				if err == nil {
					r.protocolReady = true
				}
			}
			r.protocolMu.Unlock()
			if err != nil {
				fail(w, q, err)
				return
			}
			if err = r.d.Auth.CheckEntryProtocol(q.Context(), scheme); err != nil {
				fail(w, q, err)
				return
			}
			q = q.WithContext(auth.WithEntryScheme(q.Context(), scheme))
		}
		r.mux.ServeHTTP(w, q)
	})
}
func (r *router) originAllowed(q *http.Request) bool {
	return canonicalOrigin(q.Header.Get("Origin")) == r.origin
}
func (r *router) anonymous(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, q *http.Request) {
		cookie, err := q.Cookie(r.cookieName(preAuthCookie))
		if !r.originAllowed(q) || err != nil || !r.d.Auth.VerifyPreAuth(cookie.Value, q.Header.Get("X-CSRF-Token")) {
			fail(w, q, fault.New(403, "csrf_rejected", "请求来源或安全令牌无效"))
			return
		}
		next(w, q)
	}
}

type protectedHandler func(http.ResponseWriter, *http.Request, auth.Principal, string)

func (r *router) protected(next protectedHandler) http.HandlerFunc {
	return r.protect(next, true)
}

// Automated playback renewals/cleanup authenticate and enforce CSRF, without
// extending the user-activity idle deadline.
func (r *router) protectedPassive(next protectedHandler) http.HandlerFunc {
	return r.protect(next, false)
}

func (r *router) protect(next protectedHandler, activity bool) http.HandlerFunc {
	return func(w http.ResponseWriter, q *http.Request) {
		cookie, err := q.Cookie(r.cookieName(sessionCookie))
		if err != nil {
			fail(w, q, auth.ErrUnauthenticated)
			return
		}
		p, err := r.d.Auth.CheckSession(q.Context(), cookie.Value)
		if err != nil {
			fail(w, q, err)
			return
		}
		if q.Method != "GET" && (!r.originAllowed(q) || !r.d.Auth.VerifyCSRF(cookie.Value, q.Header.Get("X-CSRF-Token"))) {
			fail(w, q, fault.New(403, "csrf_rejected", "请求来源或安全令牌无效"))
			return
		}
		if activity && q.Method != "GET" && q.Method != "HEAD" && q.Method != "OPTIONS" {
			p, err = r.d.Auth.Authenticate(q.Context(), cookie.Value)
			if err != nil {
				fail(w, q, err)
				return
			}
		}
		next(w, q, p, cookie.Value)
	}
}
func (r *router) cookieName(base string) string {
	if r.secure {
		return "__Host-" + base
	}
	return base
}
func (r *router) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: r.cookieName(name), Value: value, Path: "/", HttpOnly: true, Secure: r.secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}
func (r *router) setupStatus(w http.ResponseWriter, q *http.Request) {
	initialized, err := r.d.Site.Initialized(q.Context())
	if err != nil {
		fail(w, q, err)
		return
	}
	cookie, token, err := r.d.Auth.PreAuthToken()
	if err != nil {
		fail(w, q, err)
		return
	}
	r.setCookie(w, preAuthCookie, cookie, 600)
	respond(w, q, 200, struct {
		Initialized bool   `json:"initialized"`
		CSRFToken   string `json:"csrf_token"`
	}{initialized, token})
}
func (r *router) setup(w http.ResponseWriter, q *http.Request) {
	if !r.allow("setup:" + r.clientIP(q)) {
		fail(w, q, fault.New(429, "rate_limited", "尝试过于频繁，请稍后重试"))
		return
	}
	var in site.SetupInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	result, err := r.d.Site.Setup(q.Context(), in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, result)
}
func (r *router) allow(key string, maximum ...int) bool {
	limit := 10
	if len(maximum) > 0 {
		limit = maximum[0]
	}
	hash := sha256.Sum256([]byte(key))
	key = hex.EncodeToString(hash[:])
	r.limitMu.Lock()
	defer r.limitMu.Unlock()
	now := time.Now()
	for k, v := range r.limits {
		if !v.expires.After(now) {
			delete(r.limits, k)
		}
	}
	v, exists := r.limits[key]
	if !exists {
		if len(r.limits) >= 1024 {
			var oldest string
			var expiry time.Time
			for k, entry := range r.limits {
				if oldest == "" || entry.expires.Before(expiry) {
					oldest = k
					expiry = entry.expires
				}
			}
			delete(r.limits, oldest)
		}
		v = limitWindow{expires: now.Add(time.Minute)}
	}
	v.count++
	r.limits[key] = v
	return v.count <= limit
}
func (r *router) login(w http.ResponseWriter, q *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	peer := r.clientIP(q)
	if !r.allow("login-client:"+peer, 60) {
		fail(w, q, fault.New(429, "rate_limited", "尝试过于频繁，请稍后重试"))
		return
	}
	if !auth.ValidateUsername(in.Username) || len(in.Password) > 128 {
		fail(w, q, auth.ErrInvalid)
		return
	}
	if !r.allow("login:" + strings.ToLower(in.Username)) {
		fail(w, q, fault.New(429, "rate_limited", "尝试过于频繁，请稍后重试"))
		return
	}
	result, err := r.d.Auth.Login(q.Context(), in.Username, in.Password)
	if err != nil {
		fail(w, q, err)
		return
	}
	r.setCookie(w, sessionCookie, result.RawSession, 12*60*60)
	r.setCookie(w, preAuthCookie, "", -1)
	respond(w, q, 200, sessionDTO(result.User, r.d.Auth.CSRF(result.RawSession)))
}
func sessionDTO(user auth.User, csrf string) any {
	return struct {
		User      auth.User `json:"user"`
		CSRFToken string    `json:"csrf_token"`
	}{user, csrf}
}
func (r *router) me(w http.ResponseWriter, q *http.Request, p auth.Principal, raw string) {
	user, err := r.d.Auth.CurrentUser(q.Context(), p)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, sessionDTO(user, r.d.Auth.CSRF(raw)))
}
func (r *router) logout(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	if err := r.d.Auth.Logout(q.Context(), p); err != nil {
		fail(w, q, err)
		return
	}
	r.setCookie(w, sessionCookie, "", -1)
	respond(w, q, 200, struct{}{})
}
func (r *router) changePassword(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in struct {
		Old string `json:"old_password"`
		New string `json:"new_password"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Auth.ChangePassword(q.Context(), p, in.Old, in.New); err != nil {
		fail(w, q, err)
		return
	}
	r.setCookie(w, sessionCookie, "", -1)
	respond(w, q, 200, struct{}{})
}
func (r *router) listUsers(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	after, limit, err := pagination(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	users, err := r.d.Auth.ListUsers(q.Context(), p, after, limit+1)
	if err != nil {
		fail(w, q, err)
		return
	}
	var next *id.ID
	if len(users) > limit {
		cursor := users[limit-1].ID
		next = &cursor
		users = users[:limit]
	}
	respond(w, q, 200, struct {
		Items      []auth.User `json:"items"`
		NextCursor *id.ID      `json:"next_cursor"`
	}{users, next})
}
func (r *router) createUser(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	var in auth.CreateUserInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	user, err := r.d.Auth.CreateUser(q.Context(), p, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 201, user)
}
func (r *router) updateUser(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	userID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in auth.UpdateUserInput
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	user, err := r.d.Auth.UpdateUser(q.Context(), p, userID, version, in)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, user)
}
func (r *router) getGrants(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	userID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	grants, err := r.d.Auth.GetGrants(q.Context(), p, userID)
	if err != nil {
		fail(w, q, err)
		return
	}
	respond(w, q, 200, grants)
}
func (r *router) setGrants(w http.ResponseWriter, q *http.Request, p auth.Principal, _ string) {
	userID, err := pathID(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	version, err := expectedVersion(q)
	if err != nil {
		fail(w, q, err)
		return
	}
	var in struct {
		Grants *[]auth.Grant `json:"grants"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if in.Grants == nil {
		fail(w, q, auth.ErrInvalid)
		return
	}
	if err := r.d.Auth.SetGrants(q.Context(), p, userID, version, *in.Grants); err != nil {
		fail(w, q, err)
		return
	}
	// The transaction revoked the edited user's sessions. Do not re-authorize a
	// successful self-edit using the now-invalid principal.
	if userID == p.UserID {
		r.setCookie(w, sessionCookie, "", -1)
	}
	respond(w, q, 200, *in.Grants)
}

func (r *router) clientIP(q *http.Request) string {
	token := q.Header.Get("X-One-NVR-Proxy-Token")
	if r.d.TrustedProxyToken != "" && len(token) == len(r.d.TrustedProxyToken) && subtle.ConstantTimeCompare([]byte(token), []byte(r.d.TrustedProxyToken)) == 1 {
		if ip, err := netip.ParseAddr(q.Header.Get("X-One-NVR-Client-IP")); err == nil && ip.Zone() == "" {
			return ip.Unmap().String()
		}
	}
	peer, _, err := net.SplitHostPort(q.RemoteAddr)
	if err != nil {
		return q.RemoteAddr
	}
	return peer
}

func (r *router) activity(w http.ResponseWriter, q *http.Request, _ auth.Principal, _ string) {
	respond(w, q, 200, struct {
		Renewed bool `json:"renewed"`
	}{true})
}
