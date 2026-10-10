package db

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open opens a SQLite database and applies all embedded migrations.
func Open(ctx context.Context, filename string) (*sql.DB, error) {
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
	if err := applyMigrations(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}
