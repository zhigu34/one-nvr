package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	// Email settings, present for kind 'email'. The password is never here: it
	// lives in the sealed secret and HasSecret says whether one exists.
	Email      *SMTPConfig     `json:"email,omitempty"`
	Recipients []Recipient     `json:"recipients,omitempty"`
	WeCom      *WeComConfig    `json:"wecom,omitempty"`
	WeComApp   *WeComAppConfig `json:"wecom_app,omitempty"`
}

// ChannelInput is a create or update request. Secret is write-only: empty on an
// update keeps the stored one, so a rename does not require re-typing it.
type ChannelInput struct {
	Kind    string
	Name    string
	URL     string
	Secret  string
	Enabled bool
	// Email carries the SMTP parameters for kind 'email'.
	Email *SMTPConfig
	// Recipients replace the stored list when non-nil. A nil slice means "leave
	// the recipients alone", which is what an update that only renames a channel
	// needs.
	Recipients *[]RecipientInput
	// WeCom carries the group-robot settings for kind 'wecom'.
	WeCom *WeComConfig
	// WeComApp carries the enterprise-application settings for kind 'wecom_app'.
	WeComApp *WeComAppConfig
}

// RecipientInput is one mailbox on an email channel, from the editor.
type RecipientInput struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

const channelColumns = `c.id,c.kind,c.name,COALESCE(c.url,''),c.enabled,(octet_length(c.secret_ciphertext)>0),c.last_check,c.version,c.created_at,
	COALESCE(c.sender_name,''),COALESCE(c.sender_address,''),COALESCE(c.smtp_host,''),COALESCE(c.smtp_port,0),c.smtp_tls,c.smtp_auth,COALESCE(c.smtp_username,''),
	COALESCE(c.wecom_msg_type,''),COALESCE(c.wecom_mentions,''),
	COALESCE(c.wecom_corp_id,''),COALESCE(c.wecom_agent_id,0),COALESCE(c.wecom_to,'')`

func scanChannel(row pgx.Row) (ChannelView, error) {
	var out ChannelView
	var senderName, senderAddress, host, username string
	var port int
	var useTLS, useAuth bool
	var wecomMsgType, wecomMentions, wecomCorpID, wecomTo string
	var wecomAgentID int
	err := row.Scan(&out.ID, &out.Kind, &out.Name, &out.URL, &out.Enabled, &out.HasSecret, &out.LastCheck, &out.Version, &out.CreatedAt,
		&senderName, &senderAddress, &host, &port, &useTLS, &useAuth, &username,
		&wecomMsgType, &wecomMentions, &wecomCorpID, &wecomAgentID, &wecomTo)
	if err != nil {
		return out, err
	}
	// The SMTP block is reported only where it means something: an inbox with an
	// empty sender name would otherwise look half-configured.
	if out.Kind == "wecom" {
		out.WeCom = &WeComConfig{MsgType: wecomMsgType, Mentions: wecomMentions}
	}
	if out.Kind == "wecom_app" {
		out.WeComApp = &WeComAppConfig{
			CorpID: wecomCorpID, AgentID: wecomAgentID, To: wecomTo, MsgType: wecomMsgType,
		}
	}
	if out.Kind == "email" {
		out.Email = &SMTPConfig{
			SenderName: senderName, SenderAddress: senderAddress, Host: host, Port: port,
			TLS: useTLS, UseAuth: useAuth, Username: username,
		}
	}
	if out.Kind == "wecom" {
		out.WeCom = &WeComConfig{MsgType: wecomMsgType, Mentions: wecomMentions}
	}
	if out.Kind == "wecom_app" {
		out.WeComApp = &WeComAppConfig{
			CorpID: wecomCorpID, AgentID: wecomAgentID, To: wecomTo, MsgType: wecomMsgType,
		}
	}
	return out, nil
}

