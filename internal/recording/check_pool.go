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
	network, err := s.FreshNetwork(ctx)
	if err != nil {
		return err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT DISTINCT c.id,r.revision_id,GREATEST(c.source_generation,1),r.priority,c.channel_no FROM channels c JOIN LATERAL (
 SELECT c.current_revision_id AS revision_id,1 AS priority WHERE c.current_revision_id IS NOT NULL
 UNION ALL SELECT t.revision_id,0 FROM (SELECT revision_id FROM source_tests WHERE channel_id=c.id AND state='succeeded' AND expires_at>clock_timestamp() ORDER BY observed_at DESC LIMIT 1) t
 ) r ON true WHERE c.site_id=$1 AND c.enabled ORDER BY r.priority,c.channel_no LIMIT 64`, s.siteID)
	if err != nil {
		return err
	}
	type candidate struct {
		channel, revision id.ID
		generation        int64
	}
	var candidates []candidate
	seen := map[id.ID]bool{}
	for rows.Next() {
		var v candidate
		var priority, no int
		if err := rows.Scan(&v.channel, &v.revision, &v.generation, &priority, &no); err != nil {
			rows.Close()
			return err
		}
		if !seen[v.revision] {
			candidates = append(candidates, v)
			seen[v.revision] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(candidates) == 0 {
		return zlm.ErrTestSourceRequired
	}
	// Source failures may try another candidate; a pool write/verification failure
	// must remain visible and must not be disguised as a camera selection issue.
	bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var last error
	for _, v := range candidates {
		if err := bounded.Err(); err != nil {
			return err
		}
		input, err := s.Sources.PrivateConnection(bounded, v.channel, v.revision, "main", network)
		if err != nil {
			last = err
			continue
		}
		err = s.checkPoolCandidate(bounded, poolID, v.channel, v.revision, v.generation, input)
		if err == nil {
			return nil
		}
		var source *sourceConnectionFailure
		if !errors.As(err, &source) {
			return err
		}
		last = err
	}
	return last
}

func (s *Service) checkPoolCandidate(ctx context.Context, poolID, channelID, revisionID id.ID, generation int64, input zlm.ProxyInput) error {
	connection, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
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
	proxy, err := s.Media.AddProxy(connection, input)
	if err != nil {
		return sourceFailure("add_proxy", err)
	}
	ref = &proxy
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE stream_sessions SET state='active',proxy_key=$2,started_at=clock_timestamp() WHERE id=$1`, sessionID, proxy.OpaqueKey); err != nil {
		return err
	}
	privateURL, err := probe.InternalURL(input.Key, s.ProbeToken)
	if err != nil {
		return err
	}
	if _, err := s.Probe.FirstFrame(connection, privateURL); err != nil {
		return sourceFailure("first_frame", err)
	}
	return s.ProbeSession(ctx, poolID, sessionID)
}
