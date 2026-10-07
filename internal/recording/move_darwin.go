//go:build darwin

package recording

import "golang.org/x/sys/unix"

func renameNoReplace(from int, source string, to int, target string) error {
	return unix.RenameatxNp(from, source, to, target, unix.RENAME_EXCL)
}
