package integration

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/live"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

type liveMedia struct {
	mu        sync.Mutex
	ticket    string
	key       zlm.StreamKey
	closed    int
	negotiate func()
	closeErr  error
}

func (*liveMedia) Inspect(_ context.Context, k zlm.StreamKey) (zlm.StreamSnapshot, error) {
	return zlm.StreamSnapshot{Key: k, Tracks: []zlm.Track{{Codec: "H264", Ready: true}}}, nil
}
func (m *liveMedia) Negotiate(_ context.Context, k zlm.StreamKey, offer, ticket string) (string, zlm.RTCPeer, error) {
	m.ticket = ticket
	m.key = k
	if m.negotiate != nil {
		m.negotiate()
	}
	return "v=0\r\nanswer", zlm.RTCPeer{ID: "fixture-peer", Token: "private-delete"}, nil
}
func (m *liveMedia) CloseRTC(ctx context.Context, _ zlm.RTCPeer) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed++
	return m.closeErr
}

func TestLiveLeaseWithoutRecordingAndRevocation(t *testing.T) {
	ctx := context.Background()
	db, accounts, sites, admin := authFixture(t)
	var ch id.ID
	db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&ch)
	policy, _ := channel.ParseNetworkPolicy("192.168.33.0/24", nil)
	sources := channel.NewSources(db, accounts, sites.Secrets, policy)
	rev, err := sources.CreateDraft(ctx, admin.Principal, ch, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := id.New()
	_, err = db.Pool.Exec(ctx, `UPDATE channels SET current_revision_id=$2 WHERE id=$1;`, ch, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,vhost,app,stream,purpose,state) VALUES($1,$2,$3,1,'__defaultVhost__','one_nvr',$4,'main','active')`, sid, ch, rev.ID, string(sid))
	if err != nil {
		t.Fatal(err)
	}
	media := &liveMedia{}
	svc := &live.Service{DB: db, Auth: accounts, Media: media}
	out, err := svc.Open(ctx, admin.Principal, ch, "sub", "v=0\r\na=rtpmap:96 H264/90000\r\n")
	if err != nil || out.Stream != "main" || !out.Fallback {
		t.Fatalf("no-recording preview failed: %+v %v", out, err)
	}
	if err = svc.AuthorizePlay(ctx, media.key, media.ticket); err != nil {
		t.Fatal(err)
	}
	wrong := media.key
	other, _ := id.New()
	wrong.Stream = string(other)
	if svc.AuthorizePlay(ctx, wrong, media.ticket) == nil {
		t.Fatal("cross-stream ticket accepted")
	}
	if err = svc.Renew(ctx, admin.Principal, out.ID); err != nil {
		t.Fatal(err)
	}
	// Automatic lease renewal must not keep an idle login alive.
	if _, err = db.Pool.Exec(ctx, `UPDATE sessions SET last_seen_at=clock_timestamp()-interval '2 minutes' WHERE id=$1`, admin.Principal.SessionID); err != nil {
		t.Fatal(err)
	}
	var before, after time.Time
	db.Pool.QueryRow(ctx, `SELECT last_seen_at FROM sessions WHERE id=$1`, admin.Principal.SessionID).Scan(&before)
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com", Live: svc})
	req := httptest.NewRequest("POST", "http://nvr.example.com/api/v1/live-sessions/"+string(out.ID)+"/renew", bytes.NewBufferString("{}"))
	req.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: admin.RawSession})
	req.Header.Set("Origin", "http://nvr.example.com")
	req.Header.Set("X-CSRF-Token", accounts.CSRF(admin.RawSession))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("renew HTTP failed", w.Code, w.Body.String())
	}
	db.Pool.QueryRow(ctx, `SELECT last_seen_at FROM sessions WHERE id=$1`, admin.Principal.SessionID).Scan(&after)
	if !before.Equal(after) {
		t.Fatal("passive renewal refreshed login idle deadline")
	}
	req.Header.Set("X-CSRF-Token", "invalid")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("passive renewal skipped CSRF")
	}
	// A foreign close cannot affect this viewer.
	otherUser, _ := id.New()
	if err = svc.Close(ctx, auth.Principal{UserID: otherUser, SessionID: admin.Principal.SessionID}, out.ID); err != nil {
		t.Fatal(err)
	}
	if svc.AuthorizePlay(ctx, media.key, media.ticket) != nil {
		t.Fatal("foreign close affected viewer")
	}
	// Switching the current revision must invalidate old physical viewers.
	if _, err = db.Pool.Exec(ctx, `UPDATE channels SET current_revision_id=NULL WHERE id=$1`, ch); err != nil {
		t.Fatal(err)
	}
	if svc.AuthorizePlay(ctx, media.key, media.ticket) == nil {
		t.Fatal("old source survived switch")
	}
	if err = svc.Reap(ctx); err != nil || media.closed != 1 {
		t.Fatal("switch cleanup failed", err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE channels SET current_revision_id=$2 WHERE id=$1`, ch, rev.ID); err != nil {
		t.Fatal(err)
	}
	out, err = svc.Open(ctx, admin.Principal, ch, "main", "v=0\r\na=rtpmap:96 H264/90000\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE live_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, out.ID); err != nil {
		t.Fatal(err)
	}
	if svc.Renew(ctx, admin.Principal, out.ID) == nil {
		t.Fatal("expired viewer renewed")
	}
	if svc.AuthorizePlay(ctx, media.key, media.ticket) == nil {
		t.Fatal("expired ticket accepted")
	}
	if err = svc.Reap(ctx); err != nil || media.closed != 2 {
		t.Fatal("expired RTC not removed", err)
	}
	// Revocation DURING negotiation must close the late-created media peer.
	media.negotiate = func() {
		if _, err = db.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1`, admin.Principal.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = svc.Open(ctx, admin.Principal, ch, "main", "v=0\r\na=rtpmap:96 H264/90000\r\n"); err == nil || media.closed != 3 {
		t.Fatal("late RTC survived revocation", err)
	}
	media.negotiate = nil
	if _, err = db.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=NULL WHERE id=$1`, admin.Principal.SessionID); err != nil {
		t.Fatal(err)
	}
	out, err = svc.Open(ctx, admin.Principal, ch, "main", "v=0\r\na=rtpmap:96 H264/90000\r\n")
	if err != nil {
		t.Fatal(err)
	}
	media.closeErr = errors.New("temporary media failure")
	_, err = db.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE id=$1`, admin.Principal.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if svc.AuthorizePlay(ctx, media.key, media.ticket) == nil {
		t.Fatal("revoked session still playing")
	}
	if err = svc.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = db.Pool.QueryRow(ctx, `SELECT state FROM live_sessions WHERE id=$1`, out.ID).Scan(&state); err != nil || state != "closing" {
		t.Fatal("cleanup failure not durable", state, err)
	}
	media.closeErr = nil
	if _, err = db.Pool.Exec(ctx, `UPDATE live_sessions SET cleanup_after=clock_timestamp() WHERE id=$1`, out.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Reap(ctx); err != nil || media.closed != 5 {
		t.Fatalf("revoked RTC not removed: %v %d", err, media.closed)
	}
	var runs int
	db.Pool.QueryRow(ctx, "SELECT count(*) FROM recording_runs").Scan(&runs)
	if runs != 0 {
		t.Fatal("preview started recording")
	}
	// A full batch of unavailable old peers must not starve a later close.
	if _, err = db.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=NULL WHERE id=$1`, admin.Principal.SessionID); err != nil {
		t.Fatal(err)
	}
	media.closeErr = errors.New("old peers unavailable")
	for range 8 {
		viewer, err := svc.Open(ctx, admin.Principal, ch, "main", "v=0\r\na=rtpmap:96 H264/90000\r\n")
		if err != nil {
			t.Fatal(err)
		}
		if err = svc.Close(ctx, admin.Principal, viewer.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = svc.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	newer, err := svc.Open(ctx, admin.Principal, ch, "main", "v=0\r\na=rtpmap:96 H264/90000\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Close(ctx, admin.Principal, newer.ID); err != nil {
		t.Fatal(err)
	}
	media.closeErr = nil
	if err = svc.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT state FROM live_sessions WHERE id=$1`, newer.ID).Scan(&state); err != nil || state != "closed" {
		t.Fatal("old failures starved new peer", state, err)
	}
	// Persist UPDATE can time out after negotiation. Cleanup must use a fresh
	// context and still capture/remove the otherwise unregistered media peer.
	var locked id.ID
	unlocked := make(chan struct{})
	media.negotiate = func() {
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.QueryRow(ctx, `SELECT id FROM live_sessions WHERE state='pending' ORDER BY created_at DESC LIMIT 1 FOR UPDATE`).Scan(&locked); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		go func() { time.Sleep(5500 * time.Millisecond); tx.Rollback(ctx); close(unlocked) }()
	}
	closedBefore := media.closed
	if _, err = svc.Open(ctx, admin.Principal, ch, "main", "v=0\r\na=rtpmap:96 H264/90000\r\n"); err == nil {
		t.Fatal("persistence timeout not reported")
	}
	<-unlocked
	if media.closed != closedBefore+1 {
		t.Fatal("expired persistence context prevented RTC cleanup")
	}
	if err = db.Pool.QueryRow(ctx, `SELECT state FROM live_sessions WHERE id=$1`, locked).Scan(&state); err != nil || state != "closed" {
		t.Fatal("late unregistered RTC not cleaned", state, err)
	}
}

func TestLiveRequiresExplicitLivePermission(t *testing.T) {
	ctx := context.Background()
	db, accounts, _, admin := authFixture(t)
	var ch id.ID
	db.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=1").Scan(&ch)
	_, err := db.Pool.Exec(ctx, "UPDATE channel_grants SET live=false WHERE user_id=$1 AND channel_id=$2", admin.Principal.UserID, ch)
	if err != nil {
		t.Fatal(err)
	}
	svc := &live.Service{DB: db, Auth: accounts, Media: &liveMedia{}}
	_, err = svc.Open(ctx, admin.Principal, ch, "main", "v=0\r\na=rtpmap:96 H264/90000\r\n")
	if !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("configure granted playback: %v", err)
	}
}
