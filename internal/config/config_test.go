package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadUsesDefaultsWhenConfigurationDoesNotExist(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing", "config.json")
	got, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.ConfigFile != configPath {
		t.Fatalf("ConfigFile = %q, want %q", got.ConfigFile, configPath)
	}
	if filepath.Base(got.DatabasePath) != defaultDatabase {
		t.Fatalf("DatabasePath = %q, want default database name", got.DatabasePath)
	}
	if got.RuntimeDataRoot == "" || got.ModelRoot == "" {
		t.Fatal("expected default runtime and model paths")
	}
	if got.NeighborSearchMethod != "exact" {
		t.Fatalf("NeighborSearchMethod = %q, want exact", got.NeighborSearchMethod)
	}
}

func TestLoadResolvesConfiguredRootAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "settings.json")
	settings := File{
		RuntimeDataRoot: "runtime",
		DatabasePath:    "db/catalog.sqlite3",
		ModelRoot:       "/models/insightface",
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	wantRoot := filepath.Join(dir, "runtime")
	if got.RuntimeDataRoot != wantRoot {
		t.Errorf("RuntimeDataRoot = %q, want %q", got.RuntimeDataRoot, wantRoot)
	}
	if got.DatabasePath != filepath.Join(wantRoot, "db", "catalog.sqlite3") {
		t.Errorf("DatabasePath = %q", got.DatabasePath)
	}
	if got.ModelRoot != filepath.FromSlash("/models/insightface") {
		t.Errorf("ModelRoot = %q", got.ModelRoot)
	}
}

func TestLoadAcceptsConfigurationDirectory(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, defaultConfigFile)
	if err := os.WriteFile(configPath, []byte(`{"runtime_data_root":"data"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.ConfigFile != configPath {
		t.Errorf("ConfigFile = %q, want %q", got.ConfigFile, configPath)
	}
}

func TestDefaultRuntimeDataRootUsesPlatformConvention(t *testing.T) {
	root, err := defaultRuntimeDataRoot()
	if err != nil {
		t.Fatal(err)
	}
	if root == "" {
		t.Fatal("default root is empty")
	}
	if runtime.GOOS == "darwin" && filepath.Base(root) != "face_me_bro" {
		t.Errorf("unexpected macOS default root: %q", root)
	}
}
