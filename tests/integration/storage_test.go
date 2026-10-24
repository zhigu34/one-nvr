package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/storage"
)

func TestPoolRootAndNestedConcurrency(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	base := t.TempDir()
	parent := filepath.Join(base, "disk")
	child := filepath.Join(parent, "nested")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	svc := storage.New(db, accounts, []string{base})
	ctx := context.Background()
	results := make(chan error, 2)
	for _, path := range []string{parent, child} {
		go func(path string) {
			_, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "disk", Path: path})
			results <- err
		}(path)
	}
	success := 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if !errors.Is(err, auth.ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("nested registration successes=%d", success)
	}
	page, err := svc.List(ctx, admin.Principal)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("pool list: %+v %v", page, err)
	}
	pool := page.Items[0]
	if _, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "duplicate", Path: pool.Path}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("duplicate accepted", err)
	}
	if _, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "outside", Path: t.TempDir()}); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("outside accepted", err)
	}
}

func TestPoolCheckQueueStoresIndependentEvidence(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	base := t.TempDir()
	path := filepath.Join(base, "disk")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := storage.New(db, accounts, []string{base})
	pool, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "disk", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.RequestCheck(ctx, admin.Principal, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.RequestCheck(ctx, admin.Principal, pool.ID)
	if err != nil || first != second {
		t.Fatal("in-flight check not coalesced", err)
	}
	if err := svc.Delete(ctx, admin.Principal, pool.ID, 1); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("active job pool deleted", err)
	}
	repo := jobs.Repository{DB: db}
	lease, err := repo.Claim(ctx, "storage.check")
	if err != nil {
		t.Fatal(err)
	}
	worker := storage.New(db, nil, []string{t.TempDir()}) // Its mount is independently unavailable.
	if err := jobs.Execute(ctx, repo, lease, worker.HandleCheck); err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	checks := page.Items[0].Checks
	if checks[0].State != "healthy" || checks[1].State != "unavailable" || checks[2].State != "pending" || page.Items[0].State == "ready" {
		t.Fatalf("fabricated readiness: %+v", page.Items[0])
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE storage_pool_checks SET expires_at=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	page, err = svc.List(ctx, admin.Principal)
	if err != nil || page.Capacity.FilesystemCount != 0 || page.Items[0].Checks[0].State != "expired" {
		t.Fatal("stale checks accepted", err)
	}
}

// A file the reconciler cannot examine must not stop it from examining the rest.
//
// The previous pass returned on the first error, so one unreadable file froze
// verification for every recording on the pool — and because it restarted from
// the first row on each request, it hit the same file forever. "Cannot read" is
// not a verdict: the row is left alone and retried, while the next file is still
// checked.
func TestStorageReconcileContinuesPastAnUnreadableFile(t *testing.T) {
	fixture := newPublicationFixture(t)
	ctx := context.Background()
	pools := storage.New(fixture.DB, fixture.Auth, []string{filepath.Dir(fixture.Pool.Path)})
	seed := func(relative string, verifiedAgo time.Duration) id.ID {
		t.Helper()
		segment, err := id.New()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_segments(
			id,channel_id,source_revision_id,run_id,pool_id,original_relative_path,target_relative_path,
			original_start,naming_timezone,utc_offset_seconds,start_at,end_at,size_bytes,state,ready_at)
			VALUES($1,$2,$3,$4,$5,$6,$6,$7,'UTC',0,$7,$8,1024,'ready',clock_timestamp())`,
			segment, fixture.Channel, fixture.Revision, fixture.Run, fixture.Pool.ID,
			relative, now.Add(-time.Minute), now); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.DB.Pool.Exec(ctx, `INSERT INTO recording_locations(
			segment_id,pool_id,relative_path,size_bytes,state,verified_at)
			VALUES($1,$2,$3,1024,'ready',clock_timestamp()-$4::interval)`,
			segment, fixture.Pool.ID, relative, verifiedAgo.String()); err != nil {
			t.Fatal(err)
		}
		return segment
	}
	// A directory where a file is expected is unreadable but definitely present:
	// exactly the shape that used to abort the pool scan.
	dirPath := "recordings/not-a-file.mp4"
	if err := os.MkdirAll(filepath.Join(fixture.Pool.Path, filepath.Dir(dirPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.Pool.Path, dirPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// The unreadable row sorts first, so the missing one is only reached if the
	// error did not end the pass.
	unreadable := seed(dirPath, 2*time.Hour)
	vanished := seed("recordings/gone.mp4", time.Hour)

	if _, err := pools.VerifyBatch(ctx, fixture.Pool, 10); err != nil {
		t.Fatal(err)
	}
	var unreadableState, vanishedState string
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT state FROM recording_segments WHERE id=$1`, unreadable).Scan(&unreadableState); err != nil {
		t.Fatal(err)
	}
	if unreadableState != "ready" {
		t.Fatalf("an unreadable file was given a verdict (%q); the evidence does not support one", unreadableState)
	}
	if err := fixture.DB.Pool.QueryRow(ctx,
		`SELECT state FROM recording_segments WHERE id=$1`, vanished).Scan(&vanishedState); err != nil {
		t.Fatal(err)
	}
	if vanishedState != "missing" {
		t.Fatalf("a file after the unreadable one was never checked (state %q); one bad file froze the pool", vanishedState)
	}
}

func TestPoolAuditFailureKeepsRecoverableIdentity(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	base := t.TempDir()
	path := filepath.Join(base, "disk")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	svc := storage.New(db, accounts, []string{base})
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, "ALTER TABLE audit_logs ADD CONSTRAINT reject_pool_audit CHECK(false) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "disk", Path: path}); err == nil {
		t.Fatal("audit failure committed registration")
	}
	identity, err := os.ReadFile(filepath.Join(path, ".one-nvr.json"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(ctx, admin.Principal)
	if err != nil || len(page.Items) != 0 {
		t.Fatal("failed transaction left pool row", err)
	}
	if _, err := db.Pool.Exec(ctx, "ALTER TABLE audit_logs DROP CONSTRAINT reject_pool_audit"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "disk", Path: path}); err != nil {
		t.Fatal("orphan identity not recoverable", err)
	}
	recovered, err := os.ReadFile(filepath.Join(path, ".one-nvr.json"))
	if err != nil || string(recovered) != string(identity) {
		t.Fatal("retry replaced identity", err)
	}
}

func TestPoolDeleteProtectsFilesAndPreservesIdentity(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	base := t.TempDir()
	path := filepath.Join(base, "disk")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	svc := storage.New(db, accounts, []string{base})
	ctx := context.Background()
	pool, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "disk", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "important.mp4")
	if err := os.WriteFile(file, []byte("fixture-video"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, admin.Principal, pool.ID, 1); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("nonempty pool deleted", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, admin.Principal, pool.ID, 2); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("stale deletion accepted", err)
	}
	if err := svc.Delete(ctx, admin.Principal, pool.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("mounted root deleted", err)
	}
	again, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "re-added", Path: path})
	if err != nil || again.ID != pool.ID {
		t.Fatal("empty pool lost identity", err)
	}
}

