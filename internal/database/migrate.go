package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/zhigu34/one-nvr/migrations"
	"io/fs"
	"strings"
)

func Migrate(ctx context.Context, db *DB) error {
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return err
	}
	return db.WithinTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(170017)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())"); err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			raw, err := migrations.Files.ReadFile(entry.Name())
			if err != nil {
				return err
			}
			hash := sha256.Sum256(raw)
			checksum := hex.EncodeToString(hash[:])
			var previous string
			err = tx.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE name=$1", entry.Name()).Scan(&previous)
			if err == nil {
				if previous != checksum {
					return fmt.Errorf("migration %s checksum mismatch", entry.Name())
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if _, err = tx.Exec(ctx, string(raw)); err != nil {
				return fmt.Errorf("migration %s: %w", entry.Name(), err)
			}
			if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)", entry.Name(), checksum); err != nil {
				return err
			}
		}
		return nil
	})
}

// Ready verifies the installed schema without running DDL in health requests.
func Ready(ctx context.Context, db *DB) error {
	if err := db.Pool.Ping(ctx); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations.Files, ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		hash := sha256.Sum256(raw)
		var stored string
		if err := db.Pool.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE name=$1", entry.Name()).Scan(&stored); err != nil {
			return err
		}
		if stored != hex.EncodeToString(hash[:]) {
			return fmt.Errorf("migration checksum mismatch")
		}
	}
	return nil
}
