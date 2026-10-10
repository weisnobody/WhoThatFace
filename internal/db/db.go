package db

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Open opens a SQLite database and applies all embedded migrations.
func Open(ctx context.Context, filename string) (*sql.DB, error) {
	return OpenWithOptions(ctx, filename, OpenOptions{})
}

// Backup copies a database file to a new, exclusive backup file.
func Backup(filename, backupDir string) (string, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return "", fmt.Errorf("stat database for backup: %w", err)
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	name := filepath.Join(backupDir, filepath.Base(filename)+"."+time.Now().UTC().Format("20060102T150405.000000000Z")+".bak")
	input, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer input.Close()
	output, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return "", err
	}
	if err := output.Close(); err != nil {
		return "", err
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("database is empty")
	}
	return name, nil
}

// Restore replaces a database from a backup. Callers must ensure no database
// connection is open; the CLI performs this operation before app startup.
func Restore(filename, backup string) error {
	input, err := os.Open(backup)
	if err != nil {
		return fmt.Errorf("open restore source: %w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("restore source is empty or unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		return err
	}
	temporary := filename + ".restore.tmp"
	output, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		_ = os.Remove(temporary)
		return err
	}
	if err := output.Close(); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, filename); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// OpenOptions controls database startup behavior.
type OpenOptions struct {
	// NoMigrate refuses to create or upgrade the schema.
	NoMigrate bool
	// BackupDir receives a copy before pending migrations are applied.
	BackupDir string
	// SkipMigrations avoids migration checks for commands that do not own
	// schema upgrades. The schema is still required by ValidateInvariants.
	SkipMigrations bool
	// ReadOnly opens an existing database without permitting writes.
	ReadOnly bool
}

// OpenWithOptions opens a SQLite database and applies migrations unless
// NoMigrate is set.
func OpenWithOptions(ctx context.Context, filename string, options OpenOptions) (*sql.DB, error) {
	if options.NoMigrate {
		if _, err := os.Stat(filename); err != nil {
			return nil, fmt.Errorf("open database without migrations: %w", err)
		}
	}
	dsn, err := sqliteDSN(filename, options.ReadOnly)
	if err != nil {
		return nil, fmt.Errorf("build sqlite DSN: %w", err)
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)

	if options.BackupDir != "" && !options.NoMigrate && !options.SkipMigrations {
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
	} else if !options.SkipMigrations {
		if err := applyMigrations(ctx, database); err != nil {
			database.Close()
			return nil, err
		}
	}
	if err := ValidateInvariants(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func sqliteDSN(filename string, readOnly bool) (string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return "", err
	}
	values := url.Values{}
	if readOnly {
		values.Set("mode", "ro")
	}
	values.Add("_pragma", "busy_timeout=10000")
	values.Add("_pragma", "foreign_keys=1")
	if !readOnly {
		values.Add("_pragma", "journal_mode=WAL")
		values.Add("_pragma", "synchronous=NORMAL")
	}
	return (&url.URL{Scheme: "file", Path: absolute, RawQuery: values.Encode()}).String(), nil
}

// WithBusyRetry retries an operation when SQLite reports a transient lock.
// The connection-level busy timeout handles ordinary contention; this helper
// covers lock transitions such as WAL setup and cross-process migrations.
func WithBusyRetry(ctx context.Context, attempts int, operation func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = operation()
		if !IsBusy(err) || attempt == attempts-1 {
			return err
		}
		delay := time.Duration(25*(1<<min(attempt, 6))) * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func IsBusy(err error) bool {
	if err == nil {
		return false
	}
	value := strings.ToLower(err.Error())
	return strings.Contains(value, "sqlite_busy") || strings.Contains(value, "database is locked")
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func hasPendingMigrations(ctx context.Context, database *sql.DB) (bool, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return false, err
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer connection.Close()
	if err := WithBusyRetry(ctx, 8, func() error {
		_, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE")
		return err
	}); err != nil {
		return false, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer connection.ExecContext(context.Background(), "ROLLBACK")
	var table string
	if err := connection.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'").Scan(&table); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	for _, migration := range migrations {
		applied, err := migrationApplied(ctx, connection, migration.version)
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
