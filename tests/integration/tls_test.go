package integration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"github.com/zhigu34/one-nvr/tests/testcerts"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tlsPair(t *testing.T, serial int64) ([]byte, []byte) {
	t.Helper()
	chain, key, err := testcerts.Pair("nvr.example.com", serial)
	if err != nil {
		t.Fatal(err)
	}
	return chain, key
}
func inputPair(t *testing.T, dir string, chain, key []byte) {
	t.Helper()
	for name, data := range map[string][]byte{"fullchain.pem": chain, "privkey.pem": key} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func stableTLSCheck(t *testing.T, s *tlsmanager.Service) tlsmanager.CheckResult {
	t.Helper()
	first, err := s.CheckDirectory(context.Background())
	if err != nil || first.State != "stabilizing" {
		t.Fatalf("initial directory check %+v %v", first, err)
	}
	time.Sleep(2050 * time.Millisecond)
	second, err := s.CheckDirectory(context.Background())
	if err != nil || second.State != "valid" {
		t.Fatalf("stable directory check %+v %v", second, err)
	}
	return second
}

func TestTLSPrivateMaterialNeverSerialized(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	data := t.TempDir()
	ctx := context.Background()
	svc := tlsmanager.New(db, accounts, data, "http://nvr.example.com", "")
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chain, key := tlsPair(t, 1)
	version, err := svc.Import(ctx, admin.Principal, chain, key)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Import(ctx, admin.Principal, chain, key)
	if err != nil || version.ID != again.ID {
		t.Fatal("duplicate import changed identity", err)
	}
	state, err := svc.Current(ctx, admin.Principal)
	if err != nil || state.ActiveID != nil || state.DesiredID != nil || state.CandidateID == nil {
		t.Fatal("import pretended active", err)
	}
	for _, dto := range []any{version, state} {
		encoded, err := json.Marshal(dto)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"PRIVATE KEY", "private_key", "content_digest", string(key)} {
			if strings.Contains(string(encoded), secret) {
				t.Fatal("private material leaked")
			}
		}
	}
	path := filepath.Join(data, "tls", "versions", string(version.ID))
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("version directory not private", err)
	}
	for _, name := range []string{"fullchain.pem", "privkey.pem"} {
		info, err := os.Stat(filepath.Join(path, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("snapshot file not private", err)
		}
	}
	if _, err := svc.RequestApply(ctx, admin.Principal, version.ID, state.Version); err == nil {
		t.Fatal("HTTP queued a TLS application")
	}
	var jobs int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='tls.apply'").Scan(&jobs); err != nil || jobs != 0 {
		t.Fatal("HTTP created apply job", err)
	}
}

func TestTLSDirectoryPendingPartialAndDuplicate(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	data := t.TempDir()
	input := t.TempDir()
	ctx := context.Background()
	svc := tlsmanager.New(db, accounts, data, "http://nvr.example.com", input)
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chain, key := tlsPair(t, 1)
	inputPair(t, input, chain, key)
	first := stableTLSCheck(t, svc)
	if first.JobID != nil {
		t.Fatal("HTTP automatically applies TLS")
	}
	again, err := svc.CheckDirectory(ctx)
	if err != nil || again.CertificateID == nil || *again.CertificateID != *first.CertificateID {
		t.Fatal("duplicate generated new version", err)
	}
	next, _ := tlsPair(t, 2)
	inputPair(t, input, next, key)
	bad, err := svc.CheckDirectory(ctx)
	if err != nil || bad.State != "unavailable" {
		t.Fatalf("half update %+v %v", bad, err)
	}
	if _, err := svc.CheckDirectory(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := svc.Current(ctx, admin.Principal)
	if err != nil || state.ConsecutiveErrors != 2 || len(state.Certificates) != 1 || *state.CandidateID != *first.CertificateID || state.ActiveID != nil {
		t.Fatalf("partial input changed saved version: %+v %v", state, err)
	}
	if _, err := svc.Import(ctx, admin.Principal, chain, key); err == nil {
		t.Fatal("manual upload allowed in directory mode")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs").Scan(&count); err != nil || count != 0 {
		t.Fatal("HTTP directory created job", err)
	}
}

func TestTLSRollbackAtomicallyPausesAndKeepsVersion(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	data := t.TempDir()
	ctx := context.Background()
	manual := tlsmanager.New(db, accounts, data, "https://nvr.example.com", "")
	if err := manual.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	a, ak := tlsPair(t, 1)
	b, bk := tlsPair(t, 2)
	older, err := manual.Import(ctx, admin.Principal, a, ak)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := manual.Import(ctx, admin.Principal, b, bk)
	if err != nil {
		t.Fatal(err)
	}
	// This seeds committed certificate history to test the request transaction.
	// Actual Nginx activation is deliberately a separate Task 7 container test.
	if _, err := db.Pool.Exec(ctx, "UPDATE gateway_tls_state SET active_id=$1,previous_id=$2,state='active'", newer.ID, older.ID); err != nil {
		t.Fatal(err)
	}
	input := t.TempDir()
	inputPair(t, input, b, bk)
	directory := tlsmanager.New(db, accounts, data, "https://nvr.example.com", input)
	if err := directory.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := directory.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := directory.RequestRollback(ctx, admin.Principal, state.Version-1); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale rollback accepted", err)
	}
	jobID, err := directory.RequestRollback(ctx, admin.Principal, state.Version)
	if err != nil {
		t.Fatal(err)
	}
	after, err := directory.Current(ctx, admin.Principal)
	if err != nil || after.AutoApply || after.DesiredID == nil || *after.DesiredID != older.ID || *after.ActiveID != newer.ID || after.State != "applying" {
		t.Fatalf("rollback request %+v %v", after, err)
	}
	var object id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT object_id FROM jobs WHERE id=$1 AND kind='tls.apply'", jobID).Scan(&object); err != nil || object != older.ID {
		t.Fatal("rollback target", err)
	}
	if err := directory.SetAutoApply(ctx, admin.Principal, after.Version-1, true); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale resume accepted", err)
	}
}

