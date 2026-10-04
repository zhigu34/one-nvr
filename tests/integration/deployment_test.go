package integration

import (
	"context"
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
	var n int
	if e = db.Pool.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind='tls.apply'").Scan(&n); e != nil || n != 1 {
		t.Fatal("repeat bootstrap queued duplicates")
	}
}
