package journal

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CurrentSchemaVersion is the newest embedded SQLite migration.
const CurrentSchemaVersion = 2

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func applyMigrations(ctx context.Context, database *sql.DB) error {
	return applyMigrationsFromFS(ctx, database, migrationFiles, CurrentSchemaVersion)
}

func applyMigrationsFromFS(ctx context.Context, database *sql.DB, files fs.FS, currentVersion int) error {
	migrations, err := migrationsFromFS(files, currentVersion)
	if err != nil {
		return err
	}

	if _, err := database.ExecContext(ctx, `
        CREATE TABLE IF NOT EXISTS schema_migrations (
            version INTEGER PRIMARY KEY,
            name TEXT NOT NULL,
            applied_at TEXT NOT NULL
        )
    `); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	for _, item := range migrations {
		var applied int
		err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", item.version).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", item.version, err)
		}
		if applied == 1 {
			continue
		}

		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", item.version, err)
		}
		if _, err := transaction.ExecContext(ctx, item.sql); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply migration %d: %w", item.version, err)
		}
		if _, err := transaction.ExecContext(
			ctx,
			"INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)",
			item.version,
			item.name,
			formatTime(time.Now()),
		); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("record migration %d: %w", item.version, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", item.version, err)
		}
	}
	return nil
}
func migrationsFromFS(files fs.FS, currentVersion int) ([]migration, error) {
	names, err := fs.Glob(files, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded migrations: %w", err)
	}
	result := make([]migration, 0, len(names))
	for _, name := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid migration filename %q", name)
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("parse migration filename %q: %w", name, err)
		}
		contents, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", name, err)
		}
		result = append(result, migration{version: version, name: parts[1], sql: string(contents)})
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].version != result[right].version {
			return result[left].version < result[right].version
		}
		return result[left].name < result[right].name
	})
	previousVersion := 0
	for _, item := range result {
		if item.version <= previousVersion {
			return nil, fmt.Errorf(
				"migration version %d (%q) must be greater than %d",
				item.version,
				item.name,
				previousVersion,
			)
		}
		previousVersion = item.version
	}
	if len(result) == 0 || result[len(result)-1].version != currentVersion {
		return nil, fmt.Errorf("embedded migrations do not end at schema version %d", currentVersion)
	}
	return result, nil
}
