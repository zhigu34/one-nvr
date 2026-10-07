// Package live authorizes short-lived viewers of existing channel streams.
// Viewing never starts a proxy, pre-recording, or recording job.
package live

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

var ErrOffline = fault.New(409, "live_stream_offline", "通道流未就绪，请检查摄像头连接与应用状态")
var ErrUnavailable = fault.New(503, "live_unavailable", "媒体服务暂时不可用")
var ErrCodec = fault.New(422, "live_codec_unsupported", "浏览器不支持此视频编码，请将摄像头此码流改为 H.264")
var ErrExpired = fault.New(409, "live_session_expired", "播放会话已失效，请重新连接")

type Media interface {
	Inspect(context.Context, zlm.StreamKey) (zlm.StreamSnapshot, error)
	Negotiate(context.Context, zlm.StreamKey, string, string) (string, zlm.RTCPeer, error)
	CloseRTC(context.Context, zlm.RTCPeer) error
}
type Service struct {
	DB    *database.DB
	Auth  *auth.Service
	Media Media
}
type Answer struct {
	ID        id.ID  `json:"id"`
	SDP       string `json:"sdp"`
	Stream    string `json:"stream"`
	Fallback  bool   `json:"fallback"`
	ExpiresIn int    `json:"expires_in"`
}

// Shared predicate is evaluated against current grants, cookie-session validity
// and the physical stream identity on each hook, renewal and reap.
const validity = `u.enabled AND u.auth_version=l.auth_version AND ss.auth_version=u.auth_version
 AND ss.revoked_at IS NULL AND ss.last_seen_at>clock_timestamp()-interval '30 minutes'
 AND ss.absolute_expires_at>clock_timestamp() AND g.live AND c.enabled
 AND st.state='active' AND st.source_test_id IS NULL AND st.purpose IN ('main','sub')
 AND st.source_revision_id=c.current_revision_id
 AND NOT EXISTS(SELECT 1 FROM stream_sessions newer WHERE newer.channel_id=st.channel_id AND newer.source_revision_id=c.current_revision_id AND newer.purpose=st.purpose AND newer.state='active' AND newer.generation>st.generation)`
const joins = ` FROM live_sessions l JOIN users u ON u.id=l.user_id JOIN sessions ss ON ss.id=l.session_id AND ss.user_id=u.id
 JOIN channels c ON c.id=l.channel_id JOIN channel_grants g ON g.user_id=u.id AND g.channel_id=c.id
 JOIN stream_sessions st ON st.id=l.stream_session_id AND st.channel_id=c.id `

