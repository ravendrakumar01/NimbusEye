// Package migrate applies the SQL files in db/migrations to a database.
//
// A hand-rolled runner rather than a migration library: the requirement is
// "apply these files once, in order, and record it", which is about sixty lines.
// A library would add a dependency and a CLI to learn for no extra safety.
//
// Properties that matter:
//
//   - Each file runs inside its own transaction, so a failure leaves no partial
//     schema behind.
//   - An advisory lock serialises concurrent runners, so two API instances
//     starting at once cannot both apply the same migration.
//   - Applied files are recorded with a checksum. Editing a migration that has
//     already run is an error rather than a silent no-op, which is the failure
//     mode that leaves environments quietly diverged.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nimbuseye/db"
)

// lockID is an arbitrary but fixed key for the advisory lock.
const lockID int64 = 0x4e696d62 // "Nimb"

// Result describes what a run did.
type Result struct {
	Applied []string
	Skipped []string
}

// Run applies any migrations the database has not seen.
func Run(ctx context.Context, dsn string, log *slog.Logger) (Result, error) {
	var res Result

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return res, fmt.Errorf("migrate: connect: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return res, fmt.Errorf("migrate: acquire lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename    text PRIMARY KEY,
			checksum    text NOT NULL,
			applied_at  timestamptz NOT NULL DEFAULT now(),
			duration_ms integer NOT NULL
		)`); err != nil {
		return res, fmt.Errorf("migrate: create ledger: %w", err)
	}

	applied := map[string]string{}
	rows, err := conn.Query(ctx, `SELECT filename, checksum FROM schema_migrations`)
	if err != nil {
		return res, fmt.Errorf("migrate: read ledger: %w", err)
	}
	for rows.Next() {
		var name, sum string
		if err := rows.Scan(&name, &sum); err != nil {
			rows.Close()
			return res, err
		}
		applied[name] = sum
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	entries, err := db.Migrations.ReadDir("migrations")
	if err != nil {
		return res, fmt.Errorf("migrate: read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	// Filenames are numerically prefixed, so lexical order is execution order.
	sort.Strings(names)
	if len(names) == 0 {
		return res, errors.New("migrate: no migration files embedded")
	}

	for _, name := range names {
		body, err := db.Migrations.ReadFile("migrations/" + name)
		if err != nil {
			return res, err
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])

		if prev, seen := applied[name]; seen {
			if prev != checksum {
				return res, fmt.Errorf(
					"migrate: %s has already been applied but its contents changed "+
						"(recorded %s, now %s); add a new migration instead of editing this one",
					name, prev[:12], checksum[:12])
			}
			res.Skipped = append(res.Skipped, name)
			continue
		}

		start := time.Now()
		tx, err := conn.Begin(ctx)
		if err != nil {
			return res, fmt.Errorf("migrate: begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return res, fmt.Errorf("migrate: %s failed: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (filename, checksum, duration_ms) VALUES ($1,$2,$3)`,
			name, checksum, time.Since(start).Milliseconds()); err != nil {
			_ = tx.Rollback(ctx)
			return res, fmt.Errorf("migrate: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return res, fmt.Errorf("migrate: commit %s: %w", name, err)
		}

		log.Info("migration applied", "file", name, "took", time.Since(start).Round(time.Millisecond).String())
		res.Applied = append(res.Applied, name)
	}

	return res, nil
}
