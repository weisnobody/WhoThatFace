package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestParsePythonVersion(t *testing.T) {
	if got := parsePythonVersion("Python 3.11.9"); got != 311 {
		t.Errorf("version = %d", got)
	}
	if parsePythonVersion("unknown") != 0 {
		t.Error("unknown version should be zero")
	}
}

func TestModelChecksumsIncludesNestedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "model.onnx"), []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "weights.bin"), []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "checksums.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	checksums, err := modelChecksums(root)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte("weights"))
	if checksums["nested/weights.bin"] != hex.EncodeToString(want[:]) {
		t.Fatalf("nested checksum = %q", checksums["nested/weights.bin"])
	}
	if _, found := checksums["checksums.json"]; found {
		t.Fatal("checksum manifest must not be included")
	}
}
