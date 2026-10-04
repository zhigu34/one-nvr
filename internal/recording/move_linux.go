//go:build linux

package recording

import "golang.org/x/sys/unix"

func renameNoReplace(from int, source string, to int, target string) error {
	return unix.Renameat2(from, source, to, target, unix.RENAME_NOREPLACE)
}
