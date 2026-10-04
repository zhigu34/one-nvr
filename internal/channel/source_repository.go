package channel

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/secrets"
)

// Cipher fields never become part of a public response or generic job payload.
type encryptedRevision struct {
	Revision           SourceRevision
	Username, Password secrets.EncryptedCredential
}

const revisionColumns = `id,channel_id,source_id,revision_no,host(ip),rtsp_port,main_path,sub_path,transport,onvif_port,username_summary,password_state,created_at,credential_key_id,username_nonce,username_ciphertext,password_nonce,password_ciphertext`

func scanRevision(row pgx.Row) (encryptedRevision, error) {
	var r encryptedRevision
	err := row.Scan(&r.Revision.ID, &r.Revision.ChannelID, &r.Revision.SourceID, &r.Revision.Number, &r.Revision.Config.IP, &r.Revision.Config.RTSPPort, &r.Revision.Config.MainPath, &r.Revision.Config.SubPath, &r.Revision.Config.Transport, &r.Revision.Config.ONVIFPort, &r.Revision.UsernameSummary, &r.Revision.PasswordState, &r.Revision.CreatedAt, &r.Username.KeyID, &r.Username.Nonce, &r.Username.Ciphertext, &r.Password.Nonce, &r.Password.Ciphertext)
	r.Password.KeyID = r.Username.KeyID
	r.Revision.CreatedAt = r.Revision.CreatedAt.UTC()
	if errors.Is(err, pgx.ErrNoRows) {
		return encryptedRevision{}, auth.ErrNotFound
	}
	return r, err
}
func loadRevision(ctx context.Context, tx pgx.Tx, channelID, revisionID id.ID) (encryptedRevision, error) {
	return scanRevision(tx.QueryRow(ctx, "SELECT "+revisionColumns+" FROM source_revisions WHERE id=$1 AND channel_id=$2", revisionID, channelID))
}
