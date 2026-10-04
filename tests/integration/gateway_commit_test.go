package integration

import (
	"context"
	"errors"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/jobs"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"path/filepath"
	"strings"
	"testing"
)

func TestGatewayTLSReceiptFencedAndIdempotent(t *testing.T) {
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
	jobID, err := svc.RequestApply(ctx, admin.Principal, version.ID, state.Version)
	if err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: db}
	old, err := repo.Claim(ctx, "tls.apply")
	if err != nil {
		t.Fatal(err)
	}
	result := tlsmanager.GatewayResult{JobID: jobID, State: "applied", ActiveID: &version.ID, LeafSHA256: version.LeafSHA256, ChainSHA256: version.ChainSHA256}
	if _, err := db.Pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", jobID); err != nil {
		t.Fatal(err)
	}
	fresh, err := repo.Claim(ctx, "tls.apply")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CommitGatewayResult(ctx, old, result); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatal("old lease committed gateway evidence", err)
	}
	prepared, err := svc.PrepareApply(ctx, fresh)
	if err != nil || prepared.Metadata.ChainSHA256 != result.ChainSHA256 {
		t.Fatal("real snapshot not revalidated", err)
	}
	wrong := result
	wrong.ChainSHA256 = strings.Repeat("0", 64)
	if err := svc.CommitGatewayResult(ctx, fresh, wrong); !errors.Is(err, auth.ErrInvalid) {
		t.Fatal("unverified chain receipt accepted", err)
	}
	if err := svc.CommitGatewayResult(ctx, fresh, result); err != nil {
		t.Fatal(err)
	}
	if err := svc.CommitGatewayResult(ctx, fresh, result); err != nil {
		t.Fatal("receipt not idempotent", err)
	}
	replay, err := svc.PrepareApply(ctx, fresh)
	if err != nil || replay.CommittedResult == nil {
		t.Fatal("committed-but-job-incomplete recovery lost receipt", err)
	}
	after, err := svc.Current(ctx, admin.Principal)
	if err != nil || after.ActiveID == nil || *after.ActiveID != version.ID || after.State != "active" {
		t.Fatal("matching receipt not activated", err)
	}
	var audits int
	if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action='tls.applied'").Scan(&audits); err != nil || audits != 1 {
		t.Fatal("replay duplicated audit", err)
	}
}

func TestGatewayTLSContradictoryReceiptRejected(t *testing.T) {
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
	jobID, err := svc.RequestApply(ctx, admin.Principal, version.ID, state.Version)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := (jobs.Repository{DB: db}).Claim(ctx, "tls.apply")
	if err != nil {
		t.Fatal(err)
	}
	result := tlsmanager.GatewayResult{JobID: jobID, State: "applied", ActiveID: &version.ID, LeafSHA256: version.LeafSHA256, ChainSHA256: version.ChainSHA256}
	if err := svc.CommitGatewayResult(ctx, lease, result); err != nil {
		t.Fatal(err)
	}
	contradictory := tlsmanager.GatewayResult{JobID: jobID, State: "failed", ErrorCode: "verification_failed"}
	if err := svc.CommitGatewayResult(ctx, lease, contradictory); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("conflicting evidence silently accepted", err)
	}
}

func TestGatewayTLSPermanentFailureAndAuditRollback(t *testing.T) {
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
	jobID, err := svc.RequestApply(ctx, admin.Principal, version.ID, state.Version)
	if err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: db}
	lease, err := repo.Claim(ctx, "tls.apply")
	if err != nil {
		t.Fatal(err)
	}
	result := tlsmanager.GatewayResult{JobID: jobID, State: "failed", ErrorCode: "verification_failed"}
	if _, err := db.Pool.Exec(ctx, "ALTER TABLE audit_logs ADD CONSTRAINT reject_gateway_audit CHECK(false) NOT VALID"); err != nil {
		t.Fatal(err)
	}
	if err := svc.CommitGatewayResult(ctx, lease, result); err == nil {
		t.Fatal("audit failure committed gateway result")
	}
	after, err := svc.Current(ctx, admin.Principal)
	if err != nil || after.State != "applying" {
		t.Fatal("audit failure changed gateway state", err)
	}
	if _, err := db.Pool.Exec(ctx, "ALTER TABLE audit_logs DROP CONSTRAINT reject_gateway_audit"); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Execute(ctx, repo, lease, func(ctx context.Context, _ jobs.Lease) (jobs.Result, error) {
		if err := svc.CommitGatewayResult(ctx, lease, result); err != nil {
			return jobs.Result{}, err
		}
		return jobs.Result{}, &jobs.PermanentFailure{Code: result.ErrorCode}
	}); err != nil {
		t.Fatal(err)
	}
	var jobState string
	var attempts int
	if err := db.Pool.QueryRow(ctx, "SELECT state,attempt FROM jobs WHERE id=$1", jobID).Scan(&jobState, &attempts); err != nil || jobState != "failed" || attempts != 1 {
		t.Fatal("verified failure retried", jobState, attempts, err)
	}
	after, err = svc.Current(ctx, admin.Principal)
	if err != nil || after.State != "unavailable" || after.ActiveID != nil || after.ErrorCode == nil {
		t.Fatal("failed apply pretended active", err)
	}
}