func TestTLSTamperedSnapshotCannotBeApplied(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	data := t.TempDir()
	ctx := context.Background()
	svc := tlsmanager.New(db, accounts, data, "https://nvr.example.com", "")
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chain, key := tlsPair(t, 1)
	version, err := svc.Import(ctx, admin.Principal, chain, key)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	other, otherKey := tlsPair(t, 2)
	inputPair(t, filepath.Join(data, "tls", "versions", string(version.ID)), other, otherKey)
	if _, err := svc.RequestApply(ctx, admin.Principal, version.ID, state.Version); err == nil {
		t.Fatal("tampered snapshot queued")
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='tls.apply'").Scan(&count); err != nil || count != 0 {
		t.Fatal("tampered version side effect", err)
	}
}

func TestTLSDirectoryAutoQueuesOneDurableApply(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	input := t.TempDir()
	svc := tlsmanager.New(db, accounts, t.TempDir(), "https://nvr.example.com", input)
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chain, key := tlsPair(t, 1)
	inputPair(t, input, chain, key)
	first := stableTLSCheck(t, svc)
	if first.JobID == nil {
		t.Fatal("validated auto apply not queued")
	}
	again, err := svc.CheckDirectory(ctx)
	if err != nil || again.JobID != nil {
		t.Fatal("duplicate queued competing application", err)
	}
	state, err := svc.Current(ctx, admin.Principal)
	if err != nil || state.State != "applying" || state.ActiveID != nil {
		t.Fatal("queue incorrectly marked active", err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='tls.apply'").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate tasks", err)
	}
}

func TestTLSAuditFailureRollsBackApplyIntent(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	svc := tlsmanager.New(db, accounts, t.TempDir(), "https://nvr.example.com", "")
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chain, key := tlsPair(t, 1)
	version, err := svc.Import(ctx, admin.Principal, chain, key)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "ALTER TABLE audit_logs ADD CONSTRAINT reject_tls_apply_audit CHECK(false) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RequestApply(ctx, admin.Principal, version.ID, state.Version); err == nil {
		t.Fatal("failed audit reported apply request success")
	}
	after, err := svc.Current(ctx, admin.Principal)
	if err != nil || after.DesiredID != nil || after.State != "pending" || after.Version != state.Version {
		t.Fatal("half apply intent committed", err)
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='tls.apply'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed transaction left apply job", err)
	}
}

func TestTLSPauseCancelsQueuedAutomaticApply(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	input := t.TempDir()
	svc := tlsmanager.New(db, accounts, t.TempDir(), "https://nvr.example.com", input)
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chain, key := tlsPair(t, 1)
	inputPair(t, input, chain, key)
	if stableTLSCheck(t, svc).JobID == nil {
		t.Fatal("automatic job missing")
	}
	state, err := svc.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetAutoApply(ctx, admin.Principal, state.Version, false); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='tls.apply' AND state IN ('queued','running')").Scan(&queued); err != nil || queued != 0 {
		t.Fatal("pause left automatic application executable", queued, err)
	}
	after, err := svc.Current(ctx, admin.Principal)
	if err != nil || after.AutoApply || after.State != "pending" || after.DesiredID != nil || after.CandidateID == nil {
		t.Fatal("pause lost candidate or kept apply intent", err)
	}
}