func validateChannelInput(in ChannelInput, creating bool) error {
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
	case "wecom":
		// The robot URL already contains the key; an empty one cannot deliver.
		if strings.TrimSpace(in.URL) == "" {
			return ErrChannelInvalid
		}
		if in.WeCom == nil || (in.WeCom.MsgType != "markdown" && in.WeCom.MsgType != "news") {
			return ErrChannelInvalid
		}
		return nil
	case "wecom_app":
		if in.WeComApp == nil {
			return ErrChannelInvalid
		}
		if strings.TrimSpace(in.WeComApp.CorpID) == "" || in.WeComApp.AgentID <= 0 {
			return ErrChannelInvalid
		}
		if strings.TrimSpace(in.WeComApp.To) == "" {
			return ErrChannelInvalid
		}
		// The secret is exchanged for a token; without it nothing can be sent.
		// An update may omit it (nil keeps the stored one), handled by the caller.
		return nil
	case "email":
		// Every field the adapter needs to open a session, checked here so a
		// channel that could never deliver is refused at save time instead of at
		// the first real alarm.
		if in.Email == nil {
			return ErrChannelInvalid
		}
		if strings.TrimSpace(in.Email.SenderAddress) == "" || strings.TrimSpace(in.Email.Host) == "" {
			return ErrChannelInvalid
		}
		if in.Email.Port < 1 || in.Email.Port > 65535 {
			return ErrChannelInvalid
		}
		// A new channel must have at least one mailbox. An update may omit the
		// list entirely (nil keeps the stored one — the rename path relies on
		// that), but an explicit empty list is refused: it would leave the
		// channel undeliverable while looking configured.
		if in.Recipients == nil {
			if creating {
				return ErrChannelInvalid
			}
		} else if len(trimRecipients(*in.Recipients)) == 0 {
			return ErrChannelInvalid
		}
		if in.Email.UseAuth && strings.TrimSpace(in.Email.Username) == "" {
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.attachRecipients(ctx, out)
}

// RegisterChannel creates a destination. A webhook must carry a signing secret
// up front: one saved without it could never deliver, and finding that out on
// the first real event is too late.
func (s *Service) RegisterChannel(ctx context.Context, p auth.Principal, in ChannelInput) (ChannelView, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return ChannelView{}, err
	}
	if err := validateChannelInput(in, true); err != nil {
		return ChannelView{}, err
	}
	// A pushing kind that needs a credential cannot be saved without it: one
	// stored empty could never deliver, and finding that out on the first real
	// alarm is too late.
	if (in.Kind == "webhook" || in.Kind == "wecom_app") && in.Secret == "" {
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
		insertArgs := []any{channelID, s.siteID, in.Kind, in.Name, url, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, in.Enabled}
		insertArgs = append(insertArgs, smtpColumns(in.Email)...)
		if in.Kind == "wecom_app" {
			insertArgs = append(insertArgs, wecomAppColumns(in.WeComApp)...)
		} else {
			insertArgs = append(insertArgs, wecomColumns(in.WeCom)...)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notification_channels(id,site_id,kind,name,url,credential_key_id,secret_nonce,secret_ciphertext,enabled,
			sender_name,sender_address,smtp_host,smtp_port,smtp_tls,smtp_auth,smtp_username,wecom_msg_type,wecom_mentions,
			wecom_corp_id,wecom_agent_id,wecom_to)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
			insertArgs...); err != nil {
			return err
		}
		if err := replaceRecipientsTx(ctx, tx, channelID, in.Recipients); err != nil {
			return err
		}
		// The audit names the channel and its kind; the endpoint is a URL and
		// the secret is a credential, so neither travels into the log.
		details, _ := json.Marshal(map[string]any{"kind": in.Kind, "name": in.Name})
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "notification.channel_registered", ObjectID: channelID, Details: details}); err != nil {
			return err
		}
		scanned, scanErr := scanChannel(tx.QueryRow(ctx, `SELECT `+channelColumns+` FROM notification_channels c WHERE c.id=$1`, channelID))
		if scanErr != nil {
			return scanErr
		}
		recipients, recipientErr := recipientsTx(ctx, tx, channelID)
		if recipientErr != nil {
			return recipientErr
		}
		scanned.Recipients = recipients
		out = scanned
		return nil
	})
	return out, err
}