func TestGatewayTLSNewIntentWaitsForPriorJobCompletion(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	svc := tlsmanager.New(db, accounts, t.TempDir(), "https://nvr.example.com", "")
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chain, key := tlsPair(t, 1)
	v, err := svc.Import(ctx, admin.Principal, chain, key)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	job, err := svc.RequestApply(ctx, admin.Principal, v.ID, state.Version)
	if err != nil {
		t.Fatal(err)
	}
	repo := jobs.Repository{DB: db}
	lease, err := repo.Claim(ctx, "tls.apply")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.CommitGatewayResult(ctx, lease, tlsmanager.GatewayResult{JobID: job, State: "failed", ErrorCode: "verification_failed"}); err != nil {
		t.Fatal(err)
	}
	state, err = svc.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RequestApply(ctx, admin.Principal, v.ID, state.Version); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("new intent raced unfinished old job", err)
	}
	if err = repo.FailPermanent(ctx, lease, "verification_failed"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RequestApply(ctx, admin.Principal, v.ID, state.Version); err != nil {
		t.Fatal("terminal old job blocks retry", err)
	}
}

func TestGatewayTLSInvalidQueuedSnapshotTerminatesPreparation(t *testing.T) {
	db, accounts, _, admin := authFixture(t)
	ctx := context.Background()
	data := t.TempDir()
	svc := tlsmanager.New(db, accounts, data, "https://nvr.example.com", "")
	if err := svc.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	chain, key := tlsPair(t, 1)
	v, err := svc.Import(ctx, admin.Principal, chain, key)
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.Current(ctx, admin.Principal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.RequestApply(ctx, admin.Principal, v.ID, state.Version); err != nil {
		t.Fatal(err)
	}
	other, otherKey := tlsPair(t, 2)
	inputPair(t, filepath.Join(data, "tls", "versions", string(v.ID)), other, otherKey)
	lease, err := (jobs.Repository{DB: db}).Claim(ctx, "tls.apply")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := svc.PrepareApply(ctx, lease)
	if err != nil || prepared.CommittedResult == nil || prepared.CommittedResult.ErrorCode != "candidate_invalid" {
		t.Fatal("invalid queued snapshot left applying without a terminal result", err)
	}
	after, err := svc.Current(ctx, admin.Principal)
	if err != nil || after.State != "unavailable" || after.DesiredID != nil {
		t.Fatal("failed preparation left stuck intent", err)
	}
}

func TestGatewayTLSRetainsReconciliationAfterRetryLimit(t *testing.T) {
	for _, crashed := range []bool{false, true} {
		t.Run(map[bool]string{false: "temporary-errors", true: "last-lease-crash"}[crashed], func(t *testing.T) {
			db, accounts, _, admin := authFixture(t)
			ctx := context.Background()
			svc := tlsmanager.New(db, accounts, t.TempDir(), "https://nvr.example.com", "")
			if e := svc.Initialize(ctx); e != nil {
				t.Fatal(e)
			}
			chain, key := tlsPair(t, 91)
			v, e := svc.Import(ctx, admin.Principal, chain, key)
			if e != nil {
				t.Fatal(e)
			}
			state, e := svc.Current(ctx, admin.Principal)
			if e != nil {
				t.Fatal(e)
			}
			job, e := svc.RequestApply(ctx, admin.Principal, v.ID, state.Version)
			if e != nil {
				t.Fatal(e)
			}
			repo := jobs.Repository{DB: db}
			for i := 0; i < 6; i++ {
				lease, e := repo.Claim(ctx, "tls.apply")
				if e != nil {
					t.Fatal("TLS reconciliation stopped before verified outcome", e)
				}
				if crashed {
					_, e = db.Pool.Exec(ctx, "UPDATE jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", job)
				} else {
					e = repo.Fail(ctx, lease, "handler_failed")
					if e == nil {
						_, e = db.Pool.Exec(ctx, "UPDATE jobs SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1", job)
					}
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			// An unrelated worker may not discard a TLS intent at its generic limit.
			_, _ = repo.Claim(ctx, "pool.check")
			fresh, e := repo.Claim(ctx, "tls.apply")
			if e != nil {
				t.Fatal("TLS intent was abandoned at retry limit", e)
			}
			if _, e = svc.PrepareApply(ctx, fresh); e != nil {
				t.Fatal(e)
			}
			result := tlsmanager.GatewayResult{JobID: job, State: "applied", ActiveID: &v.ID, LeafSHA256: v.LeafSHA256, ChainSHA256: v.ChainSHA256}
			if e = svc.CommitGatewayResult(ctx, fresh, result); e != nil {
				t.Fatal(e)
			}
			if e = repo.Complete(ctx, fresh, jobs.Result{}); e != nil {
				t.Fatal(e)
			}
			after, e := svc.Current(ctx, admin.Principal)
			if e != nil || after.State != "active" || after.ActiveID == nil || *after.ActiveID != v.ID {
				t.Fatal("verified receipt did not recover domain state", e)
			}
		})
	}
}
