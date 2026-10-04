package channel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

type SourceService struct {
	DB      *database.DB
	Auth    *auth.Service
	secret  secrets.State
	network NetworkPolicy
}

func NewSources(db *database.DB, a *auth.Service, secret secrets.State, network NetworkPolicy) *SourceService {
	return &SourceService{DB: db, Auth: a, secret: secret, network: NetworkPolicy{Allowed: append(network.Allowed[:0:0], network.Allowed...), Denied: append(network.Denied[:0:0], network.Denied...)}}
}
func (s *SourceService) protectedDigest(value any) ([]byte, error) {
	key, err := hex.DecodeString(s.secret.MasterKey)
	if err != nil || len(key) != 32 {
		return nil, secrets.ErrCredentialUnavailable
	}
	defer clear(key)
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, auth.ErrInvalid
	}
	defer clear(raw)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("one-nvr/source-parameters/v1\x00"))
	m.Write(raw)
	return m.Sum(nil), nil
}
func maskedPassword(value string) bool {
	if utf8.RuneCountInString(value) < 3 {
		return false
	}
	return strings.Trim(value, "*•●") == ""
}
func usernameSummary(value string) string {
	if value == "" {
		return ""
	}
	r, _ := utf8.DecodeRuneInString(value)
	return string(r) + "***"
}

