//go:build !linux

package egress

func Install(Policy) error  { return ErrBoundary }
func DropPrivileges() error { return ErrBoundary }
