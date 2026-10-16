package config

import (
	"encoding/json"
	"fmt"
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
	if runtime.GOOS != "windows" && got.PythonPath != filepath.Join(got.RuntimeDataRoot, "venv", "bin", "python") {
		t.Fatalf("PythonPath = %q, want runtime virtual environment", got.PythonPath)
	}
	if got.NeighborSearchMethod != "exact" {
		t.Fatalf("NeighborSearchMethod = %q, want exact", got.NeighborSearchMethod)
	}
	if got.ServeAddress != defaultServeAddress {
		t.Fatalf("ServeAddress = %q, want %q", got.ServeAddress, defaultServeAddress)
	}
	if got.Provider != defaultProvider {
		t.Fatalf("Provider = %q, want %q", got.Provider, defaultProvider)
	}
	if got.RecognitionThresholds.DirectAssignmentSimilarity != 0.75 {
		t.Fatalf("DirectAssignmentSimilarity = %v, want 0.75", got.RecognitionThresholds.DirectAssignmentSimilarity)
	}
}

func TestLoadRejectsDirectAssignmentAtOrBelowHighSimilarity(t *testing.T) {
	for _, value := range []float32{0.50, 0.49} {
		t.Run("threshold-"+formatFloat(value), func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "settings.json")
			data, err := json.Marshal(File{RecognitionDirectAssignmentSimilarity: value})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(configPath); err == nil {
				t.Fatalf("Load() accepted direct assignment threshold %v", value)
			}
		})
	}
}

func formatFloat(value float32) string {
	return fmt.Sprintf("%.2f", value)
}

func TestLoadResolvesConfiguredRootAndRelativePaths(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "settings.json")
	settings := File{
		RuntimeDataRoot: "runtime",
		DatabasePath:    "db/catalog.sqlite3",
		ModelRoot:       "/models/insightface",
		PythonPath:      "venv/bin/python-custom",
		Provider:        "CoreMLExecutionProvider",
		ServeAddress:    "0.0.0.0:9090",
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
	if got.PythonPath != filepath.Join(wantRoot, "venv/bin/python-custom") {
		t.Errorf("PythonPath = %q", got.PythonPath)
	}
	if got.ServeAddress != "0.0.0.0:9090" {
		t.Errorf("ServeAddress = %q", got.ServeAddress)
	}
	if got.Provider != "CoreMLExecutionProvider" {
		t.Errorf("Provider = %q", got.Provider)
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

func TestLoadAcceptsJSONCConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "settings.jsonc")
	content := []byte(`{
  // comments and trailing commas are valid JSONC
  "runtime_data_root": "runtime",
  "database_path": "catalog.sqlite3",
}`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.RuntimeDataRoot != filepath.Join(filepath.Dir(configPath), "runtime") {
		t.Fatalf("RuntimeDataRoot = %q", got.RuntimeDataRoot)
	}
	if got.DatabasePath != filepath.Join(got.RuntimeDataRoot, "catalog.sqlite3") {
		t.Fatalf("DatabasePath = %q", got.DatabasePath)
	}
}

func TestLoadUsesEnvironmentConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "environment.json")
	if err := os.WriteFile(configPath, []byte(`{"runtime_data_root":"runtime"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnvironment, configPath)
	got, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.ConfigFile != configPath {
		t.Fatalf("ConfigFile = %q, want %q", got.ConfigFile, configPath)
	}
}

func TestLoadRejectsConflictingCLIAndEnvironmentConfiguration(t *testing.T) {
	dir := t.TempDir()
	cliPath := filepath.Join(dir, "cli.json")
	environmentPath := filepath.Join(dir, "environment.json")
	for _, path := range []string{cliPath, environmentPath} {
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(configEnvironment, environmentPath)
	if _, err := Load(cliPath); err == nil {
		t.Fatal("expected conflicting configuration paths to fail")
	}
}

func TestLoadAllowsSameCLIAndEnvironmentConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "same.json")
	if err := os.WriteFile(configPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configEnvironment, configPath)
	if _, err := Load(configPath); err != nil {
		t.Fatalf("Load() error = %v", err)
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
