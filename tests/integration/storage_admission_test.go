package integration

import (
	"context"
	"testing"

	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/site"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// The write-proof setting is a reliability preference, but it may not be used
// to remove the two process samples: those are what make a full disk
// observable, and losing them would let a site record into a full filesystem
// without stopping. These tests pin both halves of that boundary.
func TestStorageAdmissionRequiresProcessSamplesAtEverySetting(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	sites.Auth = accounts
	ctx := context.Background()
	current, err := sites.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if !current.RequireStorageWriteProof {
		t.Fatal("a new site must start with write proof required")
	}
	pool := seedPool(t, db, current.ID)

	// Default site: no evidence at all is not admissible.
	if ready, err := storage.PoolHasFreshEvidence(ctx, db.Pool, current.ID, pool); err != nil || ready {
		t.Fatalf("no evidence admitted: ready=%v err=%v", ready, err)
	}

	// Both process samples alone are not enough while write proof is required.
	seedPoolCheck(t, db, pool, "api", "healthy")
	seedPoolCheck(t, db, pool, "worker", "healthy")
	if ready, err := storage.PoolHasFreshEvidence(ctx, db.Pool, current.ID, pool); err != nil || ready {
		t.Fatalf("process samples admitted without write proof: ready=%v err=%v", ready, err)
	}

	// Adding the verified write completes the requirement.
	seedPoolCheck(t, db, pool, "zlm", "healthy")
	if ready, err := storage.PoolHasFreshEvidence(ctx, db.Pool, current.ID, pool); err != nil || !ready {
		t.Fatalf("all three peers not admitted: ready=%v err=%v", ready, err)
	}

	// Opting out drops only the write proof.
	off := false
	if _, err := sites.Update(ctx, admin.Principal, current.Version, site.UpdateInput{RequireStorageWriteProof: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE storage_pool_checks SET state='unavailable' WHERE pool_id=$1 AND service='zlm'", pool); err != nil {
		t.Fatal(err)
	}
	if ready, err := storage.PoolHasFreshEvidence(ctx, db.Pool, current.ID, pool); err != nil || !ready {
		t.Fatalf("write proof still required after opting out: ready=%v err=%v", ready, err)
	}

	// And the process samples stay mandatory.
	if _, err := db.Pool.Exec(ctx, "UPDATE storage_pool_checks SET state='unavailable' WHERE pool_id=$1 AND service='worker'", pool); err != nil {
		t.Fatal(err)
	}
	if ready, err := storage.PoolHasFreshEvidence(ctx, db.Pool, current.ID, pool); err != nil || ready {
		t.Fatalf("missing process sample admitted while opted out: ready=%v err=%v", ready, err)
	}
}

// A stale sample is not fresh evidence, at either setting: the TTL is what stops
// an old observation from authorizing a new write.
func TestStorageAdmissionIgnoresExpiredSamples(t *testing.T) {
	db, accounts, sites, admin := authFixture(t)
	sites.Auth = accounts
	ctx := context.Background()
	current, err := sites.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	pool := seedPool(t, db, current.ID)
	off := false
	if _, err := sites.Update(ctx, admin.Principal, current.Version, site.UpdateInput{RequireStorageWriteProof: &off}); err != nil {
		t.Fatal(err)
	}
	seedPoolCheck(t, db, pool, "api", "healthy")
	seedPoolCheck(t, db, pool, "worker", "healthy")
	if ready, err := storage.PoolHasFreshEvidence(ctx, db.Pool, current.ID, pool); err != nil || !ready {
		t.Fatalf("fresh samples not admitted: ready=%v err=%v", ready, err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE storage_pool_checks SET expires_at=clock_timestamp()-interval '1 second' WHERE pool_id=$1", pool); err != nil {
		t.Fatal(err)
	}
	if ready, err := storage.PoolHasFreshEvidence(ctx, db.Pool, current.ID, pool); err != nil || ready {
		t.Fatalf("expired samples admitted: ready=%v err=%v", ready, err)
	}
}

// Switching the setting must not disturb the channel and recording tables: it is
// an admission preference, not a migration of existing behaviour.
func TestStorageProofSettingRoundTripsThroughSiteUpdate(t *testing.T) {
	_, accounts, sites, admin := authFixture(t)
	sites.Auth = accounts
	ctx := context.Background()
	current, err := sites.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	updated, err := sites.Update(ctx, admin.Principal, current.Version, site.UpdateInput{RequireStorageWriteProof: &off})
	if err != nil {
		t.Fatal(err)
	}
	if updated.RequireStorageWriteProof {
		t.Fatal("opting out did not persist")
	}
	if updated.Version != current.Version+1 {
		t.Fatalf("version=%d want=%d", updated.Version, current.Version+1)
	}
	// A conflicting expected version must not silently overwrite.
	if _, err := sites.Update(ctx, admin.Principal, current.Version, site.UpdateInput{RequireStorageWriteProof: &off}); err == nil {
		t.Fatal("stale version accepted")
	}
	// Name and timezone updates keep the setting intact.
	name := "Renamed site"
	back, err := sites.Update(ctx, admin.Principal, updated.Version, site.UpdateInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if back.RequireStorageWriteProof || back.Name != name {
		t.Fatalf("update dropped the setting: %+v", back)
	}
}

func seedPool(t *testing.T, db *database.DB, siteID id.ID) id.ID {
	t.Helper()
	pool, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), "INSERT INTO storage_pools(id,site_id,name,canonical_path) VALUES($1,$2,$3,$4)", pool, siteID, "proof-disk", "/storage/proof-disk"); err != nil {
		t.Fatal(err)
	}
	return pool
}

func seedPoolCheck(t *testing.T, db *database.DB, pool id.ID, service, state string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO storage_pool_checks(pool_id,service,state,reason_code,observed_at,expires_at,total_bytes,free_bytes) VALUES($1,$2,$3,'seeded',clock_timestamp(),clock_timestamp()+interval '30 seconds',0,0) ON CONFLICT(pool_id,service) DO UPDATE SET state=EXCLUDED.state,observed_at=EXCLUDED.observed_at,expires_at=EXCLUDED.expires_at`, pool, service, state); err != nil {
		t.Fatal(err)
	}
}
