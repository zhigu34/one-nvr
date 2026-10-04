package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/site"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Dependencies struct {
	Auth        *auth.Service
	Site        *site.Service
	PublicURL   string
	HealthCheck func(context.Context) error
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
	r.mux.Handle("/health/", Health(d.HealthCheck))
	r.mux.HandleFunc("GET /api/v1/setup/status", r.setupStatus)
	r.mux.HandleFunc("POST /api/v1/setup", r.anonymous(r.setup))
	r.mux.HandleFunc("POST /api/v1/auth/login", r.anonymous(r.login))
	r.mux.HandleFunc("GET /api/v1/auth/me", r.protected(r.me))
	r.mux.HandleFunc("POST /api/v1/auth/logout", r.protected(r.logout))
	r.mux.HandleFunc("POST /api/v1/auth/change-password", r.protected(r.changePassword))
	r.mux.HandleFunc("GET /api/v1/users", r.protected(r.listUsers))
	r.mux.HandleFunc("POST /api/v1/users", r.protected(r.createUser))
	r.mux.HandleFunc("PATCH /api/v1/users/{id}", r.protected(r.updateUser))
	r.mux.HandleFunc("GET /api/v1/users/{id}/channel-grants", r.protected(r.getGrants))
	r.mux.HandleFunc("PUT /api/v1/users/{id}/channel-grants", r.protected(r.setGrants))
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
		cookie, err := q.Cookie(preAuthCookie)
		if !r.originAllowed(q) || err != nil || !r.d.Auth.VerifyPreAuth(cookie.Value, q.Header.Get("X-CSRF-Token")) {
			fail(w, q, fault.New(403, "csrf_rejected", "请求来源或安全令牌无效"))
			return
		}
		next(w, q)
	}
}

type protectedHandler func(http.ResponseWriter, *http.Request, auth.Principal, string)

func (r *router) protected(next protectedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, q *http.Request) {
		cookie, err := q.Cookie(sessionCookie)
		if err != nil {
			fail(w, q, auth.ErrUnauthenticated)
			return
		}
		p, err := r.d.Auth.Authenticate(q.Context(), cookie.Value)
		if err != nil {
			fail(w, q, err)
			return
		}
		if q.Method != "GET" && (!r.originAllowed(q) || !r.d.Auth.VerifyCSRF(cookie.Value, q.Header.Get("X-CSRF-Token"))) {
			fail(w, q, fault.New(403, "csrf_rejected", "请求来源或安全令牌无效"))
			return
		}
		next(w, q, p, cookie.Value)
	}
}
func (r *router) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: r.secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
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
	if !r.allow("setup:" + q.RemoteAddr) {
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
	peer, _, err := net.SplitHostPort(q.RemoteAddr)
	if err != nil {
		peer = q.RemoteAddr
	}
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
		Grants []auth.Grant `json:"grants"`
	}
	if err := decode(w, q, &in); err != nil {
		fail(w, q, err)
		return
	}
	if err := r.d.Auth.SetGrants(q.Context(), p, userID, version, in.Grants); err != nil {
		fail(w, q, err)
		return
	}
	// The transaction revoked the edited user's sessions. Do not re-authorize a
	// successful self-edit using the now-invalid principal.
	if userID == p.UserID {
		r.setCookie(w, sessionCookie, "", -1)
	}
	respond(w, q, 200, in.Grants)
}
