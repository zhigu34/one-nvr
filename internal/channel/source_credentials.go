package channel

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/audit"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
)

type RevealedCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (RevealedCredentials) String() string { return "<revealed credentials redacted>" }

type ExportItem struct {
	ChannelID            id.ID  `json:"channel_id"`
	SourceRevisionID     *id.ID `json:"source_revision_id"`
	SourceID             *id.ID `json:"source_id"`
	SourceRevisionNumber *int64 `json:"source_revision_number"`
	SourceState          string `json:"source_state"`
	ChannelNo            int    `json:"channel_no"`
	ChannelName          string `json:"channel_name"`
	IP                   string `json:"ip"`
	RTSPPort             int    `json:"rtsp_port"`
	Username             string `json:"username"`
	Password             string `json:"password"`
	MainPath             string `json:"main_path"`
	SubPath              string `json:"sub_path"`
	ONVIFPort            *int   `json:"onvif_port,omitempty"`
}

func (ExportItem) String() string { return "<source export credentials redacted>" }

type ExportFile struct {
	FormatVersion int          `json:"format_version"`
	SiteID        id.ID        `json:"site_id"`
	ExportedAt    time.Time    `json:"exported_at"`
	Channels      []ExportItem `json:"channels"`
}

func (ExportFile) String() string { return "<source export redacted>" }
func (s *SourceService) decrypt(r encryptedRevision) (RevealedCredentials, error) {
	var out RevealedCredentials
	u, err := s.secret.OpenCredential(s.secret.SiteID, r.Revision.ChannelID, r.Revision.ID, "username", r.Username)
	if err != nil {
		return out, err
	}
	defer clear(u)
	p, err := s.secret.OpenCredential(s.secret.SiteID, r.Revision.ChannelID, r.Revision.ID, "password", r.Password)
	if err != nil {
		return out, err
	}
	defer clear(p)
	return RevealedCredentials{Username: string(u), Password: string(p)}, nil
}
func (s *SourceService) Reveal(ctx context.Context, p auth.Principal, channelID, revisionID id.ID) (RevealedCredentials, error) {
	var out RevealedCredentials
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		if err := s.Auth.RequireChannelTx(ctx, p, channelID, auth.Configure, tx); err != nil {
			return err
		}
		r, err := loadRevision(ctx, tx, channelID, revisionID)
		if err != nil {
			return err
		}
		out, err = s.decrypt(r)
		if err != nil {
			return err
		}
		return audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "source.credentials_revealed", ObjectID: revisionID})
	})
	if err != nil {
		return RevealedCredentials{}, err
	}
	return out, nil
}
func (s *SourceService) Export(ctx context.Context, p auth.Principal, channels []id.ID) (ExportFile, error) {
	var out ExportFile
	if len(channels) < 1 || len(channels) > 32 {
		return out, auth.ErrInvalid
	}
	seen := map[id.ID]bool{}
	for _, c := range channels {
		if _, err := id.Parse(string(c)); err != nil || seen[c] {
			return out, auth.ErrInvalid
		}
		seen[c] = true
	}
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		if err := s.Auth.RequireAdminTx(ctx, p, tx); err != nil {
			return err
		}
		for _, c := range channels {
			if err := s.Auth.RequireChannelTx(ctx, p, c, auth.Configure, tx); err != nil {
				return err
			}
		}
		// One statement locks all selected channel versions in deterministic order;
		// subsequent immutable revision reads belong to this source snapshot.
		rows, err := tx.Query(ctx, "SELECT id,site_id,channel_no,channel_name,current_revision_id FROM channels WHERE id=ANY($1::uuid[]) ORDER BY id FOR SHARE", channels)
		if err != nil {
			return err
		}
		var items []ExportItem
		for rows.Next() {
			var item ExportItem
			var siteID id.ID
			var revision *id.ID
			if err := rows.Scan(&item.ChannelID, &siteID, &item.ChannelNo, &item.ChannelName, &revision); err != nil {
				rows.Close()
				return err
			}
			if siteID != s.secret.SiteID {
				rows.Close()
				return auth.ErrConflict
			}
			item.SourceRevisionID = revision
			item.SourceState = "not_configured"
			item.RTSPPort = 554
			items = append(items, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) != len(channels) {
			return auth.ErrNotFound
		}
		for i := range items {
			if items[i].SourceRevisionID == nil {
				continue
			}
			r, err := loadRevision(ctx, tx, items[i].ChannelID, *items[i].SourceRevisionID)
			if err != nil {
				return err
			}
			plain, err := s.decrypt(r)
			if err != nil {
				return err
			}
			items[i].IP = r.Revision.Config.IP
			items[i].RTSPPort = r.Revision.Config.RTSPPort
			items[i].MainPath = r.Revision.Config.MainPath
			items[i].SubPath = r.Revision.Config.SubPath
			items[i].ONVIFPort = r.Revision.Config.ONVIFPort
			items[i].Username = plain.Username
			items[i].Password = plain.Password
			items[i].SourceState = "configured"
			items[i].SourceID = &r.Revision.SourceID
			items[i].SourceRevisionNumber = &r.Revision.Number
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ChannelNo < items[j].ChannelNo })
		details, err := json.Marshal(map[string]any{"channel_ids": channels})
		if err != nil {
			return err
		}
		if err := audit.Append(ctx, tx, audit.Entry{ActorID: p.UserID, Action: "source.config_exported", ObjectID: s.secret.SiteID, Details: details}); err != nil {
			return err
		}
		out = ExportFile{FormatVersion: 1, SiteID: s.secret.SiteID, ExportedAt: time.Now().UTC(), Channels: items}
		return nil
	})
	if err != nil {
		return ExportFile{}, err
	}
	return out, nil
}

// savedCredentialsTx decrypts the applied revision's credentials for internal
// use (a probe, never a response).
//
// It exists so the ONVIF probe can reuse the account the operator already saved
// instead of asking for it again — PRD CH-13 says ONVIF shares the source's
// credentials. The caller must hold a transaction that already authorized the
// channel; this function deliberately does no authorization of its own, because
// a second check here would suggest the first one was optional.
//
// A channel with no applied revision, or a revision with no password, returns
// empty strings rather than an error: "there is nothing saved" is a normal state
// for a channel being configured for the first time.
func (s *SourceService) savedCredentialsTx(ctx context.Context, tx pgx.Tx, channelID id.ID) ([]byte, []byte, error) {
	var revisionID *id.ID
	if err := tx.QueryRow(ctx, "SELECT current_revision_id FROM channels WHERE id=$1", channelID).Scan(&revisionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, auth.ErrNotFound
		}
		return nil, nil, err
	}
	if revisionID == nil {
		return nil, nil, nil
	}
	r, err := loadRevision(ctx, tx, channelID, *revisionID)
	if err != nil {
		return nil, nil, err
	}
	// Bytes, not strings: the caller zeroes them when done, and a string cannot
	// be cleared. This is the same reason decrypt() hands back byte slices.
	username, err := s.secret.OpenCredential(s.secret.SiteID, r.Revision.ChannelID, r.Revision.ID, "username", r.Username)
	if err != nil {
		return nil, nil, err
	}
	password, err := s.secret.OpenCredential(s.secret.SiteID, r.Revision.ChannelID, r.Revision.ID, "password", r.Password)
	if err != nil {
		clear(username)
		return nil, nil, err
	}
	return username, password, nil
}
