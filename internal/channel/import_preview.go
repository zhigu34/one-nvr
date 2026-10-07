package channel

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"io"
	"strings"
)

func (s *SourceService) PreviewImport(ctx context.Context, p auth.Principal, format string, r io.Reader) (ImportPreview, error) {
	var out ImportPreview
	if err := s.Auth.RequireAdmin(ctx, p); err != nil {
		return out, err
	}
	parsed, err := ParseImport(r, format)
	if err != nil {
		return out, err
	}
	out.BatchID, err = id.New()
	if err != nil {
		return out, err
	}
	out.Items = make([]ImportItem, 0, len(parsed))
	err = s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO source_import_batches(id,site_id,created_by,format,expires_at) VALUES($1,$2,$3,$4,clock_timestamp()+interval '30 minutes') RETURNING expires_at`, out.BatchID, s.secret.SiteID, p.UserID, format).Scan(&out.ExpiresAt); err != nil {
			return err
		}
		seen := map[id.ID]bool{}
		addresses := map[string]bool{}
		for _, input := range parsed {
			summary := ImportItem{Row: input.Row, PasswordAction: input.PasswordAction, State: "draft", Differences: []string{}, Errors: []string{}, Warnings: append([]string{}, input.Warnings...)}
			var ch id.ID
			var current *id.ID
			var version int64
			var name string
			err := tx.QueryRow(ctx, "SELECT id,current_revision_id,version,channel_name FROM channels WHERE site_id=$1 AND channel_no=$2 FOR SHARE", s.secret.SiteID, input.ChannelNo).Scan(&ch, &current, &version, &name)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				err = s.Auth.RequireChannelTx(ctx, p, ch, auth.Configure, tx)
			}
			if err != nil {
				if !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, auth.ErrNotFound) && !errors.Is(err, auth.ErrForbidden) {
					return err
				}
				summary.Errors = append(summary.Errors, "target_unavailable")
			} else {
				if seen[ch] {
					return invalidSource("duplicate_import_target", "导入存在重复目标通道")
				}
				seen[ch] = true
				if input.SiteID == s.secret.SiteID && (input.ChannelID == nil || *input.ChannelID != ch) {
					return invalidSource("import_identity_mismatch", "同站配置的通道ID与编号不一致")
				}
				summary.ChannelID = &ch
				summary.ExpectedVersion = &version
				if err := tx.QueryRow(ctx, "SELECT $2::uuid IS NULL AND NOT EXISTS(SELECT 1 FROM source_switches WHERE channel_id=$1 AND kind='apply' AND state='succeeded')", ch, current).Scan(&summary.RequiresInitialRecordingMode); err != nil {
					return err
				}
				if input.SiteID != "" && input.SiteID != s.secret.SiteID {
					summary.Warnings = append(summary.Warnings, "foreign_identity_ignored")
				}
				if input.SourceState == "not_configured" {
					summary.State = "skipped"
					summary.Warnings = append(summary.Warnings, "empty_source_skipped")
				} else {
					if name != input.ChannelName {
						summary.Differences = append(summary.Differences, "channel_name")
					}
					if input.ChannelName == "" || len(input.ChannelName) > 128 || strings.ContainsAny(input.ChannelName, "\x00\r\n") {
						summary.Errors = append(summary.Errors, "channel_name_invalid")
					}
					if err := input.Config.Validate(s.network); err != nil {
						summary.Errors = append(summary.Errors, "source_config_invalid")
					}
					address := input.Config.IP + ":" + input.Config.MainPath
					if addresses[address] {
						summary.Warnings = append(summary.Warnings, "duplicate_source_address")
					}
					addresses[address] = true
					if current == nil {
						summary.Differences = append(summary.Differences, "new_source")
						if input.PasswordAction == "keep" {
							summary.Errors = append(summary.Errors, "password_keep_without_source")
						}
					} else {
						previous, err := loadRevision(ctx, tx, ch, *current)
						if err != nil {
							return err
						}
						credentials, err := s.decrypt(previous)
						if err != nil {
							summary.Errors = append(summary.Errors, "credential_unavailable")
						} else {
							username, password := credentials.Username, credentials.Password
							if input.Username != nil {
								username = *input.Username
							}
							if input.Password != nil {
								password = *input.Password
							}
							digest, err := s.protectedDigest(struct {
								Config             SourceConfig
								Username, Password string
							}{input.Config, username, password})
							if err != nil {
								return err
							}
							var actual []byte
							if err := tx.QueryRow(ctx, "SELECT configuration_digest FROM source_revisions WHERE id=$1", current).Scan(&actual); err != nil {
								return err
							}
							if !hmac.Equal(digest, actual) {
								summary.Differences = append(summary.Differences, "source_configuration")
							}
						}
					}
				}
			}
			item, err := id.New()
			if err != nil {
				return err
			}
			raw, err := json.Marshal(input)
			if err != nil {
				return err
			}
			cipher, err := s.secret.SealCredential(s.secret.SiteID, out.BatchID, item, "import_draft", raw)
			clear(raw)
			if err != nil {
				return err
			}
			safe, err := json.Marshal(summary)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO source_import_items(id,batch_id,row_no,channel_id,expected_version,draft_nonce,draft_ciphertext,credential_key_id,summary,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, item, out.BatchID, input.Row, summary.ChannelID, summary.ExpectedVersion, cipher.Nonce, cipher.Ciphertext, cipher.KeyID, safe, summary.State); err != nil {
				return err
			}
			out.Items = append(out.Items, summary)
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "source.import_previewed", ObjectID: out.BatchID})
	})
	if err != nil {
		return ImportPreview{}, err
	}
	return out, nil
}