// UpdateChannel edits a destination. An empty secret keeps the stored one, so
// renaming or toggling a channel does not require the operator to re-enter it.
func (s *Service) UpdateChannel(ctx context.Context, p auth.Principal, channelID id.ID, expected int64, in ChannelInput) (ChannelView, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return ChannelView{}, err
	}
	if err := validateChannelInput(in, false); err != nil {
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
		if (in.Kind == "webhook" || in.Kind == "wecom_app") && in.Secret == "" && !hasSecret {
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
			args := []any{channelID, in.Kind, in.Name, url, in.Enabled, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, expected}
			args = append(args, smtpColumns(in.Email)...)
			if in.Kind == "wecom_app" {
				args = append(args, wecomAppColumns(in.WeComApp)...)
			} else {
				args = append(args, wecomColumns(in.WeCom)...)
			}
			if _, err := tx.Exec(ctx, `UPDATE notification_channels SET kind=$2,name=$3,url=$4,enabled=$5,credential_key_id=$6,secret_nonce=$7,secret_ciphertext=$8,
				sender_name=$10,sender_address=$11,smtp_host=$12,smtp_port=$13,smtp_tls=$14,smtp_auth=$15,smtp_username=$16,
				wecom_msg_type=$17,wecom_mentions=$18,wecom_corp_id=$19,wecom_agent_id=$20,wecom_to=$21,version=version+1,updated_at=now()
				WHERE id=$1 AND version=$9`, args...); err != nil {
				return err
			}
		} else {
			args := []any{channelID, in.Kind, in.Name, url, in.Enabled, expected}
			args = append(args, smtpColumns(in.Email)...)
			if in.Kind == "wecom_app" {
				args = append(args, wecomAppColumns(in.WeComApp)...)
			} else {
				args = append(args, wecomColumns(in.WeCom)...)
			}
			if _, err := tx.Exec(ctx, `UPDATE notification_channels SET kind=$2,name=$3,url=$4,enabled=$5,
				sender_name=$7,sender_address=$8,smtp_host=$9,smtp_port=$10,smtp_tls=$11,smtp_auth=$12,smtp_username=$13,
				wecom_msg_type=$14,wecom_mentions=$15,wecom_corp_id=$16,wecom_agent_id=$17,wecom_to=$18,version=version+1,updated_at=now()
				WHERE id=$1 AND version=$6`, args...); err != nil {
				return err
			}
		}
		if err := replaceRecipientsTx(ctx, tx, channelID, in.Recipients); err != nil {
			return err
		}
		var version int64
		if err := tx.QueryRow(ctx, `SELECT version FROM notification_channels WHERE id=$1`, channelID).Scan(&version); err != nil {
			return err
		}
		if version == expected {
			return fault.New(409, "notification_channel_version_conflict", "渠道已被他人修改，请刷新后重试")
		}
		// The key cannot be named "secret_rotated": safejson refuses any field whose
		// name contains "secret", because the audit trail must not become a place a
		// credential leaks into. `rotated` says the same thing without tripping it.
		details, _ := json.Marshal(map[string]any{"kind": in.Kind, "name": in.Name, "rotated": in.Secret != ""})
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "notification.channel_updated", ObjectID: channelID, Details: details}); err != nil {
			return err
		}
		scanned, scanErr := scanChannel(tx.QueryRow(ctx, `SELECT `+channelColumns+` FROM notification_channels c WHERE c.id=$1`, channelID))
		if scanErr != nil {
			return scanErr
		}
		recipients, recipientErr := recipientsTx(ctx, tx, channelID)
		if recipientErr != nil {
			return recipientErr
		}
		scanned.Recipients = recipients
		out = scanned
		return nil
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
	sendErr := adapter.Send(ctx, testMessage())
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
	var senderName, senderAddress, host, username string
	var port int
	var useTLS, useAuth bool
	var wecomMsgType, wecomMentions, wecomCorpID, wecomTo string
	var wecomAgentID int
	if err := s.DB.Pool.QueryRow(ctx, `SELECT id,kind,name,url,credential_key_id,secret_nonce,secret_ciphertext,
		COALESCE(sender_name,''),COALESCE(sender_address,''),COALESCE(smtp_host,''),COALESCE(smtp_port,0),smtp_tls,smtp_auth,COALESCE(smtp_username,''),
		COALESCE(wecom_msg_type,''),COALESCE(wecom_mentions,''),
		COALESCE(wecom_corp_id,''),COALESCE(wecom_agent_id,0),COALESCE(wecom_to,'')
		FROM notification_channels WHERE id=$1 AND site_id=$2`,
		channelID, s.siteID).Scan(&out.ID, &out.Kind, &out.Name, &url, &keyID, &nonce, &ciphertext,
		&senderName, &senderAddress, &host, &port, &useTLS, &useAuth, &username,
		&wecomMsgType, &wecomMentions, &wecomCorpID, &wecomAgentID, &wecomTo); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, auth.ErrNotFound
		}
		return out, err
	}
	if url != nil {
		out.URL = *url
	}
	if out.Kind == "email" {
		out.Email = &SMTPConfig{
			SenderName: senderName, SenderAddress: senderAddress, Host: host, Port: port,
			TLS: useTLS, UseAuth: useAuth, Username: username,
		}
		recipients, err := s.loadRecipients(ctx, channelID)
		if err != nil {
			return out, err
		}
		for _, recipient := range recipients {
			out.Recipients = append(out.Recipients, recipient.Address)
		}
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

// smtpColumns renders the SMTP settings as the argument list the INSERT and
// UPDATE statements expect, in one place so the two cannot drift.
//
// A nil config writes NULLs: a webhook has no sender and must not be given one,
// and the table's CHECK relies on that to tell the kinds apart.
func smtpColumns(cfg *SMTPConfig) []any {
	if cfg == nil {
		return []any{nil, nil, nil, nil, false, true, nil}
	}
	nullable := func(v string) any {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return strings.TrimSpace(v)
	}
	var port any
	if cfg.Port > 0 {
		port = cfg.Port
	}
	return []any{
		nullable(cfg.SenderName), nullable(cfg.SenderAddress), nullable(cfg.Host), port,
		cfg.TLS, cfg.UseAuth, nullable(cfg.Username),
	}
}

// wecomColumns is the same idea for the group-robot settings. It writes NULLs for
// every other kind, and the table's CHECK relies on that to keep them apart.
func wecomColumns(cfg *WeComConfig) []any {
	if cfg == nil {
		return []any{nil, nil, nil, nil, nil}
	}
	nullable := func(v string) any {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return strings.TrimSpace(v)
	}
	return []any{nullable(cfg.MsgType), nullable(cfg.Mentions), nil, nil, nil}
}

// wecomAppColumns is the enterprise-application half. It shares the message-type
// column with the robot (both WeCom kinds choose a layout) and writes the robot's
// mention field as NULL.
func wecomAppColumns(cfg *WeComAppConfig) []any {
	if cfg == nil {
		return []any{nil, nil, nil, nil, nil}
	}
	nullable := func(v string) any {
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return strings.TrimSpace(v)
	}
	var agentID any
	if cfg.AgentID > 0 {
		agentID = cfg.AgentID
	}
	msgType := nullable(cfg.MsgType)
	return []any{msgType, nil, nullable(cfg.CorpID), agentID, nullable(cfg.To)}
}

// replaceRecipientsTx rewrites an email channel's mailbox list.
//
// A nil pointer leaves the stored list alone (an update that only renames the
// channel); a non-nil one replaces it wholesale, which matches how the editor
// behaves — the operator edits the list as a set, not as individual rows.
func replaceRecipientsTx(ctx context.Context, tx pgx.Tx, channelID id.ID, in *[]RecipientInput) error {
	if in == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM notification_recipients WHERE channel_id=$1`, channelID); err != nil {
		return err
	}
	for _, recipient := range trimRecipients(*in) {
		recipientID, err := id.New()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notification_recipients(id,channel_id,name,address) VALUES($1,$2,$3,$4)`,
			recipientID, channelID, recipient.Name, recipient.Address); err != nil {
			return err
		}
	}
	return nil
}

