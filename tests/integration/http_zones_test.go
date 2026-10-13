package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/detection"
	"github.com/zhigu34/one-nvr/internal/httpapi"
)

// The zone canvas reads a still from the detector and draws on it; the route
// must therefore carry a permission, refuse a channel the detector does not
// watch, and pass the image through untouched.
func TestZoneAndFrameRoutesNeedConfigureGrant(t *testing.T) {
	fixture, eventService := newEventsFixture(t)
	ctx := context.Background()
	detector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("frame-bytes"))
	}))
	defer detector.Close()
	zones := detection.New(fixture.DB, fixture.Auth, detector.URL, "/dev/null")
	handler := httpapi.NewHandler(httpapi.Dependencies{
		Auth: fixture.Auth, Site: fixture.Site, Events: eventService, Detection: zones,
		PublicURL: "http://nvr.example.com",
	})
	request := func(method, url, body, raw string) *httptest.ResponseRecorder {
		var reader *bytes.Buffer
		if body == "" {
			reader = bytes.NewBuffer(nil)
		} else {
			reader = bytes.NewBufferString(body)
		}
		r := httptest.NewRequest(method, "http://nvr.example.com"+url, reader)
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: raw})
		r.Header.Set("Origin", "http://nvr.example.com")
		r.Header.Set("X-CSRF-Token", fixture.Auth.CSRF(raw))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	// A viewer holds no configure grant: the drawings of a channel it can watch
	// are not its business, and the list answers empty rather than leaking.
	viewer, err := fixture.Auth.CreateUser(ctx, fixture.Admin, auth.CreateUserInput{Username: "zone_viewer", Password: testPassword, Role: "viewer"})
	if err != nil {
		t.Fatal(err)
	}
	viewerLogin, err := fixture.Auth.Login(ctx, "zone_viewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO channel_grants(user_id,channel_id,playback) VALUES($1,$2,true)`, viewer.ID, fixture.Channel); err != nil {
		t.Fatal(err)
	}
	channelQuery := "?channel_id=" + string(fixture.Channel)
	if w := request("GET", "/api/v1/detection/zones"+channelQuery, "", viewerLogin.RawSession); w.Code != 404 && w.Code != 403 {
		t.Fatalf("a viewer must not list zones: %d %s", w.Code, w.Body.String())
	}
	if w := request("GET", "/api/v1/detection/frame"+channelQuery, "", viewerLogin.RawSession); w.Code != 404 && w.Code != 403 {
		t.Fatalf("a viewer must not read the detection frame: %d %s", w.Code, w.Body.String())
	}

	// An admin saves a zone; the reference geometry comes from the channel, so
	// the body carries only the drawing.
	save := `{"channel_id":"` + string(fixture.Channel) + `","name":"门口","coordinates":[{"x":0.1,"y":0.1},{"x":0.4,"y":0.1},{"x":0.4,"y":0.4}],"objects":["person"]}`
	w := request("POST", "/api/v1/detection/zones", save, fixture.Session)
	if w.Code != 200 {
		t.Fatalf("saving a zone failed: %d %s", w.Code, w.Body.String())
	}
	var saved struct {
		Data detection.Zone `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Data.Name != "门口" || len(saved.Data.Coordinates) != 3 || saved.Data.NeedsCalibration {
		t.Fatal("the stored zone must round-trip", saved.Data)
	}
	// A self-intersecting polygon is refused with a code the UI can explain.
	bowtie := `{"channel_id":"` + string(fixture.Channel) + `","name":"蝴蝶结","coordinates":[{"x":0.1,"y":0.1},{"x":0.4,"y":0.4},{"x":0.4,"y":0.1},{"x":0.1,"y":0.4}]}`
	if w := request("POST", "/api/v1/detection/zones", bowtie, fixture.Session); w.Code != 422 {
		t.Fatalf("a crossing polygon must be refused: %d %s", w.Code, w.Body.String())
	}

	// Without enabled detection the detector has no camera for the channel, so
	// there is no honest frame to show.
	if w := request("GET", "/api/v1/detection/frame"+channelQuery, "", fixture.Session); w.Code != 409 {
		t.Fatalf("a channel without enabled detection has no frame: %d %s", w.Code, w.Body.String())
	}
	if _, err := zones.Upsert(ctx, fixture.Admin, detection.Config{
		ChannelID: fixture.Channel, Enabled: true, Stream: "sub", FPS: 5, Labels: []string{"person"},
	}); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/api/v1/detection/frame"+channelQuery, "", fixture.Session)
	if w.Code != 200 || w.Body.String() != "frame-bytes" || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("the still must pass through untouched: %d %s", w.Code, w.Body.String())
	}
	// The image is read for the operator on demand; caching it would show a
	// stale view as if it were current.
	if cache := w.Header().Get("Cache-Control"); cache != "no-store" {
		t.Fatalf("the frame must not be cached, got %q", cache)
	}

	// The rule list is readable with playback (the event viewer needs to see
	// what fired) but only configurable with the configure grant.
	if w := request("GET", "/api/v1/detection/rules"+channelQuery, "", viewerLogin.RawSession); w.Code != 200 {
		t.Fatalf("a playback holder may list rules: %d %s", w.Code, w.Body.String())
	}
	rule := `{"channel_id":"` + string(fixture.Channel) + `","name":"任何人","kind":"appear","categories":["person"],"severity":"low","enabled":true}`
	if w := request("POST", "/api/v1/detection/rules", rule, viewerLogin.RawSession); w.Code != 403 && w.Code != 404 {
		t.Fatalf("a viewer must not save a rule: %d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/v1/detection/rules", rule, fixture.Session); w.Code != 200 {
		t.Fatalf("an admin may save a rule: %d %s", w.Code, w.Body.String())
	}
	// A rule that could never fire is refused at the HTTP edge as well.
	broken := `{"channel_id":"` + string(fixture.Channel) + `","name":"没有区域","kind":"enter","categories":["person"],"severity":"low","enabled":true}`
	if w := request("POST", "/api/v1/detection/rules", broken, fixture.Session); w.Code != 422 {
		t.Fatalf("an unusable rule must be refused: %d %s", w.Code, w.Body.String())
	}
}
