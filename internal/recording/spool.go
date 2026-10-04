package recording

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zhigu34/one-nvr/internal/id"
	"golang.org/x/sys/unix"
)

const spoolQuota int64 = 256 << 20

var spoolName = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

type Spool struct{ Dir string }

func (s *Spool) open(ctx context.Context) (*os.Root, *os.File, error) {
	if !filepath.IsAbs(s.Dir) {
		return nil, nil, ErrCompletionUnavailable
	}
	if info, err := os.Lstat(s.Dir); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return nil, nil, ErrCompletionUnavailable
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return nil, nil, ErrCompletionUnavailable
	}
	info, err := os.Lstat(s.Dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, nil, ErrCompletionUnavailable
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, nil, ErrCompletionUnavailable
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, nil, ErrCompletionUnavailable
	}
	lock, err := root.OpenFile(".lock", os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW, 0600)
	if err != nil {
		root.Close()
		return nil, nil, ErrCompletionUnavailable
	}
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return root, lock, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			lock.Close()
			root.Close()
			return nil, nil, ErrCompletionUnavailable
		}
		select {
		case <-ctx.Done():
			lock.Close()
			root.Close()
			return nil, nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func closeSpool(root *os.Root, lock *os.File) {
	unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	lock.Close()
	root.Close()
}
func syncDirectory(root *os.Root) error {
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func entries(root *os.Root) ([]os.DirEntry, error) {
	d, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadDir(-1)
}
func readCompletion(root *os.Root, name string) (Completion, []byte, error) {
	var c Completion
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 || info.Mode().Perm() != 0600 {
		return c, nil, ErrCompletionInvalid
	}
	f, err := root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return c, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return c, nil, ErrCompletionInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &c) != nil || c.validate() != nil {
		return c, nil, ErrCompletionInvalid
	}
	return c, raw, nil
}
func (s *Spool) Put(ctx context.Context, c Completion) error {
	if c.validate() != nil {
		return ErrCompletionInvalid
	}
	raw, err := json.Marshal(c)
	if err != nil || len(raw) > 65536 {
		return ErrCompletionInvalid
	}
	root, lock, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer closeSpool(root, lock)
	name := c.sourceKey() + ".json"
	if _, err := root.Lstat(name); err == nil {
		old, _, err := readCompletion(root, name)
		if err != nil || old.sourceKey() != c.sourceKey() {
			return ErrCompletionUnavailable
		}
		return syncDirectory(root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrCompletionUnavailable
	}
	listing, err := entries(root)
	if err != nil {
		return err
	}
	var used int64
	for _, entry := range listing {
		if entry.Name() == ".lock" {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return ErrCompletionUnavailable
		}
		if info.Size() > spoolQuota-used {
			return ErrCompletionUnavailable
		}
		used += info.Size()
	}
	if int64(len(raw)) > spoolQuota-used {
		return ErrCompletionUnavailable
	}
	nonce, err := id.New()
	if err != nil {
		return err
	}
	temp := ".pending-" + string(nonce) + ".tmp"
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return ErrCompletionUnavailable
	}
	if err = root.Link(temp, name); err != nil {
		return ErrCompletionUnavailable
	}
	if err = syncDirectory(root); err != nil {
		return ErrCompletionUnavailable
	}
	if err = root.Remove(temp); err != nil {
		return ErrCompletionUnavailable
	}
	return syncDirectory(root)
}
func (s *Spool) Drain(ctx context.Context, accept func(context.Context, Completion) error) error {
	root, lock, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer closeSpool(root, lock)
	listing, err := entries(root)
	if err != nil {
		return err
	}
	var damaged error
	for _, entry := range listing {
		name := entry.Name()
		if !spoolName.MatchString(name) && !(strings.HasPrefix(name, ".pending-") && strings.HasSuffix(name, ".tmp")) {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c, _, err := readCompletion(root, name)
		if err != nil {
			damaged = ErrCompletionInvalid
			continue
		}
		if spoolName.MatchString(name) && name != c.sourceKey()+".json" {
			damaged = ErrCompletionInvalid
			continue
		}
		if err = accept(ctx, c); err != nil {
			return err
		}
		if err = root.Remove(name); err != nil {
			return err
		}
		if err = syncDirectory(root); err != nil {
			return err
		}
	}
	return damaged
}
