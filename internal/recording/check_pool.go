package recording

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

func (s *Service) CheckPool(ctx context.Context, poolID id.ID) error {
	if s.Media == nil || s.Probe == nil || s.Sources == nil || s.FreshNetwork == nil || s.ProbeToken == "" {
		return zlm.ErrTestSourceRequired
	}
	owned, release, err := s.ownPoolProbe(ctx, poolID)
	if err != nil {
		return err
	}
	defer release()
	ctx = owned
	if err := s.recoverPoolProbe(ctx, poolID); err != nil {
		return err
	}
	var channelID, revisionID id.ID
	var generation int64
	// Reuse a configured source, or an explicitly tested draft for first-pool
	// bootstrap. This check does not change the channel's current source/policy.
	err = s.DB.Pool.QueryRow(ctx, `SELECT c.id,COALESCE(c.current_revision_id,t.revision_id),GREATEST(c.source_generation,1) FROM channels c LEFT JOIN LATERAL (SELECT revision_id FROM source_tests WHERE channel_id=c.id AND state='succeeded' AND expires_at>clock_timestamp() ORDER BY observed_at DESC LIMIT 1) t ON true WHERE c.site_id=$1 AND c.enabled AND COALESCE(c.current_revision_id,t.revision_id) IS NOT NULL ORDER BY (c.current_revision_id IS NOT NULL) DESC,c.channel_no LIMIT 1`, s.siteID).Scan(&channelID, &revisionID, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return zlm.ErrTestSourceRequired
	}
	if err != nil {
		return err
	}
	network, err := s.FreshNetwork(ctx)
	if err != nil {
		return err
	}
	input, err := s.Sources.PrivateConnection(ctx, channelID, revisionID, "main", network)
	if err != nil {
		return err
	}
	sessionID, err := id.New()
	if err != nil {
		return err
	}
	input.Key = zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: string(sessionID)}
	if err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO stream_sessions(id,channel_id,source_revision_id,generation,app,stream,purpose,proxy_key) VALUES($1,$2,$3,$4,'one_nvr',$1::uuid::text,'test','__defaultVhost__/one_nvr/'||$1::uuid::text)`, sessionID, channelID, revisionID, generation); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO pool_probe_intents(session_id,pool_id) VALUES($1,$2)", sessionID, poolID)
		return err
	}); err != nil {
		return err
	}
	ref := &zlm.ProxyRef{Key: input.Key, OpaqueKey: input.Key.VHost + "/" + input.Key.App + "/" + input.Key.Stream}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		state := "failed"
		removeErr := s.Media.RemoveProxy(cleanup, *ref)
		if removeErr == nil || errors.Is(removeErr, zlm.ErrProxyAbsent) {
			state = "closed"
		}
		s.DB.Pool.Exec(cleanup, `UPDATE stream_sessions SET state=$2,closed_at=clock_timestamp() WHERE id=$1`, sessionID, state)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	proxy, err := s.Media.AddProxy(ctx, input)
	if err != nil {
		return err
	}
	ref = &proxy
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE stream_sessions SET state='active',proxy_key=$2,started_at=clock_timestamp() WHERE id=$1`, sessionID, proxy.OpaqueKey); err != nil {
		return err
	}
	privateURL, err := probe.InternalURL(input.Key, s.ProbeToken)
	if err != nil {
		return err
	}
	if _, err := s.Probe.FirstFrame(ctx, privateURL); err != nil {
		return err
	}
	return s.ProbeSession(ctx, poolID, sessionID)
}