func (s *Service) Open(ctx context.Context, p auth.Principal, ch id.ID, kind, offer string) (Answer, error) {
	if kind != "main" && kind != "sub" || len(offer) > 65536 || !strings.HasPrefix(offer, "v=0") {
		return Answer{}, auth.ErrInvalid
	}
	if err := s.Auth.RequireChannel(ctx, p, ch, auth.Live); err != nil {
		return Answer{}, err
	}
	if s.Media == nil {
		return Answer{}, ErrUnavailable
	}
	lease, _ := id.New()
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return Answer{}, ErrUnavailable
	}
	ticket := hex.EncodeToString(bytes)
	hash := sha256.Sum256([]byte(ticket))
	var physical id.ID
	var key zlm.StreamKey
	actual := kind
	fallback := false
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, ch, auth.Live, tx); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM live_sessions WHERE session_id=$1 AND state IN ('pending','active') AND expires_at>clock_timestamp()`, p.SessionID).Scan(&count); err != nil {
			return err
		}
		if count >= 16 {
			return fault.New(429, "live_limit", "最多同时预览 16 个画面，请先关闭其他预览")
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM live_sessions WHERE state IN ('pending','active') AND expires_at>clock_timestamp()`).Scan(&count); err != nil {
			return err
		}
		if count >= 256 {
			return fault.New(429, "live_limit", "预览连接已达到系统上限，请先关闭其他预览")
		}
		var subConfigured bool
		err := tx.QueryRow(ctx, `SELECT r.sub_path<>'' FROM channels c JOIN source_revisions r ON r.id=c.current_revision_id WHERE c.id=$1 AND c.enabled`, ch).Scan(&subConfigured)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOffline
		}
		if err != nil {
			return err
		}
		if kind == "sub" && !subConfigured {
			actual = "main"
			fallback = true
		}
		err = tx.QueryRow(ctx, `SELECT st.id,st.vhost,st.app,st.stream FROM stream_sessions st JOIN channels c ON c.id=st.channel_id WHERE c.id=$1 AND c.enabled AND st.source_revision_id=c.current_revision_id AND st.source_test_id IS NULL AND st.purpose=$2 AND st.state='active' ORDER BY st.generation DESC LIMIT 1`, ch, actual).Scan(&physical, &key.VHost, &key.App, &key.Stream)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOffline
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO live_sessions(id,user_id,session_id,auth_version,channel_id,stream_session_id,ticket_hash) VALUES($1,$2,$3,$4,$5,$6,$7)`, lease, p.UserID, p.SessionID, p.AuthVersion, ch, physical, hash[:])
		return err
	})
	if err != nil {
		return Answer{}, err
	}
	// Capture a late answer even when the HTTP caller goes away, so the private
	// peer can be persisted/closed. All work is still bounded to ten seconds.
	owned, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	snapshot, err := s.Media.Inspect(owned, key)
	if err != nil {
		s.finish(lease, zlm.RTCPeer{})
		if errors.Is(err, zlm.ErrStreamAbsent) {
			return Answer{}, ErrOffline
		}
		return Answer{}, ErrUnavailable
	}
	compatible := false
	upper := strings.ToUpper(offer)
	for _, track := range snapshot.Tracks {
		codec := strings.ToUpper(track.Codec)
		if track.Ready && (codec == "H264" || codec == "H265" || codec == "VP8" || codec == "VP9" || codec == "AV1") && strings.Contains(upper, " "+codec+"/90000") {
			compatible = true
		}
	}
	if !compatible {
		s.finish(lease, zlm.RTCPeer{})
		return Answer{}, ErrCodec
	}
	answer, peer, err := s.Media.Negotiate(owned, key, offer, ticket)
	if err != nil {
		s.finish(lease, peer)
		return Answer{}, ErrUnavailable
	}
	persist, persistCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer persistCancel()
	_, err = s.DB.Pool.Exec(persist, `UPDATE live_sessions SET peer_id=$2,peer_token=$3 WHERE id=$1`, lease, peer.ID, peer.Token)
	if err != nil {
		s.finish(lease, peer)
		return Answer{}, ErrUnavailable
	}
	if ctx.Err() != nil {
		s.finish(lease, peer)
		return Answer{}, ctx.Err()
	}
	if err = s.Renew(owned, p, lease); err != nil {
		s.finish(lease, peer)
		return Answer{}, err
	}
	return Answer{lease, answer, actual, fallback, 30}, nil
}

func (s *Service) Renew(ctx context.Context, p auth.Principal, lease id.ID) error {
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := auth.LockAuthorization(ctx, tx); err != nil {
			return err
		}
		var valid bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+joins+`WHERE l.id=$1 AND l.user_id=$2 AND l.session_id=$3 AND l.state IN ('pending','active') AND l.expires_at>clock_timestamp() AND `+validity+`)`, lease, p.UserID, p.SessionID).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return ErrExpired
		}
		tag, err := tx.Exec(ctx, `UPDATE live_sessions SET state='active',expires_at=clock_timestamp()+interval '30 seconds' WHERE id=$1 AND state IN ('pending','active') AND expires_at>clock_timestamp()`, lease)
		if err == nil && tag.RowsAffected() != 1 {
			return ErrExpired
		}
		return err
	})
}
func (s *Service) AuthorizePlay(ctx context.Context, key zlm.StreamKey, ticket string) error {
	if key.Validate() != nil || len(ticket) != 64 {
		return auth.ErrForbidden
	}
	hash := sha256.Sum256([]byte(ticket))
	var valid bool
	err := s.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+joins+`WHERE l.ticket_hash=$1 AND st.vhost=$2 AND st.app=$3 AND st.stream=$4 AND l.state IN ('pending','active') AND l.expires_at>clock_timestamp() AND `+validity+`)`, hash[:], key.VHost, key.App, key.Stream).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return auth.ErrForbidden
	}
	return nil
}
func (s *Service) Close(ctx context.Context, p auth.Principal, lease id.ID) error {
	tag, err := s.DB.Pool.Exec(ctx, `UPDATE live_sessions SET state='closing' WHERE id=$1 AND user_id=$2 AND session_id=$3 AND state<>'closed'`, lease, p.UserID, p.SessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return nil
}
func (s *Service) finish(lease id.ID, peer zlm.RTCPeer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if peer.ID == "" {
		s.DB.Pool.Exec(ctx, `UPDATE live_sessions SET state='closed' WHERE id=$1`, lease)
		cancel()
		return
	}
	// Save cleanup intent BEFORE network I/O. A deletion timeout must never
	// consume the context used to persist a previously unregistered peer.
	s.DB.Pool.Exec(ctx, `UPDATE live_sessions SET state='closing',peer_id=$2,peer_token=$3 WHERE id=$1`, lease, peer.ID, peer.Token)
	cancel()
	deletion, deleteCancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := s.Media.CloseRTC(deletion, peer)
	deleteCancel()
	if err != nil {
		return
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.DB.Pool.Exec(ctx, `UPDATE live_sessions SET state='closed',peer_id=NULL,peer_token=NULL WHERE id=$1`, lease)
}
