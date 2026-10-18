package integration

import (
	"context"
	"testing"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/operations"
)

// SYS-01 asks for a 180-day audit window. Nothing ever deleted from
// audit_logs, so the table only grew — on a site whose log is dominated by
// playback entries that is tens of thousands of rows a year.
func TestSweepAuditsDropsRowsPastRetention(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	ops := operations.New(db, accounts, false, false)

	oldID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	freshID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	// One row past the window, one inside it. The boundary is what the test is
	// about: an off-by-one here either keeps a year of noise or deletes evidence
	// the PRD still requires.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO audit_logs(id,actor_id,action,created_at) VALUES($1,$2,'auth.login',clock_timestamp()-interval '181 days')`, oldID, admin.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO audit_logs(id,actor_id,action,created_at) VALUES($1,$2,'auth.login',clock_timestamp()-interval '179 days')`, freshID, admin.Principal.UserID); err != nil {
		t.Fatal(err)
	}

	ops.SweepAudits(ctx)

	var oldCount, freshCount int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE id=$1", oldID).Scan(&oldCount); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE id=$1", freshID).Scan(&freshCount); err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatalf("a %d-day-old audit row survived the sweep; the 180-day window "+
			"is the policy and nothing else removes these rows", 181)
	}
	if freshCount != 1 {
		t.Fatal("a 179-day-old audit row was deleted; the window is 180 days, not less")
	}
}

// The sweep must not touch anything else: it is scoped to the audit table, and
// deleting from the wrong place here would destroy operational history.
func TestSweepAuditsLeavesOtherTablesAlone(t *testing.T) {
	db, accounts, _, _ := authFixture(t)
	ctx := context.Background()
	ops := operations.New(db, accounts, false, false)

	before := map[string]int{}
	for _, table := range []string{"recording_segments", "fault_events", "reliability_intervals"} {
		var count int
		if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		before[table] = count
	}
	ops.SweepAudits(ctx)
	for table, want := range before {
		var count int
		if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("audit sweep changed %s: %d -> %d", table, want, count)
		}
	}
}

var _ = time.Now
