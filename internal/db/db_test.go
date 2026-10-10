package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigrationsAndIsIdempotent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "face_me_bro.sqlite3")
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
	if migrationCount != 5 {
		t.Fatalf("migration count = %d, want 5", migrationCount)
	}

	for _, table := range []string{
		"photo_assets", "photo_paths", "faces", "people", "assignment_events",
		"current_face_assignments", "clustering_runs", "jobs", "accounts", "audit_events",
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
