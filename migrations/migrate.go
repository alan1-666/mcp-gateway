// Package migrations applies embedded, immutable schema changes in order.
package migrations

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback(context.Background())
	// One transaction-scoped lock protects both bootstrap and all migration DDL.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(734885827194)`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return err
	}
	entries, err := files.ReadDir(".")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		body, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		checksum := hex.EncodeToString(digest[:])
		var applied string
		err = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name=$1`, name).Scan(&applied)
		if err == nil {
			if applied != checksum {
				return fmt.Errorf("migration %s checksum changed after application", name)
			}
			continue
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, name, checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