// A draft is a configuration change, not a media activation. Optional request
// keys make HTTP retries idempotent without storing plaintext or fake jobs.
func (s *SourceService) CreateDraft(ctx context.Context, p auth.Principal, channelID id.ID, expected int64, in DraftInput, requestKey ...string) (SourceRevision, error) {
	var out SourceRevision
	if expected < 1 || len(requestKey) > 1 {
		return out, auth.ErrInvalid
	}
	key := ""
	if len(requestKey) == 1 {
		key = requestKey[0]
		if key == "" || len(key) > 128 || strings.ContainsAny(key, "\x00\r\n") {
			return out, auth.ErrInvalid
		}
	}
	in.Config = normalizedConfig(in.Config)
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		if err := in.Config.Validate(s.network); err != nil {
			return err
		}
		var siteID id.ID
		var current *id.ID
		var version int64
		if err := tx.QueryRow(ctx, "SELECT site_id,current_revision_id,version FROM channels WHERE id=$1 FOR UPDATE", channelID).Scan(&siteID, &current, &version); err != nil {
			return err
		}
		if siteID != s.secret.SiteID {
			return secrets.ErrCredentialUnavailable
		}
		digest, err := s.protectedDigest(struct {
			ChannelID id.ID
			Expected  int64
			Input     DraftInput
		}{channelID, expected, in})
		if err != nil {
			return err
		}
		if key != "" {
			var previous []byte
			var revisionID id.ID
			err := tx.QueryRow(ctx, "SELECT parameter_digest,revision_id FROM source_draft_requests WHERE actor_id=$1 AND request_key=$2", p.UserID, key).Scan(&previous, &revisionID)
			if err == nil {
				if !hmac.Equal(digest, previous) {
					return auth.ErrConflict
				}
				stored, err := loadRevision(ctx, tx, channelID, revisionID)
				out = stored.Revision
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if version != expected {
			return auth.ErrConflict
		}
		var base *encryptedRevision
		var sourceID id.ID
		switch in.IdentityIntent {
		case "modify":
			if current == nil || in.HistorySourceID != "" {
				return auth.ErrInvalid
			}
			r, err := loadRevision(ctx, tx, channelID, *current)
			if err != nil {
				return err
			}
			base = &r
			sourceID = r.Revision.SourceID
		case "replace":
			if in.HistorySourceID != "" {
				return auth.ErrInvalid
			}
			if current != nil {
				r, err := loadRevision(ctx, tx, channelID, *current)
				if err != nil {
					return err
				}
				base = &r
			}
			sourceID, err = id.New()
			if err != nil {
				return err
			}
		case "history":
			if _, err := id.Parse(string(in.HistorySourceID)); err != nil {
				return auth.ErrInvalid
			}
			r, err := scanRevision(tx.QueryRow(ctx, "SELECT "+revisionColumns+" FROM source_revisions WHERE channel_id=$1 AND source_id=$2 ORDER BY revision_no DESC LIMIT 1", channelID, in.HistorySourceID))
			if err != nil {
				return err
			}
			base = &r
			sourceID = r.Revision.SourceID
		default:
			return auth.ErrInvalid
		}
		username, password := "", ""
		if in.Credentials.Username != nil {
			username = *in.Credentials.Username
		} else if base != nil {
			plain, err := s.secret.OpenCredential(siteID, channelID, base.Revision.ID, "username", base.Username)
			if err != nil {
				return err
			}
			username = string(plain)
			clear(plain)
		}
		switch in.Credentials.PasswordAction {
		case "keep":
			if base == nil || in.Credentials.Password != nil {
				return auth.ErrInvalid
			}
			plain, err := s.secret.OpenCredential(siteID, channelID, base.Revision.ID, "password", base.Password)
			if err != nil {
				return err
			}
			password = string(plain)
			clear(plain)
		case "replace":
			if in.Credentials.Password == nil || maskedPassword(*in.Credentials.Password) {
				return auth.ErrInvalid
			}
			password = *in.Credentials.Password
		case "clear":
			if in.Credentials.Password != nil && *in.Credentials.Password != "" {
				return auth.ErrInvalid
			}
		default:
			return auth.ErrInvalid
		}
		if !validCredential(username) || !validCredential(password) {
			return auth.ErrInvalid
		}
		revisionID, err := id.New()
		if err != nil {
			return err
		}
		userCipher, err := s.secret.SealCredential(siteID, channelID, revisionID, "username", []byte(username))
		if err != nil {
			return err
		}
		passCipher, err := s.secret.SealCredential(siteID, channelID, revisionID, "password", []byte(password))
		if err != nil {
			return err
		}
		var mismatch bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM source_revisions WHERE credential_key_id<>$1)", userCipher.KeyID).Scan(&mismatch); err != nil {
			return err
		}
		if mismatch {
			return secrets.ErrCredentialUnavailable
		}
		if in.IdentityIntent == "replace" {
			if _, err := tx.Exec(ctx, "INSERT INTO source_identities(id,channel_id,label) VALUES($1,$2,'摄像头')", sourceID, channelID); err != nil {
				return err
			}
		}
		var number int64
		if err := tx.QueryRow(ctx, "SELECT coalesce(max(revision_no),0)+1 FROM source_revisions WHERE channel_id=$1", channelID).Scan(&number); err != nil {
			return err
		}
		passwordState := "saved"
		if password == "" {
			passwordState = "empty"
		}
		cfgDigest, err := s.protectedDigest(struct {
			Config             SourceConfig
			Username, Password string
		}{in.Config, username, password})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO source_revisions(id,channel_id,source_id,revision_no,ip,rtsp_port,main_path,sub_path,transport,onvif_port,credential_key_id,username_nonce,username_ciphertext,password_nonce,password_ciphertext,username_summary,password_state,configuration_digest,created_by) VALUES($1,$2,$3,$4,$5::inet,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, revisionID, channelID, sourceID, number, in.Config.IP, in.Config.RTSPPort, in.Config.MainPath, in.Config.SubPath, in.Config.Transport, in.Config.ONVIFPort, userCipher.KeyID, userCipher.Nonce, userCipher.Ciphertext, passCipher.Nonce, passCipher.Ciphertext, usernameSummary(username), passwordState, cfgDigest, p.UserID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE channels SET version=version+1 WHERE id=$1", channelID); err != nil {
			return err
		}
		if key != "" {
			if _, err := tx.Exec(ctx, "INSERT INTO source_draft_requests(actor_id,request_key,channel_id,parameter_digest,revision_id) VALUES($1,$2,$3,$4,$5)", p.UserID, key, channelID, digest, revisionID); err != nil {
				return err
			}
		}
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "source.draft_created", ObjectID: revisionID}); err != nil {
			return err
		}
		stored, err := loadRevision(ctx, tx, channelID, revisionID)
		out = stored.Revision
		return err
	})
	if err != nil {
		return SourceRevision{}, err
	}
	return out, nil
}
func (s *SourceService) ListRevisions(ctx context.Context, p auth.Principal, channelID, cursor id.ID, limit int) (RevisionPage, error) {
	out := RevisionPage{Items: make([]SourceRevision, 0)}
	if limit < 1 || limit > 100 {
		return out, auth.ErrInvalid
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		before := int64(math.MaxInt64)
		if cursor != "" {
			err := tx.QueryRow(ctx, "SELECT revision_no FROM source_revisions WHERE id=$1 AND channel_id=$2", cursor, channelID).Scan(&before)
			if errors.Is(err, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			if err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, "SELECT "+revisionColumns+" FROM source_revisions WHERE channel_id=$1 AND revision_no<$2 ORDER BY revision_no DESC LIMIT $3", channelID, before, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRevision(rows)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, r.Revision)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > limit {
			last := out.Items[limit-1].ID
			out.NextCursor = &last
			out.Items = out.Items[:limit]
		}
		return nil
	})
	if err != nil {
		return RevisionPage{}, err
	}
	return out, nil
}
