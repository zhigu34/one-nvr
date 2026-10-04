package integration

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"testing"
)

func TestRepeatedDeployPreservesState(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	s := tlsmanager.New(db, accounts, t.TempDir(), "https://nvr.example.com", "")
	if e := s.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.Bootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	a, e := s.Current(ctx, admin.Principal)
	if e != nil || a.DesiredID == nil {
		t.Fatalf("initial bootstrap %+v %v", a, e)
	}
	if e = s.Bootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	b, e := s.Current(ctx, admin.Principal)
	if e != nil || *b.DesiredID != *a.DesiredID || len(b.Certificates) != 1 {
		t.Fatal("repeat deploy changed certificate")
	}
	lease, e := (jobs.Repository{DB: db}).Claim(ctx, "tls.apply")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PrepareApply(ctx, lease); e != nil {
		t.Fatal("local activation incorrectly depends on auto renewal", e)
	}
	var n int
	if e = db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='tls.apply'").Scan(&n); e != nil || n != 1 {
		t.Fatal("repeat bootstrap queued duplicates")
	}
}

func TestHTTPSBootstrapReusesHTTPCandidate(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	plain := tlsmanager.New(db, accounts, root, "http://nvr.example.com", "")
	if e := plain.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	chain, key := tlsPair(t, 77)
	candidate, e := plain.Import(ctx, admin.Principal, chain, key)
	if e != nil {
		t.Fatal(e)
	}
	state, e := plain.Current(ctx, admin.Principal)
	if e != nil {
		t.Fatal(e)
	}
	// Simulate persisted paused monitoring while activating a manual candidate.
	if _, e = db.Pool.Exec(ctx, "UPDATE gateway_tls_state SET auto_apply=false WHERE singleton"); e != nil {
		t.Fatal(e)
	}
	secure := tlsmanager.New(db, accounts, root, "https://nvr.example.com", "")
	if e = secure.Initialize(ctx); e != nil {
		t.Fatal(e)
	}
	if e = secure.Bootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	state, e = secure.Current(ctx, admin.Principal)
	if e != nil || state.DesiredID == nil || *state.DesiredID != candidate.ID {
		t.Fatal("saved HTTP candidate did not bootstrap HTTPS", e)
	}
	if e = secure.Bootstrap(ctx); e != nil {
		t.Fatal(e)
	}
	lease, e := (jobs.Repository{DB: db}).Claim(ctx, "tls.apply")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = secure.PrepareApply(ctx, lease); e != nil {
		t.Fatal("local activation incorrectly depends on auto renewal", e)
	}
	var n int
	if e = db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='tls.apply'").Scan(&n); e != nil || n != 1 || len(state.Certificates) != 1 {
		t.Fatal("candidate identity or unique application changed")
	}
}
