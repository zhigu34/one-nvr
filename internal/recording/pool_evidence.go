package recording

import (
	"context"

	"github.com/zhigu34/one-nvr/internal/channel"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
)

// poolEvidenceReady reports whether a pool holds the fresh evidence this site
// requires, refreshing it when the site asks for write proof.
//
// A site that requires write proof gets its expired evidence repaired through
// the real MP4 probe, which is what the accepted M1-B contract does. A site
// that opted out of write proof treats the two process samples as the whole
// requirement: those refresh on their own every ten seconds, so a stale sample
// means the storage monitor itself is unhealthy and writing a probe file would
// not be an honest repair. It reports not-ready instead, and the caller decides
// whether that is a wait or a failure.
func (s *Service) poolEvidenceReady(ctx context.Context, e *channel.Execution, pool id.ID) (bool, error) {
	ready, err := storage.PoolHasFreshEvidence(ctx, s.DB.Pool, s.siteID, pool)
	if err != nil || ready {
		return ready, err
	}
	proof, err := storage.RequireWriteProof(ctx, s.DB.Pool, s.siteID)
	if err != nil || !proof {
		return false, err
	}
	if err := e.Check(ctx); err != nil {
		return false, err
	}
	if err := s.CheckPool(ctx, pool); err != nil {
		return false, err
	}
	return storage.PoolHasFreshEvidence(ctx, s.DB.Pool, s.siteID, pool)
}
