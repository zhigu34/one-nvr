package storage

import (
	"github.com/zhigu34/one-nvr/internal/auth"
	"os"
	"path/filepath"
	"strings"
)

// openPool opens an existing directory through an allowed root. The returned
// descriptor bounds every subsequent file operation even during path renames.
func openPool(roots []string, path string) (*os.Root, string, error) {
	if !filepath.IsAbs(path) || len(path) > 4096 {
		return nil, "", auth.ErrInvalid
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return nil, "", auth.ErrInvalid
	}
	for _, allowed := range roots {
		base, err := filepath.EvalSymlinks(allowed)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(base, canonical)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			continue
		}
		parent, err := os.OpenRoot(base)
		if err != nil {
			continue
		}
		root, err := parent.OpenRoot(relative)
		parent.Close()
		if err == nil {
			return root, canonical, nil
		}
	}
	return nil, "", auth.ErrInvalid
}

func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) || a == string(filepath.Separator) || b == string(filepath.Separator)
}
