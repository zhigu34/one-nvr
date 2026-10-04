//go:build gateway_runtime

package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/gatewaycontrol"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/site"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"github.com/zhigu34/one-nvr/tests/testcerts"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests use the actual API/Worker/Nginx processes and dedicated volumes.
// Missing Docker fixtures are a failure, never a skipped acceptance test.
type runtimeFixture struct {
	db       *database.DB
	tls      *tlsmanager.Service
	accounts *auth.Service
	p        auth.Principal
	ctx      context.Context
}

const runtimePassword = "Runtime-test-only-2026!"

func runtimeTLS(t *testing.T, initialize bool) runtimeFixture {
	t.Helper()
	if os.Getenv("ONE_NVR_GATEWAY_RUNTIME") != "isolated" {
		t.Fatal("requires isolated gateway runtime Compose project")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, os.Getenv("ONE_NVR_DATABASE_URL"))
	if err != nil {
		t.Fatal("runtime database unavailable")
	}
	t.Cleanup(db.Pool.Close)
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	secret, err := secrets.Init("/data")
	if err != nil {
		t.Fatal(err)
	}
	passwords := auth.NewPasswordHasher(2)
	accounts := auth.NewService(db, secret, passwords)
	if err = accounts.ApplyEntryProtocol(ctx, "https"); err != nil {
		t.Fatal(err)
	}
	s := &site.Service{DB: db, Auth: accounts, Secrets: secret, Passwords: passwords}
	initialized, err := s.Initialized(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !initialized {
		if _, err = s.Setup(ctx, site.SetupInput{Token: secret.SetupToken, AdminName: "runtime_admin", AdminPassword: runtimePassword, Name: "Isolated CI", Timezone: "Asia/Shanghai", ChannelCount: 16}); err != nil {
			t.Fatal(err)
		}
	}
	login, err := accounts.Login(ctx, "runtime_admin", runtimePassword)
	if err != nil {
		t.Fatal(err)
	}
	svc := tlsmanager.New(db, accounts, "/data", "https://127.0.0.1", "")
	if err = svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if initialize {
		token, err := secret.ComponentCredential("gateway")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile("/data/gateway/proxy-token", []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return runtimeFixture{db, svc, accounts, login.Principal, ctx}
}
func (f runtimeFixture) importPair(t *testing.T, serial int64) tlsmanager.Version {
	t.Helper()
	chain, key, err := testcerts.Pair("127.0.0.1", serial)
	if err != nil {
		t.Fatal(err)
	}
	version, err := f.tls.Import(f.ctx, f.p, chain, key)
	if err != nil {
		t.Fatal(err)
	}
	return version
}
func (f runtimeFixture) apply(t *testing.T, v tlsmanager.Version) id.ID {
	t.Helper()
	state, err := f.tls.Current(f.ctx, f.p)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.tls.RequestApply(f.ctx, f.p, v.ID, state.Version)
	if err != nil {
		t.Fatal(err)
	}
	return job
}
func runtimeEventually(t *testing.T, name string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(name + " timed out")
}
func (f runtimeFixture) waitActive(t *testing.T, v tlsmanager.Version, job id.ID) {
	t.Helper()
	runtimeEventually(t, "actual Nginx fingerprint and committed result", func() bool {
		var active *id.ID
		var state string
		if f.db.Pool.QueryRow(f.ctx, "SELECT active_id FROM gateway_tls_state WHERE singleton").Scan(&active) != nil || active == nil || *active != v.ID {
			return false
		}
		if f.db.Pool.QueryRow(f.ctx, "SELECT state FROM jobs WHERE id=$1", job).Scan(&state) != nil || state != "succeeded" {
			return false
		}
		return runtimeFingerprint() == v.LeafSHA256
	})
}
func runtimeFingerprint() string {
	conn, err := tls.Dial("tcp", "gateway:443", &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "127.0.0.1", InsecureSkipVerify: true})
	if err != nil {
		return ""
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return ""
	}
	sum := sha256.Sum256(certs[0].Raw)
	return hex.EncodeToString(sum[:])
}
func runtimePhase(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join("/data/test-phase", name), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
}
func waitPhase(t *testing.T, name string) {
	t.Helper()
	runtimeEventually(t, "host phase "+name, func() bool { _, err := os.Stat(filepath.Join("/data/test-phase", name)); return err == nil })
}

func TestGatewayTLSRuntimeInit(t *testing.T) {
	f := runtimeTLS(t, true)
	v := f.importPair(t, 100)
	f.apply(t, v)
}

func TestGatewayTLSRuntimeLifecycle(t *testing.T) {
	f := runtimeTLS(t, false)
	var firstID id.ID
	var firstJob id.ID
	if err := f.db.Pool.QueryRow(f.ctx, "SELECT desired_id FROM gateway_tls_state WHERE singleton").Scan(&firstID); err != nil {
		if err = f.db.Pool.QueryRow(f.ctx, "SELECT active_id FROM gateway_tls_state WHERE singleton").Scan(&firstID); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Pool.QueryRow(f.ctx, "SELECT id FROM jobs WHERE object_id=$1 AND kind='tls.apply' ORDER BY created_at LIMIT 1", firstID).Scan(&firstJob); err != nil {
		t.Fatal(err)
	}
	state, err := f.tls.Current(f.ctx, f.p)
	if err != nil {
		t.Fatal(err)
	}
	var first tlsmanager.Version
	for _, v := range state.Certificates {
		if v.ID == firstID {
			first = v
		}
	}
	if first.ID == "" {
		t.Fatal("initial certificate missing")
	}
	f.waitActive(t, first, firstJob)
	// Actual HTTPS auth and Secure cookie survive an ordinary renewal.
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 5 * time.Second, Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}}
	response, err := client.Get("https://gateway/api/v1/setup/status")
	if err != nil {
		t.Fatal("gateway HTTPS setup unavailable")
	}
	var status struct {
		Data struct {
			CSRF string `json:"csrf_token"`
		} `json:"data"`
	}
	err = json.NewDecoder(response.Body).Decode(&status)
	response.Body.Close()
	if err != nil || status.Data.CSRF == "" {
		t.Fatal("pre-auth handshake missing")
	}
	request, _ := http.NewRequest("POST", "https://gateway/api/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"runtime_admin","password":%q}`, runtimePassword)))
	request.Header.Set("Origin", "https://127.0.0.1")
	request.Header.Set("X-CSRF-Token", status.Data.CSRF)
	request.Header.Set("Content-Type", "application/json")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal("actual gateway login unavailable")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("actual gateway login status %d", response.StatusCode)
	}
	secureCookie := false
	for _, cookie := range response.Cookies() {
		if cookie.Name == "__Host-one_nvr_session" {
			secureCookie = cookie.Secure && cookie.HttpOnly && cookie.SameSite == http.SameSiteLaxMode
		}
	}
	if !secureCookie {
		t.Fatal("HTTPS session cookie missing protections")
	}
	next := f.importPair(t, 101)
	job := f.apply(t, next)
	f.waitActive(t, next, job)
	response, err = client.Get("https://gateway/api/v1/auth/me")
	if err != nil {
		t.Fatal("me unavailable after renewal")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("ordinary certificate renewal revoked session")
	}
	// A bad key is rejected before any application or listener change.
	chain, _, _ := testcerts.Pair("127.0.0.1", 102)
	_, key, _ := testcerts.Pair("127.0.0.1", 103)
	if _, err = f.tls.Import(f.ctx, f.p, chain, key); err == nil {
		t.Fatal("mismatched key accepted")
	}
	if runtimeFingerprint() != next.LeafSHA256 {
		t.Fatal("invalid pair changed listener")
	}
	before, err := f.tls.Current(f.ctx, f.p)
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := f.tls.RequestRollback(f.ctx, f.p, before.Version)
	if err != nil {
		t.Fatal(err)
	}
	f.waitActive(t, first, rollback)
	after, err := f.tls.Current(f.ctx, f.p)
	if err != nil || after.AutoApply {
		t.Fatal("actual rollback did not pause automatic application")
	}
	// Public static content is readable; private control is not served.
	response, err = client.Get("https://gateway/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("static index permissions invalid")
	}
	response, err = client.Get("https://gateway/control/proxy-token")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.Header.Get("Content-Type") == "application/octet-stream" {
		t.Fatal("private control exposed")
	}
	runtimePhase(t, "lifecycle-done")
	// Test host pauses the worker; gateway alone applies a durable authorized job.
	waitPhase(t, "worker-paused")
	target := f.importPair(t, 104)
	pending := f.apply(t, target)
	// Consume the last permitted generic attempt before the real gateway
	// switches. Recovery must still acquire a fenced reconciliation lease.
	if _, err = f.db.Pool.Exec(f.ctx, "UPDATE jobs SET max_attempts=1 WHERE id=$1", pending); err != nil {
		t.Fatal(err)
	}
	if _, err = (jobs.Repository{DB: f.db}).Claim(f.ctx, "tls.apply"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(f.ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", pending); err != nil {
		t.Fatal(err)
	}
	current, err := f.tls.Current(f.ctx, f.p)
	if err != nil {
		t.Fatal(err)
	}
	req := gatewaycontrol.ApplyRequest{JobID: pending, CertificateID: target.ID, ExpectedActiveID: current.ActiveID, Operation: "apply"}
	box := gatewaycontrol.Mailbox{Directory: "/data/gateway"}
	if err = box.Submit(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	receipt, err := box.Wait(ctx, req)
	if err != nil || receipt.State != "applied" || runtimeFingerprint() != target.LeafSHA256 {
		t.Fatal("real gateway did not apply while worker paused")
	}
	still, err := f.tls.Current(f.ctx, f.p)
	if err != nil || still.ActiveID == nil || *still.ActiveID != first.ID {
		t.Fatal("gateway receipt prematurely changed DB")
	}
	runtimePhase(t, "receipt-before-db")
	waitPhase(t, "gateway-recreated")
	f.waitActive(t, target, pending)
	var count int
	if err = f.db.Pool.QueryRow(f.ctx, "SELECT count(*) FROM audit_logs WHERE action='tls.applied' AND object_id=$1", target.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("recovery lost or duplicated commit")
	}
	// Replay must return the persisted receipt after container recreation.
	if err = box.Submit(f.ctx, req); err != nil {
		t.Fatal(err)
	}
	again, err := box.Wait(ctx, req)
	if err != nil || again.State != receipt.State || again.Evidence != receipt.Evidence || again.ActiveID == nil || *again.ActiveID != target.ID {
		t.Fatal("persistent receipt replay changed")
	}
	runtimePhase(t, "recovery-done")
	// SIGSTOP of the real master makes nginx -s reload return zero while old
	// workers still serve the previous certificate. The gateway must reject it.
	waitPhase(t, "nginx-stopped-for-failure")
	rejected := f.importPair(t, 105)
	failedJob := f.apply(t, rejected)
	runtimeEventually(t, "wrong real fingerprint rejected", func() bool {
		var state, code string
		return f.db.Pool.QueryRow(f.ctx, "SELECT state,COALESCE(error_code,'') FROM jobs WHERE id=$1", failedJob).Scan(&state, &code) == nil && state == "failed" && code == "verification_failed"
	})
	if runtimeFingerprint() != target.LeafSHA256 {
		t.Fatal("failed reload did not preserve actual old listener")
	}
	runtimePhase(t, "wrong-fingerprint-rejected")
	waitPhase(t, "nginx-stopped-for-crash")
	crashTarget := f.importPair(t, 106)
	crashJob := f.apply(t, crashTarget)
	runtimeEventually(t, "durable config switch before crash", func() bool {
		var in struct {
			Request  gatewaycontrol.ApplyRequest `json:"request"`
			Previous json.RawMessage             `json:"previous"`
			Expected json.RawMessage             `json:"expected"`
		}
		raw, err := os.ReadFile("/data/gateway/intent.json")
		if err != nil || json.Unmarshal(raw, &in) != nil || in.Request.JobID != crashJob {
			return false
		}
		config, err := os.ReadFile("/data/gateway/active.conf")
		return err == nil && strings.Contains(string(config), string(crashTarget.ID))
	})
	if runtimeFingerprint() != target.LeafSHA256 {
		t.Fatal("master was not stopped at the intended crash boundary")
	}
	runtimePhase(t, "config-switched-before-crash")
	waitPhase(t, "crashed-gateway-recreated")
	f.waitActive(t, crashTarget, crashJob)
	// An identical leaf served with a different issuer chain must actually reload.
	leaf, issuer, private := runtimeChainPair(t)
	short, err := f.tls.Import(f.ctx, f.p, leaf, private)
	if err != nil {
		t.Fatal(err)
	}
	shortJob := f.apply(t, short)
	f.waitActive(t, short, shortJob)
	full, err := f.tls.Import(f.ctx, f.p, append(leaf, issuer...), private)
	if err != nil {
		t.Fatal(err)
	}
	if full.LeafSHA256 != short.LeafSHA256 || full.ChainSHA256 == short.ChainSHA256 {
		t.Fatal("chain-only fixture invalid")
	}
	fullJob := f.apply(t, full)
	f.waitActive(t, full, fullJob)
	conn, err := tls.Dial("tcp", "gateway:443", &tls.Config{MinVersion: tls.VersionTLS12, ServerName: "127.0.0.1", InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	served := conn.ConnectionState().PeerCertificates
	conn.Close()
	der := make([][]byte, 0, len(served))
	for _, cert := range served {
		der = append(der, cert.Raw)
	}
	if tlsmanager.ChainFingerprint(der) != full.ChainSHA256 {
		t.Fatal("chain-only renewal was not served by actual Nginx")
	}
	runtimePhase(t, "crash-recovery-done")
}

func runtimeChainPair(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	issuerKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	issuer := &x509.Certificate{SerialNumber: big.NewInt(200), Subject: pkix.Name{CommonName: "CI temporary CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	issuerDER, err := x509.CreateCertificate(rand.Reader, issuer, issuer, issuerKey.Public(), issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(201), Subject: pkix.Name{CommonName: "127.0.0.1"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, issuer, leafKey.Public(), issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuerDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}
