package db

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestOpenAppliesMigrationsAndIsIdempotent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "whothatface.sqlite3")
	ctx := context.Background()

	database, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	database, err = Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer database.Close()

	var migrationCount int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrationCount != 18 {
		t.Fatalf("migration count = %d, want 18", migrationCount)
	}

	for _, table := range []string{
		"photo_assets", "photo_paths", "faces", "people", "person_representatives", "assignment_events",
		"current_face_assignments", "clustering_runs", "jobs", "accounts", "audit_events", "possible_candidate_targets",
	} {
		var name string
		err := database.QueryRowContext(ctx,
			"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("find table %q: %v", table, err)
		}
		if name != table {
			t.Fatalf("table name = %q, want %q", name, table)
		}
	}
}

func TestOpenConfiguresWALAndPerConnectionPragmas(t *testing.T) {
	database, err := Open(context.Background(), filepath.Join(t.TempDir(), "settings.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(2)
	for index := 0; index < 2; index++ {
		var foreignKeys, busyTimeout string
		if err := database.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != "1" || busyTimeout != "10000" {
			t.Fatalf("connection %d pragmas = foreign_keys %s, busy_timeout %s", index, foreignKeys, busyTimeout)
		}
	}
	var journal string
	if err := database.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if journal != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journal)
	}
}

func TestReadOnlyConnectionRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.sqlite3")
	database, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	readOnly, err := OpenWithOptions(context.Background(), path, OpenOptions{NoMigrate: true, SkipMigrations: true, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, err := readOnly.Exec("CREATE TABLE should_fail (id INTEGER)"); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
}

func TestWithBusyRetryBacksOffAndRetries(t *testing.T) {
	attempts := 0
	err := WithBusyRetry(context.Background(), 3, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("SQLITE_BUSY: database is locked")
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Fatalf("retry result = %v after %d attempts", err, attempts)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := WithBusyRetry(ctx, 10, func() error { return errors.New("database is locked") }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled retry error = %v", err)
	}
}

func TestConcurrentOpenSerializesMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.sqlite3")
	const connections = 4
	var wait sync.WaitGroup
	errors := make(chan error, connections)
	for index := 0; index < connections; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			database, err := Open(context.Background(), path)
			if err != nil {
				errors <- err
				return
			}
			database.Close()
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatalf("concurrent open failed: %v", err)
	}
}
