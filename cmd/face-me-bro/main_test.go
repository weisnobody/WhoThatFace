package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjg/face_me_bro/internal/db"
	"github.com/pjg/face_me_bro/internal/jobs"
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

func TestRestoreRequiresExplicitConfirmation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	data, err := json.Marshal(map[string]string{"runtime_data_root": dir, "database_path": filepath.Join(dir, "catalog.sqlite3")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"--config", configPath, "restore", "--input", filepath.Join(dir, "backup.bak")}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("expected restore confirmation error")
	}
}

func TestExportModesProduceVersionedFiles(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "catalog.sqlite3")
	database, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	configPath := filepath.Join(dir, "config.json")
	data, err := json.Marshal(map[string]string{"runtime_data_root": dir, "database_path": databasePath, "export_root": dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"metadata", "assignments", "embeddings"} {
		outputPath := filepath.Join(dir, kind+".json")
		var output bytes.Buffer
		if err := run(ctx, []string{"--config", configPath, "--json", "export", kind, "--output", outputPath}, &output, &output); err != nil {
			t.Fatalf("export %s: %v", kind, err)
		}
		var envelopeValue struct {
			Result struct {
				Kind string `json:"kind"`
			} `json:"result"`
		}
		if err := json.Unmarshal(output.Bytes(), &envelopeValue); err != nil {
			t.Fatal(err)
		}
		if envelopeValue.Result.Kind != kind {
			t.Fatalf("export kind = %q, want %q", envelopeValue.Result.Kind, kind)
		}
		if _, err := os.Stat(outputPath); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanRetryResumesPersistedItemsAndEnqueuesAnalysis(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := filepath.Join(dir, "photos")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(dir, "catalog.sqlite3")
	database, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection-scan', 'Photos', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, root); err != nil {
		database.Close()
		t.Fatal(err)
	}
	database.Close()
	configPath := filepath.Join(dir, "config.json")
	data, err := json.Marshal(map[string]string{"runtime_data_root": dir, "database_path": databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"--config", configPath, "scan", "--collection-id", "collection-scan"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	database, err = db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	if err := database.QueryRowContext(ctx, `SELECT id FROM scan_runs WHERE collection_id = 'collection-scan' ORDER BY created_at DESC LIMIT 1`).Scan(&runID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE scan_runs SET status = 'completed-with-errors' WHERE id = ?`, runID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	database.Close()
	if err := os.Remove(filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(ctx, []string{"--config", configPath, "scan", "retry", runID}, &output, &output); err != nil {
		t.Fatal(err)
	}
	database, err = db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var status string
	if err := database.QueryRowContext(ctx, `SELECT status FROM scan_runs WHERE id = ?`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("scan status = %q, want completed", status)
	}
	var missing int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM scan_items WHERE scan_run_id = ? AND status = 'unsupported'`, runID).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 1 {
		t.Fatalf("persisted scan item count = %d, want 1", missing)
	}
	var activePaths int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM photo_paths WHERE collection_id = 'collection-scan' AND missing_at IS NULL`).Scan(&activePaths); err != nil {
		t.Fatal(err)
	}
	if activePaths != 0 {
		t.Fatalf("active paths = %d, want 0 after removal", activePaths)
	}
	var jobsCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE kind = 'analysis'`).Scan(&jobsCount); err != nil {
		t.Fatal(err)
	}
	if jobsCount != 2 {
		t.Fatalf("analysis jobs = %d, want initial and retry jobs", jobsCount)
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

func TestReadOnlyListCommands(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "catalog.sqlite3")
	database, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	configPath := filepath.Join(dir, "config.json")
	configData, err := json.Marshal(map[string]string{"runtime_data_root": dir, "database_path": databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"collection", "list"},
		{"pipeline", "list"},
		{"account", "list"},
		{"people", "list"},
		{"face", "list"},
		{"cluster", "list"},
		{"audit", "list"},
	}
	for _, command := range commands {
		var output bytes.Buffer
		args := append([]string{"--config", configPath, "--json"}, command...)
		if err := run(ctx, args, &output, &output); err != nil {
			t.Fatalf("%v failed: %v", command, err)
		}
	}
}

func TestListCommandsSupportZeroBasedPagination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	databasePath := filepath.Join(dir, "catalog.sqlite3")
	database, err := db.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"A", "B", "C"} {
		if _, err := database.ExecContext(ctx, `INSERT INTO people (id, name, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, fmt.Sprintf("person-%d", index), name); err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	database.Close()
	configPath := filepath.Join(dir, "config.json")
	configData, err := json.Marshal(map[string]string{"runtime_data_root": dir, "database_path": databasePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"--config", configPath, "people", "list", "--limit", "1", "--page", "1"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\tB\n") || strings.Contains(output.String(), "\tA\n") || strings.Contains(output.String(), "\tC\n") {
		t.Fatalf("unexpected paginated output: %q", output.String())
	}
}

func TestJobStatusReportsProgressAndEstimate(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	started := time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339Nano)
	if _, err := database.ExecContext(ctx, `INSERT INTO jobs (id, kind, status, payload_json, started_at, created_at) VALUES ('job-status', 'analysis', 'running', '{}', ?, ?)`, started, started); err != nil {
		t.Fatal(err)
	}
	for index, status := range []string{"completed", "completed", "running", "failed"} {
		if _, err := database.ExecContext(ctx, `INSERT INTO job_items (id, job_id, item_key, status, updated_at) VALUES (?, 'job-status', ?, ?, ?)`, fmt.Sprintf("item-%d", index), fmt.Sprintf("asset-%d", index), status, started); err != nil {
			t.Fatal(err)
		}
	}
	report, err := currentJobStatus(ctx, jobs.NewStore(database), "job-status")
	if err != nil {
		t.Fatal(err)
	}
	if report.ItemsCompleted != 2 || report.ItemsProcessed != 3 || report.ItemsRemaining != 1 || report.FailedItems != 1 || report.PercentageComplete != 75 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.CurrentItem == nil || *report.CurrentItem != "asset-2" || report.EstimatedTimeRemaining == nil {
		t.Fatalf("missing current item or estimate: %+v", report)
	}
}
