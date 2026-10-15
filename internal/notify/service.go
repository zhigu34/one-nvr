package notify

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

// Job kind and retry budget. EVT-05 caps a webhook at five retries; the count
// itself lives in job_attempts, never in a counter of our own.
const (
	JobKindWebhook = "notification.webhook"
	webhookRetries = 5
)

// Delivery states, worded to match EVT-05's list to an operator.
const (
	StateSending   = "sending"   // queued or being attempted
	StateFailed    = "failed"    // this attempt failed; a retry is coming
	StateRetrying  = "retrying"  // failed at least once, still within budget
	StateSucceeded = "succeeded" // the receiver answered 2xx
	StateExhausted = "exhausted" // out of attempts, or permanently refused
)

var (
	ErrChannelNotFound = auth.ErrNotFound
	ErrChannelInvalid  = fault.New(422, "notification_channel_invalid", "通知渠道配置不完整")
	ErrChannelInUse    = fault.New(409, "notification_channel_in_use", "该渠道仍被规则使用，请先解除绑定")
)

// Service owns notification channels, the rule bindings, and raising a
// notification when a rule hit occurs.
type Service struct {
	DB       *database.DB
	Auth     *auth.Service
	Secrets  secrets.State
	siteID   id.ID
	registry *Registry
}

func New(db *database.DB, accounts *auth.Service, sec secrets.State, siteID id.ID) *Service {
	return &Service{DB: db, Auth: accounts, Secrets: sec, siteID: siteID, registry: DefaultRegistry()}
}

// Registry exposes what this build can deliver, for the API and for tests.
func (s *Service) Registry() *Registry { return s.registry }

// Channel is the operator-facing view of a configured destination. The signing
// secret never leaves the service: `HasSecret` says whether one is stored
// without implying what it is.
type ChannelView struct {
	ID        id.ID           `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	URL       string          `json:"url,omitempty"`
	Enabled   bool            `json:"enabled"`
	HasSecret bool            `json:"has_secret"`
	LastCheck json.RawMessage `json:"last_check,omitempty"`
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"created_at"`
}

// ChannelInput is a create or update request. Secret is write-only: empty on an
// update keeps the stored one, so a rename does not require re-typing it.
type ChannelInput struct {
	Kind    string
	Name    string
	URL     string
	Secret  string
	Enabled bool
}

const channelColumns = `c.id,c.kind,c.name,COALESCE(c.url,''),c.enabled,(octet_length(c.secret_ciphertext)>0),c.last_check,c.version,c.created_at`

func scanChannel(row pgx.Row) (ChannelView, error) {
	var out ChannelView
	err := row.Scan(&out.ID, &out.Kind, &out.Name, &out.URL, &out.Enabled, &out.HasSecret, &out.LastCheck, &out.Version, &out.CreatedAt)
	return out, err
}

func validateChannelInput(in ChannelInput) error {
	if len(in.Name) < 1 || len(in.Name) > 64 {
		return ErrChannelInvalid
	}
	switch in.Kind {
	case "inapp":
		// The inbox has no endpoint and no signature; it is always deliverable.
		return nil
	case "webhook":
		if in.URL == "" {
			return ErrChannelInvalid
		}
		return nil
	default:
		return ErrChannelInvalid
	}
}

