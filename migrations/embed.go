// Package migrations embeds immutable SQL migrations in every application build.
package migrations

import "embed"

//go:embed *.sql
var Files embed.FS