// loadRecipients reads a channel's mailboxes. Kept separate from scanChannel
// because it is a second query, and folding it in would make the channel list
// N+1.
func (s *Service) loadRecipients(ctx context.Context, channelID id.ID) ([]Recipient, error) {
	rows, err := s.DB.Pool.Query(ctx, `SELECT id,COALESCE(name,''),address,last_check FROM notification_recipients
		WHERE channel_id=$1 ORDER BY created_at,address`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recipient{}
	for rows.Next() {
		var item Recipient
		if err := rows.Scan(&item.ID, &item.Name, &item.Address, &item.LastCheck); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// attachRecipients fills the mailbox list on every email channel in one query.
func (s *Service) attachRecipients(ctx context.Context, channels []ChannelView) ([]ChannelView, error) {
	emailChannels := 0
	for _, channel := range channels {
		if channel.Kind == "email" {
			emailChannels++
		}
	}
	if emailChannels == 0 {
		return channels, nil
	}
	rows, err := s.DB.Pool.Query(ctx, `SELECT channel_id,id,COALESCE(name,''),address,last_check
		FROM notification_recipients WHERE channel_id IN (SELECT id FROM notification_channels WHERE site_id=$1 AND kind='email')
		ORDER BY created_at,address`, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byChannel := map[id.ID][]Recipient{}
	for rows.Next() {
		var channelID id.ID
		var item Recipient
		if err := rows.Scan(&channelID, &item.ID, &item.Name, &item.Address, &item.LastCheck); err != nil {
			return nil, err
		}
		byChannel[channelID] = append(byChannel[channelID], item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range channels {
		if channels[index].Kind == "email" {
			channels[index].Recipients = byChannel[channels[index].ID]
		}
	}
	return channels, nil
}

// trimRecipients drops blank rows and duplicates, so "one mailbox" cannot be
// stored twice and "an empty row" cannot make a list look non-empty.
func trimRecipients(in []RecipientInput) []RecipientInput {
	out := make([]RecipientInput, 0, len(in))
	seen := map[string]bool{}
	for _, recipient := range in {
		address := strings.TrimSpace(recipient.Address)
		if address == "" {
			continue
		}
		key := strings.ToLower(address)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, RecipientInput{Name: strings.TrimSpace(recipient.Name), Address: address})
	}
	return out
}

// TestRecipient sends one message to a single mailbox and records the outcome on
// that row.
//
// The operator's Email tab tests each recipient separately, and that is the right
// unit: a refused address is a fact about that address, and a channel-level
// verdict would leave them guessing which mailbox failed. The session is opened
// and authenticated once — the parameters are the channel's — and only the
// recipient changes.
func (s *Service) TestRecipient(ctx context.Context, p auth.Principal, channelID, recipientID id.ID) (json.RawMessage, error) {
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return nil, err
	}
	var address, name string
	if err := s.DB.Pool.QueryRow(ctx, `SELECT address,COALESCE(name,'') FROM notification_recipients WHERE id=$1 AND channel_id=$2`,
		recipientID, channelID).Scan(&address, &name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrNotFound
		}
		return nil, err
	}
	config, err := s.channelConfig(ctx, channelID)
	if err != nil {
		return nil, err
	}
	if config.Kind != "email" {
		return nil, ErrChannelInvalid
	}
	// One mailbox, not the stored list: the test has to prove THIS address
	// reaches someone.
	config.Recipients = []string{address}
	adapter, err := s.registry.Build(config)
	if err != nil {
		return nil, ErrChannelInvalid
	}
	probe := testMessage()
	sendErr := adapter.Send(ctx, probe)
	result := map[string]any{"state": "healthy", "checked_at": time.Now().UTC(), "address": address}
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
	if _, err := s.DB.Pool.Exec(ctx, `UPDATE notification_recipients SET last_check=$2 WHERE id=$1`, recipientID, raw); err != nil {
		return nil, err
	}
	if sendErr != nil && !IsPermanent(sendErr) {
		return raw, sendErr
	}
	return raw, nil
}

// testMessage is the probe both test paths send. One definition keeps the two
// from drifting into different ideas of "a test".
func testMessage() Message {
	now := time.Now().UTC()
	return Message{
		DeliveryID:    mustID(),
		SiteName:      "测试",
		ChannelNo:     1,
		Category:      "person",
		CategoryLabel: CategoryLabel("person"),
		Severity:      "low",
		StartedAt:     now,
		StartedLocal:  now.Format("2006-01-02 15:04:05"),
		Title:         "one-nvr 测试通知",
		Body:          "这是一条测试通知；收到即表示该渠道可用。",
	}
}

// recipientsTx reads a channel's mailboxes inside a caller's transaction, so a
// register or update can return the complete view it just wrote instead of making
// the editor refetch.
func recipientsTx(ctx context.Context, tx pgx.Tx, channelID id.ID) ([]Recipient, error) {
	rows, err := tx.Query(ctx, `SELECT id,COALESCE(name,''),address,last_check FROM notification_recipients
		WHERE channel_id=$1 ORDER BY created_at,address`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recipient{}
	for rows.Next() {
		var item Recipient
		if err := rows.Scan(&item.ID, &item.Name, &item.Address, &item.LastCheck); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
