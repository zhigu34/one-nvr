package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
	"golang.org/x/sys/unix"
)

type runReceipt struct {
	Version                                            int
	SiteID, PoolID, RunID, ChannelID, SourceRevisionID id.ID
	WorkRelativePath, Purpose, NativeTimezone          string
	Key                                                zlm.StreamKey
	CreatedAt                                          time.Time
}

func (s *Service) receipt(ctx context.Context, runID id.ID) (runReceipt, error) {
	var receipt runReceipt
	receipt.Version = 1
	receipt.NativeTimezone = "UTC"
	err := s.DB.Pool.QueryRow(ctx, `SELECT r.site_id,r.pool_id,r.id,r.channel_id,r.source_revision_id,r.work_relative_path,r.purpose,ss.vhost,ss.app,ss.stream,r.created_at FROM recording_runs r JOIN stream_sessions ss ON ss.id=r.stream_session_id WHERE r.id=$1 AND r.site_id=$2`, runID, s.siteID).Scan(&receipt.SiteID, &receipt.PoolID, &receipt.RunID, &receipt.ChannelID, &receipt.SourceRevisionID, &receipt.WorkRelativePath, &receipt.Purpose, &receipt.Key.VHost, &receipt.Key.App, &receipt.Key.Stream, &receipt.CreatedAt)
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	return receipt, err
}
func receiptPath(runID id.ID) string { return ".meta/recording-runs/" + string(runID) + ".json" }
func readReceipt(root *os.Root, path string) (runReceipt, error) {
	var result runReceipt
	parent, err := openParent(root, path, false)
	if err != nil {
		return result, err
	}
	defer parent.Close()
	f, err := openRegularAt(parent, filepath.Base(path))
	if err != nil {
		return result, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 65536 || info.Mode().Perm() != 0600 {
		return result, ErrPublicationConflict
	}
	decoder := json.NewDecoder(io.LimitReader(f, 65537))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil {
		return result, ErrPublicationConflict
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return result, ErrPublicationConflict
	}
	return result, nil
}
func (s *Service) DescribeRun(ctx context.Context, runID id.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	want, err := s.receipt(ctx, runID)
	if err != nil {
		return err
	}
	root, pool, err := s.Pools.OpenMediaRoot(ctx, want.PoolID)
	if err != nil {
		return err
	}
	defer root.Close()
	if pool.SiteID != want.SiteID || want.Key.Validate() != nil || !safeRelative(want.WorkRelativePath) {
		return ErrPublicationConflict
	}
	path := receiptPath(runID)
	if existing, err := readReceipt(root, path); err == nil {
		if existing != want {
			return ErrPublicationConflict
		}
		return nil
	} else if _, statErr := root.Lstat(path); !os.IsNotExist(statErr) {
		return ErrPublicationConflict
	}
	parent, err := openParent(root, path, true)
	if err != nil {
		return err
	}
	defer parent.Close()
	nonce, err := id.New()
	if err != nil {
		return err
	}
	temp := ".pending-" + string(nonce) + ".tmp"
	fd, err := unix.Openat(int(parent.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), temp)
	defer unix.Unlinkat(int(parent.Fd()), temp, 0)
	raw, _ := json.Marshal(want)
	_, err = f.Write(raw)
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := renameNoReplace(int(parent.Fd()), temp, int(parent.Fd()), filepath.Base(path)); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return err
		}
		other, readErr := readReceipt(root, path)
		otherRaw, _ := json.Marshal(other)
		if readErr != nil || !bytes.Equal(otherRaw, raw) {
			return ErrPublicationConflict
		}
	}
	return parent.Sync()
}