// ListChannels returns every configured destination. Administrators only:
// channels name internal endpoints.
func (s *Service) ListChannels(ctx context.Context, p auth.Principal) ([]ChannelView, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT `+channelColumns+` FROM notification_channels c WHERE c.site_id=$1 ORDER BY c.created_at,c.id`, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChannelView{}
	for rows.Next() {
		channel, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, channel)
	}
	return out, rows.Err()
}

// RegisterChannel creates a destination. A webhook must carry a signing secret
// up front: one saved without it could never deliver, and finding that out on
// the first real event is too late.
func (s *Service) RegisterChannel(ctx context.Context, p auth.Principal, in ChannelInput) (ChannelView, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return ChannelView{}, err
	}
	if err := validateChannelInput(in); err != nil {
		return ChannelView{}, err
	}
	if in.Kind == "webhook" && in.Secret == "" {
		return ChannelView{}, ErrChannelInvalid
	}
	channelID, err := id.New()
	if err != nil {
		return ChannelView{}, err
	}
	sealed, err := s.Secrets.SealCredential(s.siteID, channelID, channelID, "secret", []byte(in.Secret))
	if err != nil {
		return ChannelView{}, err
	}
	var out ChannelView
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var url *string
		if in.URL != "" {
			url = &in.URL
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notification_channels(id,site_id,kind,name,url,credential_key_id,secret_nonce,secret_ciphertext,enabled)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			channelID, s.siteID, in.Kind, in.Name, url, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, in.Enabled); err != nil {
			return err
		}
		// The audit names the channel and its kind; the endpoint is a URL and
		// the secret is a credential, so neither travels into the log.
		details, _ := json.Marshal(map[string]any{"kind": in.Kind, "name": in.Name})
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "notification.channel_registered", ObjectID: channelID, Details: details}); err != nil {
			return err
		}
		scanned, scanErr := scanChannel(tx.QueryRow(ctx, `SELECT `+channelColumns+` FROM notification_channels c WHERE c.id=$1`, channelID))
		out = scanned
		return scanErr
	})
	return out, err
}

// UpdateChannel edits a destination. An empty secret keeps the stored one, so
// renaming or toggling a channel does not require the operator to re-enter it.
func (s *Service) UpdateChannel(ctx context.Context, p auth.Principal, channelID id.ID, expected int64, in ChannelInput) (ChannelView, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return ChannelView{}, err
	}
	if err := validateChannelInput(in); err != nil {
		return ChannelView{}, err
	}
	var out ChannelView
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var currentKind string
		var hasSecret bool
		if err := tx.QueryRow(ctx, `SELECT kind,(octet_length(secret_ciphertext)>0) FROM notification_channels WHERE id=$1 AND site_id=$2 FOR UPDATE`, channelID, s.siteID).Scan(&currentKind, &hasSecret); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return err
		}
		// A webhook that would end up with no secret cannot deliver; refuse it
		// here rather than at the first event.
		if in.Kind == "webhook" && in.Secret == "" && !hasSecret {
			return ErrChannelInvalid
		}
		var url *string
		if in.URL != "" {
			url = &in.URL
		}
		if in.Secret != "" {
			sealed, err := s.Secrets.SealCredential(s.siteID, channelID, channelID, "secret", []byte(in.Secret))
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE notification_channels SET kind=$2,name=$3,url=$4,enabled=$5,credential_key_id=$6,secret_nonce=$7,secret_ciphertext=$8,version=version+1,updated_at=now() WHERE id=$1 AND version=$9`,
				channelID, in.Kind, in.Name, url, in.Enabled, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, expected); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE notification_channels SET kind=$2,name=$3,url=$4,enabled=$5,version=version+1,updated_at=now() WHERE id=$1 AND version=$6`,
			channelID, in.Kind, in.Name, url, in.Enabled, expected); err != nil {
			return err
		}
		var version int64
		if err := tx.QueryRow(ctx, `SELECT version FROM notification_channels WHERE id=$1`, channelID).Scan(&version); err != nil {
			return err
		}
		if version == expected {
			return fault.New(409, "notification_channel_version_conflict", "渠道已被他人修改，请刷新后重试")
		}
		details, _ := json.Marshal(map[string]any{"kind": in.Kind, "name": in.Name, "secret_rotated": in.Secret != ""})
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "notification.channel_updated", ObjectID: channelID, Details: details}); err != nil {
			return err
		}
		scanned, scanErr := scanChannel(tx.QueryRow(ctx, `SELECT `+channelColumns+` FROM notification_channels c WHERE c.id=$1`, channelID))
		out = scanned
		return scanErr
	})
	return out, err
}

// DeleteChannel removes a destination that no rule still tells. A channel in
// use is refused rather than silently unbound: losing a binding is a change to
// what gets reported, and the operator should make it on purpose.
func (s *Service) DeleteChannel(ctx context.Context, p auth.Principal, channelID id.ID, expected int64) error {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return err
	}
	return s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		var bound int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM rule_notification_channels WHERE channel_id=$1`, channelID).Scan(&bound); err != nil {
			return err
		}
		if bound > 0 {
			return ErrChannelInUse
		}
		tag, err := tx.Exec(ctx, `DELETE FROM notification_channels WHERE id=$1 AND site_id=$2 AND version=$3`, channelID, s.siteID, expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_channels WHERE id=$1 AND site_id=$2)`, channelID, s.siteID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return auth.ErrNotFound
			}
			return fault.New(409, "notification_channel_version_conflict", "渠道已被他人修改，请刷新后重试")
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "notification.channel_deleted", ObjectID: channelID})
	})
}

// TestChannel sends one message the operator asked for, so a destination is
// judged by what it answers instead of by a claim that it works. Unlike an
// archive target there is no read-write-delete probe to run: a webhook's only
// capability is accepting a POST.
func (s *Service) TestChannel(ctx context.Context, p auth.Principal, channelID id.ID) (json.RawMessage, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	config, err := s.channelConfig(ctx, channelID)
	if err != nil {
		return nil, err
	}
	adapter, err := s.registry.Build(config)
	if err != nil {
		return nil, ErrChannelInvalid
	}
	probe := Message{
		DeliveryID:    mustID(),
		SiteName:      "测试",
		ChannelNo:     1,
		Category:      "person",
		CategoryLabel: CategoryLabel("person"),
		Severity:      "low",
		StartedAt:     time.Now().UTC(),
		StartedLocal:  time.Now().Format("2006-01-02 15:04:05"),
		Title:         "one-nvr 测试通知",
		Body:          "这是一条测试通知；收到即表示该渠道可用。",
	}
	sendErr := adapter.Send(ctx, probe)
	result := map[string]any{"kind": config.Kind, "state": "healthy", "checked_at": time.Now().UTC()}
	if sendErr != nil {
		result["state"] = "unavailable"
		result["error"] = sendErr.Error()
		if status := StatusOf(sendErr); status != 0 {
			result["status"] = status
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	// Record what the receiver actually said, so the list shows a measurement
	// rather than the fact that someone clicked a button.
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE notification_channels SET last_check=$2 WHERE id=$1`, channelID, raw); err != nil {
		return nil, err
	}
	if sendErr != nil && !IsPermanent(sendErr) {
		// Report the transient failure as an ordinary error; the last_check row
		// above still carries the detail for the list.
		return raw, sendErr
	}
	return raw, nil
}

// channelConfig loads and decrypts one channel for an adapter. The secret is
// opened here and nowhere else, so no adapter has to know about the keystore.
func (s *Service) channelConfig(ctx context.Context, channelID id.ID) (ChannelConfig, error) {
	var out ChannelConfig
	var keyID string
	var nonce, ciphertext []byte
	var url *string
	if err := s.DB.Pool.QueryRow(ctx, `SELECT id,kind,name,url,credential_key_id,secret_nonce,secret_ciphertext FROM notification_channels WHERE id=$1 AND site_id=$2`,
		channelID, s.siteID).Scan(&out.ID, &out.Kind, &out.Name, &url, &keyID, &nonce, &ciphertext); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, auth.ErrNotFound
		}
		return out, err
	}
	if url != nil {
		out.URL = *url
	}
	secret, err := s.Secrets.OpenCredential(s.siteID, channelID, channelID, "secret", secrets.EncryptedCredential{KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
	if err != nil {
		return out, err
	}
	out.Secret = secret
	return out, nil
}

func mustID() id.ID {
	i, err := id.New()
	if err != nil {
		panic(err)
	}
	return i
}
