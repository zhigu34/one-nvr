package integration

import (
	"context"
	"testing"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/operations"
)

// The audit page answers "who changed what". A row carrying only UUIDs answers
// nothing, which is what the table showed before: the operator column read
// `7b6fdf36-...` and the object column read a different UUID or "系统".
//
// This pins the two data facts the page depends on: the actor resolves to a
// username, and the object resolves to something a person can act on.
func TestAuditLogsResolveNamesForOperatorAndObject(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	ops := operations.New(db, accounts, false, false)

	var channelID id.ID
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM channels ORDER BY channel_no LIMIT 1").Scan(&channelID); err != nil {
		t.Fatal(err)
	}

	// One operator action against a channel, and one system action with no actor.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO audit_logs(id,actor_id,action,object_id) VALUES($1,$2,'channel.renamed',$3)`, mustID(t), admin.Principal.UserID, channelID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO audit_logs(id,actor_id,action,object_id) VALUES($1,NULL,'recording.deleted_local',NULL)`, mustID(t)); err != nil {
		t.Fatal(err)
	}

	page, err := ops.Audits(ctx, admin.Principal, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var sawChannel, sawSystem bool
	for _, item := range page.Items {
		switch item.Action {
		case "channel.renamed":
			sawChannel = true
			if item.ActorName != "admin" {
				t.Fatalf("actor_name = %q, want the username: the column showed a UUID "+
					"because the query never joined users", item.ActorName)
			}
			if item.ObjectType != "channel" {
				t.Fatalf("object_type = %q, want channel", item.ObjectType)
			}
			if item.ObjectLabel == "" || item.ObjectLabel[:2] != "CH" {
				t.Fatalf("object_label = %q, want the channel number: the object column "+
					"must not be a bare UUID", item.ObjectLabel)
			}
		case "recording.deleted_local":
			sawSystem = true
			if item.ActorName != "" {
				t.Fatalf("actor_name = %q for a system action, want empty", item.ActorName)
			}
			if item.ActorID != nil {
				t.Fatal("a system action must not carry an actor id")
			}
		}
	}
	if !sawChannel || !sawSystem {
		t.Fatalf("fixture rows missing: channel=%v system=%v", sawChannel, sawSystem)
	}
}

// The family filter narrows the view without deleting evidence.
func TestAuditLogsFilterByActionFamily(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	ops := operations.New(db, accounts, false, false)

	for _, action := range []string{"auth.login", "channel.renamed", "recording.deleted_local"} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO audit_logs(id,actor_id,action) VALUES($1,$2,$3)`, mustID(t), admin.Principal.UserID, action); err != nil {
			t.Fatal(err)
		}
	}

	page, err := ops.Audits(ctx, admin.Principal, "channel", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Action != "channel.renamed" {
		t.Fatalf("family filter returned %d items, want the single channel action", len(page.Items))
	}

	// An unknown family is rejected rather than treated as a wildcard: the value
	// ends up in a LIKE pattern, and a caller must not shape that pattern.
	if _, err := ops.Audits(ctx, admin.Principal, "ch%", "", 50); err == nil {
		t.Fatal("a wildcard family was accepted")
	}
}

func mustID(t *testing.T) id.ID {
	t.Helper()
	value, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
