package recording

import (
	"context"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/storage"
	"log/slog"
	"time"
)

// Idle pools have no ordinary MP4 completions to refresh media proof. An explicit
// temporary probe uses the same checked source/write/MP4 path as the Test button.
//
// The probe only exists to keep the `zlm` write proof fresh, so a site that does
// not require that proof is skipped entirely: probing anyway would write a
// three-second recording to every idle pool every thirty seconds forever, which
// is precisely the cost the setting exists to avoid.
// recordingDemandSQL is "some enabled channel bound to this pool wants to
// record right now". `poolExpr` is the SQL expression naming the pool, so the
// same predicate can be embedded in the candidate scan (where the pool is the
// row alias `p.id`) and asked standalone for one pool (`$1`).
//
// It must agree with unionMode. It used to read `rp.mode='continuous'`, which
// was correct while continuous was the only reason to record and became a
// deadlock the moment a manual window could start a recording: the recorder
// refuses to start without a fresh zlm write proof, that proof is produced only
// by a real recording or by this probe, and this probe did not consider a
// manual channel a reason to run. On a site with nothing recording
// continuously the evidence could never be refreshed and the manual window
// could never start.
func recordingDemandSQL(poolExpr string) string {
	return `EXISTS(SELECT 1 FROM channels c
	JOIN recording_policies rp ON rp.channel_id=c.id
	JOIN storage_pools p ON p.id=c.storage_pool_id
	WHERE c.enabled AND p.enabled AND p.id=` + poolExpr + `
	  AND (rp.mode='continuous'
	       OR EXISTS(SELECT 1 FROM recording_manual_windows w
	                 WHERE w.channel_id=c.id AND w.ended_at IS NULL AND w.expires_at>clock_timestamp())))`
}

// IdlePoolCandidates lists pools that may need a probe to refresh their zlm
// write proof. A pool qualifies when it has an in-flight probe, or when a
// channel bound to it wants to record, has no recorder running yet, and its
// evidence has expired.
func (s *Service) IdlePoolCandidates(ctx context.Context) ([]id.ID, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT p.id FROM storage_pools p
		WHERE EXISTS(SELECT 1 FROM pool_probe_intents pi JOIN stream_sessions ss ON ss.id=pi.session_id WHERE pi.pool_id=p.id AND ss.state<>'closed')
		   OR EXISTS(SELECT 1 FROM recording_runs r WHERE r.pool_id=p.id AND r.purpose='probe' AND r.state IN ('starting','recording','stopping'))
		   OR (p.enabled
		       AND `+recordingDemandSQL("p.id")+`
		       AND NOT EXISTS(SELECT 1 FROM recording_runs r WHERE r.pool_id=p.id AND r.purpose='continuous' AND r.state IN ('starting','recording','stopping'))
		       AND NOT EXISTS(SELECT 1 FROM storage_pool_checks c WHERE c.pool_id=p.id AND c.service='zlm' AND c.expires_at>clock_timestamp()))
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pools []id.ID
	for rows.Next() {
		var pool id.ID
		if err := rows.Scan(&pool); err != nil {
			return nil, err
		}
		pools = append(pools, pool)
	}
	return pools, rows.Err()
}

func (s *Service) MonitorIdlePools(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		proof, err := storage.RequireWriteProof(ctx, s.DB.Pool, s.siteID)
		var pools []id.ID
		if err == nil && proof {
			pools, err = s.IdlePoolCandidates(ctx)
		}
		if err == nil {
			for _, pool := range pools {
				probe, cancel := context.WithTimeout(ctx, 20*time.Second)
				owned, release, ownErr := s.ownPoolProbe(probe, pool)
				if ownErr == nil {
					err = s.recoverPoolProbe(owned, pool)
					release()
				} else {
					err = ownErr
				}
				var wanted bool
				if err == nil {
					err = s.DB.Pool.QueryRow(probe, "SELECT "+recordingDemandSQL("$1"), pool).Scan(&wanted)
				}
				if err == nil && wanted {
					err = s.CheckPool(probe, pool)
				}
				cancel()
				if err != nil && ctx.Err() == nil {
					slog.Warn("idle pool media check unavailable", "pool", pool, "error", err)
				}
			}
		} else if ctx.Err() == nil {
			slog.Warn("idle pool checks unavailable", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
