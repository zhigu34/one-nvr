package tlsmanager

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/id"
	"os"
	"path/filepath"
	"time"
)

type Version struct {
	ID     id.ID  `json:"id"`
	Source string `json:"source"`
	Metadata
}
type State struct {
	Source            string     `json:"source"`
	AutoApply         bool       `json:"auto_apply"`
	ActiveID          *id.ID     `json:"active_id"`
	PreviousID        *id.ID     `json:"previous_id"`
	DesiredID         *id.ID     `json:"desired_id"`
	CandidateID       *id.ID     `json:"candidate_id"`
	State             string     `json:"state"`
	Version           int64      `json:"version"`
	CheckState        string     `json:"check_state"`
	CheckReason       string     `json:"check_reason"`
	ConsecutiveErrors int        `json:"consecutive_errors"`
	LastCheckAt       *time.Time `json:"last_check_at"`
	LastApplyAt       *time.Time `json:"last_apply_at"`
	ErrorCode         *string    `json:"error_code"`
	Protocol          string     `json:"protocol"`
	Certificates      []Version  `json:"certificates"`
}

const stateColumns = "source,auto_apply,active_id,previous_id,desired_id,candidate_id,state,version,check_state,check_reason,consecutive_errors,last_check_at,last_apply_at,error_code"

func scanState(row pgx.Row) (State, error) {
	var out State
	err := row.Scan(&out.Source, &out.AutoApply, &out.ActiveID, &out.PreviousID, &out.DesiredID, &out.CandidateID, &out.State, &out.Version, &out.CheckState, &out.CheckReason, &out.ConsecutiveErrors, &out.LastCheckAt, &out.LastApplyAt, &out.ErrorCode)
	return out, err
}
func lockTLS(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(170021)")
	return err
}
func scanVersion(row pgx.Row) (Version, error) {
	var out Version
	var metadata []byte
	err := row.Scan(&out.ID, &out.Source, &metadata)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(metadata, &out.Metadata)
	return out, err
}

// snapshot writes private files into a new version, then renames that complete
// directory. It never edits an existing version or stores PEM in PostgreSQL.
func (s *Service) snapshot(ctx context.Context, versionID id.ID, chain, key []byte) error {
	if _, err := id.Parse(string(versionID)); err != nil {
		return auth.ErrInvalid
	}
	if !filepath.IsAbs(s.DataDir) {
		return auth.ErrInvalid
	}
	base, err := os.OpenRoot(s.DataDir)
	if err != nil {
		return err
	}
	defer base.Close()
	if err = base.MkdirAll("tls/versions", 0700); err != nil {
		return err
	}
	root, err := base.OpenRoot("tls/versions")
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err = root.Lstat(string(versionID)); !errors.Is(err, os.ErrNotExist) {
		return auth.ErrConflict
	}
	temp := "." + string(versionID) + ".tmp"
	if err = root.Mkdir(temp, 0700); err != nil {
		return err
	}
	defer root.RemoveAll(temp)
	staging, err := root.OpenRoot(temp)
	if err != nil {
		return err
	}
	defer staging.Close()
	manifestTime := time.Now().UTC()
	if _, err = Validate(chain, key, s.Host, manifestTime); err != nil {
		return err
	}
	digest := pairDigest(chain, key)
	manifest, err := json.Marshal(snapshotManifest{ID: versionID, Digest: hex.EncodeToString(digest[:]), ImportedAt: manifestTime})
	if err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"fullchain.pem", chain}, {"privkey.pem", key}, {".snapshot.json", manifest}} {
		if err = ctx.Err(); err != nil {
			return err
		}
		f, err := staging.OpenFile(file.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(file.data)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	dir, err := staging.Open(".")
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = root.Rename(temp, string(versionID)); err != nil {
		return err
	}
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func (s *Service) readVersion(ctx context.Context, tx pgx.Tx, versionID id.ID) (Version, error) {
	version, err := scanVersion(tx.QueryRow(ctx, "SELECT id,source,metadata FROM tls_certificates WHERE id=$1", versionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, auth.ErrNotFound
	}
	if err != nil {
		return Version{}, err
	}
	var digest []byte
	if err = tx.QueryRow(ctx, "SELECT content_digest FROM tls_certificates WHERE id=$1", versionID).Scan(&digest); err != nil {
		return Version{}, err
	}
	root, err := os.OpenRoot(s.DataDir)
	if err != nil {
		return Version{}, err
	}
	defer root.Close()
	versions, err := root.OpenRoot("tls/versions")
	if err != nil {
		return Version{}, err
	}
	defer versions.Close()
	private, err := versions.OpenRoot(string(versionID))
	if err != nil {
		return Version{}, err
	}
	defer private.Close()
	files, err := readPair(private)
	if err != nil {
		return Version{}, err
	}
	sum := pairDigest(files.chain, files.key)
	if !bytes.Equal(digest, sum[:]) {
		return Version{}, validationError("tls_snapshot_changed")
	}
	metadata, err := Validate(files.chain, files.key, s.Host, time.Now())
	if err != nil {
		return Version{}, err
	}
	if metadata.LeafSHA256 != version.LeafSHA256 {
		return Version{}, validationError("tls_snapshot_changed")
	}
	version.Metadata = metadata
	return version, nil
}

func (s *Service) importTx(ctx context.Context, tx pgx.Tx, chain, key []byte, source string) (Version, error) {
	meta, err := Validate(chain, key, s.Host, time.Now())
	if err != nil {
		return Version{}, err
	}
	digest := pairDigest(chain, key)
	existing, err := scanVersion(tx.QueryRow(ctx, "SELECT id,source,metadata FROM tls_certificates WHERE content_digest=$1", digest[:]))
	if err == nil {
		return s.readVersion(ctx, tx, existing.ID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Version{}, err
	}
	versionID, err := id.New()
	if err != nil {
		return Version{}, err
	}
	if err = s.snapshot(ctx, versionID, chain, key); err != nil {
		return Version{}, err
	}
	metadata, err := json.Marshal(meta)
	if err != nil {
		return Version{}, err
	}
	version, err := scanVersion(tx.QueryRow(ctx, "INSERT INTO tls_certificates(id,source,metadata,content_digest) VALUES($1,$2,$3,$4) RETURNING id,source,metadata", versionID, source, metadata, digest[:]))
	return version, err
}
