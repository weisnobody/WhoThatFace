package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// Open opens a SQLite database and applies all embedded migrations.
func Open(ctx context.Context, filename string) (*sql.DB, error) {
	return OpenWithOptions(ctx, filename, OpenOptions{})
}

// OpenOptions controls database startup behavior.
type OpenOptions struct {
	// NoMigrate refuses to create or upgrade the schema.
	NoMigrate bool
}

// OpenWithOptions opens a SQLite database and applies migrations unless
// NoMigrate is set.
func OpenWithOptions(ctx context.Context, filename string, options OpenOptions) (*sql.DB, error) {
	if options.NoMigrate {
		if _, err := os.Stat(filename); err != nil {
			return nil, fmt.Errorf("open database without migrations: %w", err)
		}
	}
	database, err := sql.Open("sqlite", filename)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if _, err := database.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		database.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	if _, err := database.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		database.Close()
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}
	if options.NoMigrate {
		var tableName string
		if err := database.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'").Scan(&tableName); err != nil {
			database.Close()
			return nil, fmt.Errorf("database schema is unavailable with migrations disabled: %w", err)
		}
	} else if err := applyMigrations(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}
