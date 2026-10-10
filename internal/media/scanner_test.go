package media

import (
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestScanRecursesHashesAndRetainsDuplicatePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(root, "photo.png")
	writePNG(t, imagePath)
	duplicatePath := filepath.Join(root, "nested", "copy.png")
	data, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(duplicatePath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not a photo"), 0o644); err != nil {
		t.Fatal(err)
	}

	records, err := (Scanner{}).Scan(root)
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	var supported, unsupported int
	for _, record := range records {
		switch record.Status {
		case StatusSupported:
			supported++
			if record.ContentSHA256 == "" || record.Width != 2 || record.Height != 3 {
				t.Errorf("bad supported record: %+v", record)
			}
		case StatusUnsupported:
			unsupported++
		}
	}
	if supported != 2 || unsupported != 1 {
		t.Errorf("supported=%d unsupported=%d", supported, unsupported)
	}
	if records[0].RelativePath != "nested/copy.png" {
		t.Errorf("records are not path sorted: %+v", records)
	}
	var hashes []string
	for _, record := range records {
		if record.Status == StatusSupported {
			hashes = append(hashes, record.ContentSHA256)
		}
	}
	if len(hashes) != 2 || hashes[0] != hashes[1] {
		t.Errorf("duplicate content hashes differ")
	}
}

func TestScanUsesFullFileSHA256(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "photo.png")
	writePNG(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := sha256.Sum256(data)
	records, err := (Scanner{}).Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := records[0].ContentSHA256, hex.EncodeToString(wantBytes[:]); got != want {
		t.Errorf("ContentSHA256 = %q, want %q", got, want)
	}
	if records[0].CaptureDate.Source == "none" {
		t.Error("expected filesystem capture-date fallback")
	}
}

func TestScanMarksCorruptStandardImage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "broken.jpg")
	if err := os.WriteFile(path, []byte("not jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := (Scanner{}).Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Status != StatusCorrupt {
		t.Errorf("Status = %q, want corrupt", records[0].Status)
	}
}

func writePNG(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 100), G: uint8(y * 50), A: 255})
		}
	}
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
}
