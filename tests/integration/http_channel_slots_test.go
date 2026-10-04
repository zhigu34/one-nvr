package integration

import (
	"context"
	"encoding/json"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdministratorManagesSlotMetadataWithoutVideoGrant(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "DELETE FROM channel_grants WHERE user_id=$1", admin.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateUser(ctx, admin.Principal, auth.CreateUserInput{Username: "slotviewer", Password: testPassword, Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	viewer, err := accounts.Login(ctx, "slotviewer", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewHandler(httpapi.Dependencies{Auth: accounts, Site: sites, PublicURL: "http://nvr.example.com"})
	get := func(path, raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://nvr.example.com"+path, nil)
		r.AddCookie(&http.Cookie{Name: "one_nvr_session", Value: raw})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := get("/api/v1/channel-slots", admin.RawSession); w.Code != 200 {
		t.Fatal("administrator cannot list grantable slot identities", w.Code)
	} else {
		var envelope struct {
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || len(envelope.Data) != 16 {
			t.Fatal("slot metadata missing")
		}
		for _, slot := range envelope.Data {
			if len(slot) != 3 || slot["id"] == nil || slot["channel_no"] == nil || slot["channel_name"] == nil {
				t.Fatal("slot metadata contains unrelated video permissions")
			}
		}
	}
	if w := get("/api/v1/channel-slots", viewer.RawSession); w.Code != 403 {
		t.Fatal("viewer listed administrator slot metadata", w.Code)
	}
	if w := get("/api/v1/channels", admin.RawSession); w.Code != 200 {
		t.Fatal(w.Code)
	} else {
		var envelope struct {
			Data struct {
				Items []any `json:"items"`
			} `json:"data"`
		}
		if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || len(envelope.Data.Items) != 0 {
			t.Fatal("slot management granted implicit video access")
		}
	}
}