func TestPoolMissingIdentityRegistrationDoesNotRepair(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	base := t.TempDir()
	path := filepath.Join(base, "disk")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	svc := storage.New(db, accounts, []string{base})
	ctx := context.Background()
	pool, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "disk", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(pool.Path, ".one-nvr.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: "repair", Path: path}); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("duplicate repaired marker", err)
	}
	if err := svc.Sample(ctx, "api"); err != nil {
		t.Fatal(err)
	}
	page, err := svc.List(ctx, admin.Principal)
	if err != nil || page.Items[0].Checks[0].Reason != "identity_unavailable" {
		t.Fatal("identity loss not recorded", err)
	}
	if _, err := os.Stat(filepath.Join(path, ".one-nvr.json")); !os.IsNotExist(err) {
		t.Fatal("lost marker recreated")
	}
}

func TestConcurrentDefaultPoolUnique(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	base := t.TempDir()
	svc := storage.New(db, accounts, []string{base})
	ctx := context.Background()
	var pools []storage.Pool
	for _, name := range []string{"a", "b"} {
		path := filepath.Join(base, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		pool, err := svc.Register(ctx, admin.Principal, storage.RegisterInput{Name: name, Path: path})
		if err != nil {
			t.Fatal(err)
		}
		pools = append(pools, pool)
	}
	yes := true
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, pool := range pools {
		wg.Add(1)
		go func(pool storage.Pool) {
			defer wg.Done()
			_, err := svc.Update(ctx, admin.Principal, pool.ID, pool.Version, storage.UpdateInput{IsDefault: &yes})
			errs <- err
		}(pool)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, auth.ErrConflict) {
			t.Fatal(err)
		}
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM storage_pools WHERE is_default").Scan(&count); err != nil || count != 1 {
		t.Fatalf("default count %d %v", count, err)
	}
}
