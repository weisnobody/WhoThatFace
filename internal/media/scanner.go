// Package media contains filesystem scanning and media metadata services.
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileStatus describes whether a discovered path can be analyzed.
type FileStatus string

const (
	StatusSupported   FileStatus = "supported"
	StatusUnsupported FileStatus = "unsupported"
	StatusCorrupt     FileStatus = "corrupt"
	StatusUnstable    FileStatus = "unstable"
)

// MediaKind identifies the source asset type known to the scanner.
type MediaKind string

const (
	MediaStill       MediaKind = "still"
	MediaLivePhoto   MediaKind = "live_photo"
	MediaMotionPhoto MediaKind = "motion_photo"
)

// CaptureDate records the selected fallback date and its provenance.
type CaptureDate struct {
	Value  time.Time
	Source string
}

// FileRecord is a stable scan result for one path. Multiple records may have
// the same ContentSHA256 and are intentionally retained as separate paths.
type FileRecord struct {
	RelativePath         string
	AbsolutePath         string
	Status               FileStatus
	MediaKind            MediaKind
	ContentSHA256        string
	ByteSize             int64
	FilesystemCreatedAt  time.Time
	FilesystemModifiedAt time.Time
	CaptureDate          CaptureDate
	Width                int
	Height               int
	Error                string
}

// Scanner recursively scans one collection root.
type Scanner struct {
	// StabilityDelay allows callers to avoid files currently being copied. A
	// zero duration is useful for tests and otherwise means no delay.
	StabilityDelay time.Duration
}

// Scan returns records sorted by relative path. Hidden files are included;
// callers can apply collection-specific policy after scanning.
func (s Scanner) Scan(root string) ([]FileRecord, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve scan root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat scan root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan root is not a directory: %s", root)
	}

	var records []FileRecord
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		record, err := s.inspect(root, path, entry)
		if err != nil {
			return err
		}
		records = append(records, record)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %q: %w", root, err)
	}
	return records, nil
}

func (s Scanner) inspect(root, path string, entry fs.DirEntry) (FileRecord, error) {
	info, err := entry.Info()
	if err != nil {
		return FileRecord{}, fmt.Errorf("stat %q: %w", path, err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return FileRecord{}, fmt.Errorf("relative path for %q: %w", path, err)
	}
	record := FileRecord{
		RelativePath:         filepath.ToSlash(relative),
		Status:               StatusUnsupported,
		MediaKind:            mediaKind(path),
		ByteSize:             info.Size(),
		FilesystemModifiedAt: info.ModTime(),
		FilesystemCreatedAt:  filesystemCreatedAt(info),
	}
	if !isSupported(path) {
		return record, nil
	}
	record.Status = StatusSupported
	if s.StabilityDelay > 0 {
		time.Sleep(s.StabilityDelay)
	}
	before, err := os.Stat(path)
	if err != nil {
		return FileRecord{}, fmt.Errorf("re-stat %q: %w", path, err)
	}
	if before.Size() != info.Size() || !before.ModTime().Equal(info.ModTime()) {
		record.Status = StatusUnstable
		record.Error = "file changed while scanning"
		return record, nil
	}
	hash, err := hashFile(path)
	if err != nil {
		return FileRecord{}, err
	}
	record.ContentSHA256 = hash
	if dimensions, decodeErr := decodeDimensions(path); decodeErr == nil {
		record.Width = dimensions.width
		record.Height = dimensions.height
	} else if !isMetadataOnlyFormat(path) {
		record.Status = StatusCorrupt
		record.Error = decodeErr.Error()
	}
	record.CaptureDate = selectCaptureDate(record)
	return record, nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %q for hashing: %w", path, err)
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash %q: %w", path, err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func decodeDimensions(path string) (dimensions struct{ width, height int }, err error) {
	file, err := os.Open(path)
	if err != nil {
		return dimensions, err
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return dimensions, err
	}
	return struct{ width, height int }{config.Width, config.Height}, nil
}

func selectCaptureDate(record FileRecord) CaptureDate {
	// EXIF parsing is delegated to the metadata extractor; the scanner's
	// filesystem fallback is deterministic and available on every platform.
	if !record.FilesystemCreatedAt.IsZero() {
		return CaptureDate{Value: record.FilesystemCreatedAt, Source: "filesystem-created"}
	}
	if !record.FilesystemModifiedAt.IsZero() {
		return CaptureDate{Value: record.FilesystemModifiedAt, Source: "filesystem-modified"}
	}
	return CaptureDate{Source: "none"}
}

func isSupported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".heic", ".heif", ".tif", ".tiff", ".webp":
		return true
	default:
		return false
	}
}

func isMetadataOnlyFormat(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".heic", ".heif", ".webp", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func mediaKind(path string) MediaKind {
	// Live/motion metadata is detected by the metadata extractor in the full
	// pipeline. Ordinary stills remain the safe default at scan time.
	return MediaStill
}
