package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pjg/face_me_bro/internal/db"
)

func TestHelpListsCommandsWithoutOpeningDatabase(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Usage: face-me-bro", "collection", "cluster", "Global options", "help <command>"} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("help output is missing %q", text)
		}
	}
}

func TestCommandHelpIncludesUsageParametersAndDescription(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"help", "face"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Usage: face-me-bro face", "Inspect or change", "--face-id", "--yes"} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("face help output is missing %q", text)
		}
	}
}

func TestHelpFlagCanSelectACommandWithoutOpeningDatabase(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--help", "search"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Usage: face-me-bro search") {
		t.Fatalf("unexpected help output: %s", output.String())
	}
}

func TestReadOnlyCLIListsJobsDuringWorkerWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "catalog.sqlite3")
	database, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO jobs (id, kind, priority, status, payload_json, created_at) VALUES ('worker-job', 'analysis', 1, 'running', '{}', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	configData, err := json.Marshal(map[string]string{"runtime_data_root": dir, "database_path": databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	var wait sync.WaitGroup
	wait.Add(1)
	var runErr error
	go func() {
		defer wait.Done()
		runErr = run(ctx, []string{"--config", configPath, "job", "list"}, &output, &output)
	}()
	wait.Wait()
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("read-only job list failed during worker write: %v", runErr)
	}
}
