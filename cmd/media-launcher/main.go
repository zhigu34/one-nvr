// media-launcher runs only inside the private ZLM container's network namespace.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/media/egress"
	"io"
	"os"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "media container network boundary unavailable")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 3 {
		return egress.ErrBoundary
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		return egress.ErrBoundary
	}
	defer f.Close()
	var c egress.Config
	decoder := json.NewDecoder(io.LimitReader(f, 65537))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return egress.ErrBoundary
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return egress.ErrBoundary
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := egress.Resolve(ctx, c)
	if err != nil {
		return err
	}
	if err := egress.Install(p); err != nil {
		return err
	}
	if err := egress.DropPrivileges(); err != nil {
		return err
	}
	return syscall.Exec(os.Args[2], os.Args[2:], os.Environ())
}
