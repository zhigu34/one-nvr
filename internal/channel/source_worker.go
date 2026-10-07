package channel

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

// PrivateConnection is for Worker execution only. It rechecks the supplied
// fresh network boundary before decrypting; it has no public HTTP route.
func (s *SourceService) PrivateConnection(ctx context.Context, channelID, revisionID id.ID, kind string, fresh NetworkPolicy) (zlm.ProxyInput, error) {
	var out zlm.ProxyInput
	err := s.DB.WithinTx(ctx, func(tx pgx.Tx) error {
		stored, err := loadRevision(ctx, tx, channelID, revisionID)
		if err != nil {
			return err
		}
		if err := stored.Revision.Config.Validate(fresh); err != nil {
			return err
		}
		path := stored.Revision.Config.MainPath
		switch kind {
		case "main":
		case "sub":
			path = stored.Revision.Config.SubPath
		default:
			return zlm.ErrInvalidMediaInput
		}
		if path == "" {
			return zlm.ErrInvalidMediaInput
		}
		plain, err := s.decrypt(stored)
		if err != nil {
			return err
		}
		raw, err := BuildRTSPURL(stored.Revision.Config, plain.Username, plain.Password, path)
		if err != nil {
			return err
		}
		out = zlm.ProxyInput{URL: raw, Transport: stored.Revision.Config.Transport}
		return nil
	})
	if err != nil {
		return zlm.ProxyInput{}, err
	}
	return out, nil
}
