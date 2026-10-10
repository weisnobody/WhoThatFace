package db

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

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
	// BackupDir receives a copy before pending migrations are applied.
	BackupDir string
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
	if options.BackupDir != "" && !options.NoMigrate {
		pending, err := hasPendingMigrations(ctx, database)
		if err != nil {
			database.Close()
			return nil, fmt.Errorf("check pending migrations: %w", err)
		}
		if pending {
			if err := backupDatabase(filename, options.BackupDir); err != nil {
				database.Close()
				return nil, err
			}
		}
	}
	if options.NoMigrate {
		var tableName string
		if err := database.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'").Scan(&tableName); err != nil {
			database.Close()
			return nil, fmt.Errorf("database schema is unavailable with migrations disabled: %w", err)
		}
		migrations, err := loadMigrations()
		if err != nil {
			database.Close()
			return nil, err
		}
		if err := validateSchemaVersions(ctx, database, migrations); err != nil {
			database.Close()
			return nil, err
		}
	} else if err := applyMigrations(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	if err := ValidateInvariants(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func hasPendingMigrations(ctx context.Context, database *sql.DB) (bool, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return false, err
	}
	var table string
	if err := database.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'").Scan(&table); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	for _, migration := range migrations {
		applied, err := migrationApplied(ctx, database, migration.version)
		if err != nil {
			return false, err
		}
		if !applied {
			return true, nil
		}
	}
	return false, nil
}

func backupDatabase(filename, backupDir string) error {
	info, err := os.Stat(filename)
	if err != nil || info.Size() == 0 {
		return nil
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return fmt.Errorf("create migration backup directory: %w", err)
	}
	input, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("open database for migration backup: %w", err)
	}
	defer input.Close()
	backupPath := filepath.Join(backupDir, filepath.Base(filename)+".pre-migration-"+time.Now().UTC().Format("20060102T150405.000000000Z")+".bak")
	output, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create migration backup: %w", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy migration backup: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close migration backup: %w", err)
	}
	return nil
}
