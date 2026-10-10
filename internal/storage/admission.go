package storage

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
)

// RowQuerier is the read surface both a transaction and the pool provide, so
// admission checks work inside a caller's transaction or standalone.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ProofPeers reports how many storage_pool_checks rows must be healthy and
// fresh before a pool may start recording.
//
// The two process samples (`api`, `worker`) are always required: they are what
// makes a full disk observable, and without them a site would record into a
// full filesystem without noticing. The `zlm` row is the proof that a real MP4
// landed, which is a reliability preference the site may decline.
func ProofPeers(ctx context.Context, q RowQuerier, siteID id.ID) (int, error) {
	var proof bool
	if err := q.QueryRow(ctx, "SELECT require_storage_write_proof FROM sites WHERE id=$1", siteID).Scan(&proof); err != nil {
		return 0, err
	}
	if proof {
		return 3, nil
	}
	return 2, nil
}

// RequireWriteProof reports the site's admission preference on its own, for
// callers that must decide whether refreshing expired evidence makes sense.
func RequireWriteProof(ctx context.Context, q RowQuerier, siteID id.ID) (bool, error) {
	var proof bool
	if err := q.QueryRow(ctx, "SELECT require_storage_write_proof FROM sites WHERE id=$1", siteID).Scan(&proof); err != nil {
		return false, err
	}
	return proof, nil
}

// PoolHasFreshEvidence reports whether an enabled pool of this site currently
// holds the required fresh healthy evidence.
func PoolHasFreshEvidence(ctx context.Context, q RowQuerier, siteID, poolID id.ID) (bool, error) {
	peers, err := ProofPeers(ctx, q, siteID)
	if err != nil {
		return false, err
	}
	// When write proof is required every service row must be healthy; otherwise
	// only the process samples are counted, and a stale or failed zlm row is
	// simply not part of the decision.
	var ready bool
	if peers == 3 {
		err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_pools p WHERE p.id=$1 AND p.site_id=$2 AND p.enabled AND (SELECT count(*) FROM storage_pool_checks WHERE pool_id=p.id AND state='healthy' AND expires_at>clock_timestamp())=3)`, poolID, siteID).Scan(&ready)
		return ready, err
	}
	err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM storage_pools p WHERE p.id=$1 AND p.site_id=$2 AND p.enabled AND (SELECT count(*) FROM storage_pool_checks WHERE pool_id=p.id AND service IN ('api','worker') AND state='healthy' AND expires_at>clock_timestamp())=2)`, poolID, siteID).Scan(&ready)
	return ready, err
}
