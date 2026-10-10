package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAndStatusReportResolvedPaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	settings := map[string]string{"runtime_data_root": filepath.Join(dir, "runtime")}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	application, err := Open(context.Background(), configPath, false)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer application.Close()
	status, err := application.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.DatabasePath != filepath.Join(settings["runtime_data_root"], "face_me_bro.sqlite3") {
		t.Errorf("DatabasePath = %q", status.DatabasePath)
	}
	if _, err := os.Stat(status.DatabasePath); err != nil {
		t.Errorf("database was not created: %v", err)
	}
}
