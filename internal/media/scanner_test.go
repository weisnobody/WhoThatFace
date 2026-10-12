package media

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestScanContextPreservesRecordsBeforeCancellation(t *testing.T) {
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "a.png"))
	writePNG(t, filepath.Join(root, "b.png"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var persisted int
	_, err := (Scanner{OnRecord: func(FileRecord) error {
		persisted++
		cancel()
		return nil
	}}).ScanContext(ctx, root)
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if persisted != 1 {
		t.Fatalf("persisted records = %d, want 1", persisted)
	}
}

func TestScanControlPausesUntilResumed(t *testing.T) {
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "photo.png"))
	paused := make(chan struct{})
	resume := make(chan struct{})
	var calls int
	goControl := func() (string, error) {
		calls++
		if calls == 1 {
			close(paused)
			return "paused", nil
		}
		select {
		case <-resume:
			return "running", nil
		default:
			return "paused", nil
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := (Scanner{Control: goControl}).Scan(root)
		done <- err
	}()
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("scanner did not enter paused state")
	}
	select {
	case err := <-done:
		t.Fatalf("scan completed while paused: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scanner did not resume")
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

func TestScanDetectsPairedLivePhotoAndIgnoresSymlink(t *testing.T) {
	root := t.TempDir()
	photo := filepath.Join(root, "IMG_0001.JPG")
	writeJPEG(t, photo)
	if err := os.WriteFile(filepath.Join(root, "IMG_0001.MOV"), []byte{0, 0, 0, 16, 'f', 't', 'y', 'p', 'q', 't', ' ', ' ', 0, 0, 0, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(photo, filepath.Join(root, "link.JPG")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	records, err := (Scanner{}).Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records=%d, want 2", len(records))
	}
	var found bool
	for _, record := range records {
		if record.RelativePath == "IMG_0001.JPG" {
			found = true
			if record.MediaKind != MediaLivePhoto || record.MotionMetadataJSON == "" || record.Status != StatusSupported {
				t.Fatalf("record=%+v", record)
			}
		}
		if record.RelativePath == "link.JPG" {
			t.Fatalf("symlink was scanned: %+v", record)
		}
	}
	if !found {
		t.Fatal("paired still was not found")
	}
}

func TestScanDecodesMetadataFormatDimensions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "image.tiff"), syntheticTIFF(7, 9), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.webp"), syntheticWebP(11, 13), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "image.heic"), syntheticHEIF(17, 19), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := (Scanner{}).Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Status != StatusSupported || record.Width <= 0 || record.Height <= 0 {
			t.Errorf("record = %+v", record)
		}
	}
}

func TestParseEXIFDateFallsBackAcrossSupportedTags(t *testing.T) {
	data := make([]byte, 8+2+12+20)
	copy(data[:2], []byte("II"))
	binary.LittleEndian.PutUint16(data[2:4], 42)
	binary.LittleEndian.PutUint32(data[4:8], 8)
	binary.LittleEndian.PutUint16(data[8:10], 1)
	binary.LittleEndian.PutUint16(data[10:12], 0x0132)
	binary.LittleEndian.PutUint16(data[12:14], 2)
	binary.LittleEndian.PutUint32(data[14:18], 20)
	binary.LittleEndian.PutUint32(data[18:22], 22)
	copy(data[22:], []byte("2026:10:11 12:13:14\x00"))
	value, err := parseEXIFDateTags(data, 0x9003, 0x0132)
	if err != nil {
		t.Fatal(err)
	}
	if value.Year() != 2026 || value.Month() != 10 || value.Day() != 11 {
		t.Fatalf("date = %v", value)
	}
}

func TestScanIgnoresUnrecognizedMotionCompanion(t *testing.T) {
	root := t.TempDir()
	photo := filepath.Join(root, "photo.jpg")
	writeJPEG(t, photo)
	if err := os.WriteFile(filepath.Join(root, "photo.mov"), []byte("not a movie"), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := (Scanner{}).Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].MediaKind != MediaStill {
		t.Fatalf("media kind = %q, want still", records[0].MediaKind)
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

func writeJPEG(t *testing.T, path string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	if err := jpeg.Encode(file, img, nil); err != nil {
		t.Fatal(err)
	}
}

func syntheticTIFF(width, height uint32) []byte {
	data := make([]byte, 8+2+24+4)
	copy(data, []byte("II"))
	binary.LittleEndian.PutUint16(data[2:4], 42)
	data[4] = 8
	binary.LittleEndian.PutUint16(data[8:10], 2)
	binary.LittleEndian.PutUint16(data[10:12], 0x0100)
	binary.LittleEndian.PutUint16(data[12:14], 4)
	binary.LittleEndian.PutUint32(data[14:18], 1)
	binary.LittleEndian.PutUint32(data[18:22], width)
	binary.LittleEndian.PutUint16(data[22:24], 0x0101)
	binary.LittleEndian.PutUint16(data[24:26], 4)
	binary.LittleEndian.PutUint32(data[26:30], 1)
	binary.LittleEndian.PutUint32(data[30:34], height)
	return data
}

func syntheticWebP(width, height int) []byte {
	data := make([]byte, 30)
	copy(data[:4], []byte("RIFF"))
	copy(data[8:12], []byte("WEBP"))
	copy(data[12:16], []byte("VP8X"))
	data[24] = byte(width - 1)
	data[25] = byte((width - 1) >> 8)
	data[26] = byte((width - 1) >> 16)
	data[27] = byte(height - 1)
	data[28] = byte((height - 1) >> 8)
	data[29] = byte((height - 1) >> 16)
	return data
}

func syntheticHEIF(width, height uint32) []byte {
	data := make([]byte, 24)
	copy(data[4:8], []byte("ispe"))
	data[8] = 0
	data[12] = byte(width >> 24)
	data[13] = byte(width >> 16)
	data[14] = byte(width >> 8)
	data[15] = byte(width)
	data[16] = byte(height >> 24)
	data[17] = byte(height >> 16)
	data[18] = byte(height >> 8)
	data[19] = byte(height)
	return data
}
