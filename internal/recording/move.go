package recording

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrPublicationConflict = errors.New("recording_publication_conflict")
var ErrPublicationUnavailable = errors.New("recording_publication_unavailable")

func safeRelative(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path && path != ".." && !strings.HasPrefix(path, "../")
}
func openParent(root *os.Root, path string, create bool) (*os.File, error) {
	if !safeRelative(path) {
		return nil, ErrPublicationUnavailable
	}
	current, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	relative := filepath.Dir(path)
	if relative == "." {
		return current, nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		next, err := unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			if err := unix.Mkdirat(int(current.Fd()), part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
				current.Close()
				return nil, err
			}
			if err := current.Sync(); err != nil {
				current.Close()
				return nil, err
			}
			next, err = unix.Openat(int(current.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		current.Close()
		if err != nil {
			return nil, err
		}
		current = os.NewFile(uintptr(next), part)
	}
	return current, nil
}
func openRegularAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, ErrPublicationConflict
	}
	return file, nil
}
func sameClosedFile(expected, actual os.FileInfo) bool {
	return os.SameFile(expected, actual) && expected.Size() == actual.Size() && expected.ModTime().Equal(actual.ModTime()) && actual.Mode().IsRegular()
}
func atomicPublish(ctx context.Context, root *os.Root, original, target string, expected os.FileInfo) error {
	return atomicPublishWith(ctx, root, original, target, expected, renameNoReplace, func(dir *os.File) error { return dir.Sync() })
}
func atomicPublishWith(ctx context.Context, root *os.Root, original, target string, expected os.FileInfo, move func(int, string, int, string) error, syncDir func(*os.File) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if expected == nil || !expected.Mode().IsRegular() || original == target {
		return ErrPublicationConflict
	}
	from, err := openParent(root, original, false)
	if err != nil {
		return ErrPublicationUnavailable
	}
	defer from.Close()
	source, err := openRegularAt(from, filepath.Base(original))
	if err != nil {
		return ErrPublicationConflict
	}
	defer source.Close()
	current, err := source.Stat()
	if err != nil || !sameClosedFile(expected, current) {
		return ErrPublicationConflict
	}
	to, err := openParent(root, target, true)
	if err != nil {
		return ErrPublicationUnavailable
	}
	defer to.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	// No copy/unlink fallback: EXDEV and unsupported kernel/filesystem are failures.
	if err := move(int(from.Fd()), filepath.Base(original), int(to.Fd()), filepath.Base(target)); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return ErrPublicationConflict
		}
		return ErrPublicationUnavailable
	}
	moved, err := openRegularAt(to, filepath.Base(target))
	if err != nil {
		return ErrPublicationConflict
	}
	defer moved.Close()
	info, err := moved.Stat()
	if err != nil || !sameClosedFile(expected, info) {
		return ErrPublicationConflict
	}
	if err := syncDir(to); err != nil {
		return ErrPublicationUnavailable
	}
	if err := syncDir(from); err != nil {
		return ErrPublicationUnavailable
	}
	return nil
}
