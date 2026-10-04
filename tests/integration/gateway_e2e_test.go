//go:build gateway_runtime

package integration

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"github.com/zhigu34/one-nvr/tests/testcerts"
	"os"
	"path/filepath"
	"testing"
)

// Provision only private infrastructure. The browser must initialize the site.
func TestGatewayTLSRuntimeE2EInit(t *testing.T) {
	if os.Getenv("ONE_NVR_GATEWAY_RUNTIME") != "isolated" {
		t.Fatal("requires isolated runtime")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, os.Getenv("ONE_NVR_DATABASE_URL"))
	if err != nil {
		t.Fatal("isolated database unavailable")
	}
	defer db.Pool.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	secret, err := secrets.Init("/data")
	if err != nil {
		t.Fatal(err)
	}
	accounts := auth.NewService(db, secret, auth.NewPasswordHasher(2))
	if err = accounts.ApplyEntryProtocol(ctx, "http"); err != nil {
		t.Fatal(err)
	}
	svc := tlsmanager.New(db, accounts, "/data", "http://gateway", os.Getenv("ONE_NVR_TLS_DIR"))
	if err = svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	token, err := secret.ComponentCredential("gateway")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile("/data/gateway/proxy-token", []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	chain, key, err := testcerts.Pair("gateway", 401)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"/data/e2e", "/tls-input"} {
		if err = os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "fullchain.pem"), []byte(chain), 0600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "privkey.pem"), []byte(key), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var exists bool
	if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sites)").Scan(&exists); err != nil || exists {
		t.Fatal("fixture must have no site")
	}
}

// Activate the existing browser-imported certificate before changing this
// isolated fixture to HTTPS. No production credentials or database are used.
func TestGatewayTLSRuntimeE2EProtocolInit(t *testing.T) {
	if os.Getenv("ONE_NVR_GATEWAY_RUNTIME") != "isolated" {
		t.Fatal("requires isolated runtime")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, os.Getenv("ONE_NVR_DATABASE_URL"))
	if err != nil {
		t.Fatal("isolated database unavailable")
	}
	defer db.Pool.Close()
	secret, err := secrets.Load("/data")
	if err != nil {
		t.Fatal(err)
	}
	accounts := auth.NewService(db, secret, auth.NewPasswordHasher(2))
	login, err := accounts.Login(ctx, "admin", "Browser-new-only-2026!")
	if err != nil {
		t.Fatal("isolated administrator login unavailable")
	}
	svc := tlsmanager.New(db, accounts, "/data", "https://gateway", "")
	if err = svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := svc.Current(ctx, login.Principal)
	if err != nil || state.CandidateID == nil {
		t.Fatal("isolated candidate missing")
	}
	if _, err = svc.RequestApply(ctx, login.Principal, *state.CandidateID, state.Version); err != nil {
		t.Fatal(err)
	}
}
