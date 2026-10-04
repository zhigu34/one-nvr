package integration

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func testDB(t *testing.T) *database.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || dsn == "" || !strings.HasSuffix(u.Path, "/one_nvr_test") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "postgres") {
		t.Fatal("requires isolated TEST_DATABASE_URL ending /one_nvr_test; no production DATABASE_URL fallback")
	}
	ctx := context.Background()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	i, _ := id.New()
	schema := "test_" + strings.ReplaceAll(string(i), "-", "")
	if _, err = db.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		db.Pool.Close()
		t.Fatal(err)
	}
	cfg := db.Pool.Config()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	isolated, err := database.OpenConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		isolated.Pool.Close()
		_, err := db.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		db.Pool.Close()
		if err != nil {
			t.Error(err)
		}
	})
	return isolated
}
func TestMigrationConcurrentAndChecksum(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- database.Migrate(ctx, db) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil || n != 4 {
		t.Fatalf("migrations=%d err=%v", n, err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE schema_migrations SET checksum='changed'"); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err == nil {
		t.Fatal("accepted changed executed migration checksum")
	}
}
func TestTxRollsBackJobAndAudit(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	err := db.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := jobs.Enqueue(ctx, tx, jobs.Input{Kind: "pool.check", IdempotencyKey: "rollback", Payload: []byte(`{"pool":"test"}`)}); err != nil {
			return err
		}
		if err := audit.Append(ctx, tx, audit.Entry{Action: "pool.check.requested"}); err != nil {
			return err
		}
		return errors.New("cancel transaction")
	})
	if err == nil {
		t.Fatal("rollback trigger ignored")
	}
	for _, table := range []string{"jobs", "audit_logs"} {
		var n int
		if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("partial %s: %d %v", table, n, err)
		}
	}
}
func enqueue(t *testing.T, db *database.DB, key string, payload []byte) id.ID {
	t.Helper()
	var result id.ID
	err := db.WithinTx(context.Background(), func(tx pgx.Tx) error {
		var err error
		result, err = jobs.Enqueue(context.Background(), tx, jobs.Input{Kind: "pool.check", IdempotencyKey: key, Payload: payload})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestExpiredLeaseCannotCommit(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	jobID := enqueue(t, db, "lease", []byte(`{"pool":"a"}`))
	if got := enqueue(t, db, "lease", []byte(`{"pool":"a"}`)); got != jobID {
		t.Fatal("idempotent retry changed ID")
	}
	if err := db.WithinTx(ctx, func(tx pgx.Tx) error {
		_, err := jobs.Enqueue(ctx, tx, jobs.Input{Kind: "pool.check", IdempotencyKey: "lease", Payload: []byte(`{"pool":"b"}`)})
		return err
	}); !errors.Is(err, jobs.ErrIdempotencyConflict) {
		t.Fatalf("different payload accepted: %v", err)
	}
	repo := jobs.Repository{DB: db}
	old, err := repo.Claim(ctx, "pool.check")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Renew(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	next, err := repo.Claim(ctx, "pool.check")
	if err != nil {
		t.Fatal(err)
	}
	if next.FencingToken == old.FencingToken || next.Attempt != 2 {
		t.Fatal("lease reused fencing token")
	}
	if err := repo.Complete(ctx, old, jobs.Result{}); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale completion accepted %v", err)
	}
	if err := repo.Renew(ctx, old); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale renewal accepted %v", err)
	}
	if err := repo.Complete(ctx, next, jobs.Result{Payload: []byte(`{"checked":true}`)}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", jobID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatal(fmt.Sprintf("state=%s err=%v", state, err))
	}
}

func TestExpiredLastAttemptBecomesFailed(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	jobID := enqueue(t, db, "exhausted", []byte(`{}`))
	if _, err := db.Pool.Exec(ctx, "UPDATE jobs SET max_attempts=1 WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: db}
	if _, err := repo.Claim(ctx, "pool.check"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, "pool.check"); !errors.Is(err, jobs.ErrNoJob) {
		t.Fatalf("exhausted job reclaimed: %v", err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", jobID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("expired last attempt stranded in %s", state)
	}
}
func TestAuditRefusesSecretFields(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"nested":{"password":"secret"}}`, `{"camera":"rtsp://user:pass@host/main"}`, `{"certificate":"-----BEGIN PRIVATE KEY-----"}`} {
		err := db.WithinTx(ctx, func(tx pgx.Tx) error {
			return audit.Append(ctx, tx, audit.Entry{Action: "source.updated", Details: []byte(payload)})
		})
		if err == nil {
			t.Fatal("sensitive audit committed")
		}
	}
	var count int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs").Scan(&count); err != nil || count != 0 {
		t.Fatalf("secret audit rows %d err %v", count, err)
	}
}

func TestReadinessRequiresRecordedMigrations(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Ready(ctx, db); err == nil {
		t.Fatal("empty schema declared ready")
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := database.Ready(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE schema_migrations SET checksum='wrong'"); err != nil {
		t.Fatal(err)
	}
	if err := database.Ready(ctx, db); err == nil {
		t.Fatal("incompatible schema declared ready")
	}
}

func TestRetryBackoffAndConcurrentClaim(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	jobID := enqueue(t, db, "retry", []byte(`{}`))
	repo := jobs.Repository{DB: db}
	for index, want := range []float64{2, 4, 8, 16, 30} {
		lease, err := repo.Claim(ctx, "pool.check")
		if err != nil {
			t.Fatal(err)
		}
		if lease.Attempt != index+1 {
			t.Fatal("incorrect attempt number")
		}
		if err := repo.Fail(ctx, lease, "dependency_unavailable"); err != nil {
			t.Fatal(err)
		}
		var delay float64
		if err := db.Pool.QueryRow(ctx, "SELECT extract(epoch FROM available_at-updated_at) FROM jobs WHERE id=$1", jobID).Scan(&delay); err != nil {
			t.Fatal(err)
		}
		if delay < want-0.1 || delay > want+0.1 {
			t.Fatalf("retry %d delay %f want %f", index, delay, want)
		}
		if _, err := repo.Claim(ctx, "pool.check"); !errors.Is(err, jobs.ErrNoJob) {
			t.Fatal("claimed job before backoff elapsed")
		}
		if _, err := db.Pool.Exec(ctx, "UPDATE jobs SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1", jobID); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := repo.Claim(ctx, "pool.check")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Fail(ctx, lease, "dependency_unavailable"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.Pool.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", jobID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("retry limit not terminal")
	}
	for i := range 8 {
		enqueue(t, db, fmt.Sprintf("claim-%d", i), []byte(`{}`))
	}
	var wg sync.WaitGroup
	leases := make(chan jobs.Lease, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := repo.Claim(ctx, "pool.check")
			if err != nil {
				errs <- err
				return
			}
			leases <- l
		}()
	}
	wg.Wait()
	close(leases)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[id.ID]bool{}
	for l := range leases {
		if seen[l.ID] {
			t.Fatal("concurrent workers received same job")
		}
		seen[l.ID] = true
	}
	if len(seen) != 8 {
		t.Fatal("missing claimed jobs")
	}
}
func TestLeaseLossCancelsExecutor(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	jobID := enqueue(t, db, "cancel", []byte(`{}`))
	repo := jobs.Repository{DB: db}
	lease, err := repo.Claim(ctx, "pool.check")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- jobs.Execute(ctx, repo, lease, func(ctx context.Context, l jobs.Lease) (jobs.Result, error) {
			close(started)
			<-ctx.Done()
			return jobs.Result{}, ctx.Err()
		})
	}()
	<-started
	if _, err := db.Pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, jobs.ErrLeaseLost) {
			t.Fatalf("lease loss result %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("executor continued after lost lease")
	}
	var state string
	if err := db.Pool.QueryRow(ctx, "SELECT state FROM jobs WHERE id=$1", jobID).Scan(&state); err != nil || state != "running" {
		t.Fatal("lost executor committed job state")
	}
}
