package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/pjg/face_me_bro/schemas"
)

type migration struct {
	version string
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(schemas.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		contents, err := fs.ReadFile(schemas.Migrations, path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		version := strings.TrimSuffix(entry.Name(), path.Ext(entry.Name()))
		migrations = append(migrations, migration{version: version, name: entry.Name(), sql: string(contents)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for i := 1; i < len(migrations); i++ {
		if migrations[i-1].version == migrations[i].version {
			return nil, fmt.Errorf("duplicate migration version %q", migrations[i].version)
		}
	}
	return migrations, nil
}

func applyMigrations(ctx context.Context, database *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if err := validateSchemaVersions(ctx, database, migrations); err != nil {
		return err
	}

	for _, migration := range migrations {
		applied, err := migrationApplied(ctx, database, migration.version)
		if err != nil {
			return fmt.Errorf("check migration %q: %w", migration.name, err)
		}
		if applied {
			continue
		}

		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %q: %w", migration.name, err)
		}
		if _, err := transaction.ExecContext(ctx, migration.sql); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply migration %q: %w", migration.name, err)
		}
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES (?, CURRENT_TIMESTAMP)",
			migration.version,
		); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("record migration %q: %w", migration.name, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %q: %w", migration.name, err)
		}
	}
	return nil
}

func validateSchemaVersions(ctx context.Context, database *sql.DB, migrations []migration) error {
	var table string
	if err := database.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'").Scan(&table); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	known := make(map[string]struct{}, len(migrations))
	for _, migration := range migrations {
		known[migration.version] = struct{}{}
	}
	rows, err := database.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return err
		}
		if _, ok := known[version]; !ok {
			return fmt.Errorf("database schema migration %q is newer or unsupported", version)
		}
	}
	return rows.Err()
}

// ValidateInvariants checks invariants that must hold before services use the
// database. It intentionally avoids SQLite-specific application behavior.
func ValidateInvariants(ctx context.Context, database *sql.DB) error {
	var foreignKeys int
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign-key setting: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign-key enforcement is disabled")
	}
	for _, table := range []string{"schema_migrations", "photo_assets", "photo_paths", "faces", "people", "jobs"} {
		var name string
		if err := database.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name); err != nil {
			return fmt.Errorf("required table %q is missing: %w", table, err)
		}
	}
	return nil
}

func migrationApplied(ctx context.Context, database *sql.DB, version string) (bool, error) {
	var exists int
	err := database.QueryRowContext(ctx,
		"SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations' LIMIT 1",
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	err = database.QueryRowContext(ctx,
		"SELECT 1 FROM schema_migrations WHERE version = ? LIMIT 1", version,
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
