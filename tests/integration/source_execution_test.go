package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/jobs"
)

func TestSourceSessionIntentCannotCrossChannel(t *testing.T) {
	f, _, change, _ := prepareControlledSourceTest(t)
	ctx := context.Background()
	var ch id.ID
	if err := f.DB.Pool.QueryRow(ctx, "SELECT id FROM channels WHERE channel_no=2").Scan(&ch); err != nil {
		t.Fatal(err)
	}
	revision, err := f.Service.Sources.CreateDraft(ctx, f.Admin, ch, 1, channel.DraftInput{Config: channel.SourceConfig{IP: "192.168.33.20", MainPath: "/main"}, IdentityIntent: "replace", Credentials: channel.CredentialInput{PasswordAction: "clear"}})
	if err != nil {
		t.Fatal(err)
	}
	session, _ := id.New()
	_, err = f.DB.Pool.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,source_test_id,operation_role) VALUES($1,$2,$3,1,'one_nvr',$1::uuid::text,'test',$4,'test_main')`, session, ch, revision.ID, change.TestID)
	if err == nil {
		t.Fatal("cross-channel test intent accepted")
	}
}

func TestSourceExecutionRejectsLeaseTakeoverAndConnectionLoss(t *testing.T) {
	for _, fault := range []string{"fence", "connection"} {
		t.Run(fault, func(t *testing.T) {
			f, _, change, _ := prepareControlledSourceTest(t)
			ctx := context.Background()
			lease, err := (jobs.Repository{DB: f.DB}).Claim(ctx, "source.test")
			if err != nil {
				t.Fatal(err)
			}
			e, err := channel.NewExecution(ctx, f.DB, lease, f.Channel)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			other, err := channel.NewExecution(ctx, f.DB, lease, f.Channel)
			if other != nil {
				other.Close()
			}
			if !errors.Is(err, channel.ErrExecutionBusy) {
				t.Fatal("channel not serialized", err)
			}
			if fault == "fence" {
				token, _ := id.New()
				if _, err := f.DB.Pool.Exec(ctx, "UPDATE jobs SET fencing_token=$2 WHERE id=$1", change.JobID, token); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.DB.Pool.Exec(ctx, "SELECT pg_terminate_backend($1)", e.Conn.Conn().PgConn().PID()); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			err = e.WithinTx(ctx, func(context.Context, pgx.Tx) error { called = true; return nil })
			if err == nil || called {
				t.Fatal("lost execution admitted domain mutation", err)
			}
			select {
			case <-e.Context().Done():
			case <-time.After(4 * time.Second):
				t.Fatal("lost execution not cancelled")
			}
		})
	}
}

func TestSourceExecutionOperationDeadlineDoesNotLoseOwner(t *testing.T) {
	f, _, _, _ := prepareControlledSourceTest(t)
	ctx := context.Background()
	lease, err := (jobs.Repository{DB: f.DB}).Claim(ctx, "source.test")
	if err != nil {
		t.Fatal(err)
	}
	e, err := channel.NewExecution(ctx, f.DB, lease, f.Channel)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancel()
	if err := e.Check(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired operation admitted", err)
	}
	if err := e.Check(ctx); err != nil {
		t.Fatal("operation timeout lost live execution", err)
	}
}
