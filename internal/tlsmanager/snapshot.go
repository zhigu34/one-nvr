package tlsmanager

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"github.com/zhigu34/one-nvr/internal/id"
	"io"
	"os"
	"time"
)

type snapshotManifest struct {
	ID         id.ID     `json:"id"`
	Digest     string    `json:"digest"`
	ImportedAt time.Time `json:"imported_at"`
}

// ReadSnapshot lets the private gateway validate the exact snapshot without
// database access. Historical validation is reserved for restoring its already
// committed listener; new applications always validate at the current time.
func ReadSnapshot(versionsDirectory string, certificateID id.ID, host string, now time.Time, historical bool) (Metadata, error) {
	parsed, err := id.Parse(string(certificateID))
	if err != nil || parsed != certificateID {
		return Metadata{}, validationError("tls_snapshot_invalid")
	}
	root, err := os.OpenRoot(versionsDirectory)
	if err != nil {
		return Metadata{}, err
	}
	defer root.Close()
	version, err := root.OpenRoot(string(certificateID))
	if err != nil {
		return Metadata{}, err
	}
	defer version.Close()
	info, err := version.Lstat(".snapshot.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return Metadata{}, validationError("tls_snapshot_invalid")
	}
	file, err := version.Open(".snapshot.json")
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	var manifest snapshotManifest
	dec := json.NewDecoder(io.LimitReader(file, 4097))
	dec.DisallowUnknownFields()
	if dec.Decode(&manifest) != nil {
		return Metadata{}, validationError("tls_snapshot_invalid")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || manifest.ID != certificateID || manifest.ImportedAt.IsZero() || manifest.ImportedAt.After(now) {
		return Metadata{}, validationError("tls_snapshot_invalid")
	}
	expected, err := hex.DecodeString(manifest.Digest)
	if err != nil || len(expected) != 32 {
		return Metadata{}, validationError("tls_snapshot_invalid")
	}
	files, err := readPair(version)
	if err != nil {
		return Metadata{}, err
	}
	actual := pairDigest(files.chain, files.key)
	if !bytes.Equal(expected, actual[:]) {
		return Metadata{}, validationError("tls_snapshot_changed")
	}
	at := now
	if historical {
		at = manifest.ImportedAt
	}
	return Validate(files.chain, files.key, host, at)
}
