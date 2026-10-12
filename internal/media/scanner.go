// Package media contains filesystem scanning and media metadata services.
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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
	StatusFailed      FileStatus = "failed"
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
	EXIFCaptureDate      time.Time
	EXIFMetadataJSON     string
	Width                int
	Height               int
	Error                string
	MotionMetadataJSON   string
}

// Scanner recursively scans one collection root.
type Scanner struct {
	// StabilityDelay allows callers to avoid files currently being copied. A
	// zero duration is useful for tests and otherwise means no delay.
	StabilityDelay time.Duration
	// Control is polled between filesystem entries. It may return running,
	// paused, or cancelled for a persisted scan run.
	Control func() (string, error)
	// OnRecord is called after each filesystem entry is inspected. It allows
	// callers to persist scan progress incrementally.
	OnRecord func(FileRecord) error
}

// Scan returns records sorted by relative path. Hidden files are included;
// callers can apply collection-specific policy after scanning.
func (s Scanner) Scan(root string) ([]FileRecord, error) {
	return s.ScanContext(context.Background(), root)
}

func (s Scanner) ScanContext(ctx context.Context, root string) ([]FileRecord, error) {
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
		if err := s.waitForControl(ctx); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		record, err := s.inspect(root, path, entry)
		if err != nil {
			relative, relativeErr := filepath.Rel(root, path)
			if relativeErr != nil {
				return err
			}
			record = FileRecord{RelativePath: filepath.ToSlash(relative), Status: StatusFailed, Error: err.Error()}
		}
		records = append(records, record)
		if s.OnRecord != nil {
			if err := s.OnRecord(record); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %q: %w", root, err)
	}
	return records, nil
}

func (s Scanner) waitForControl(ctx context.Context) error {
	if s.Control == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	for {
		status, err := s.Control()
		if err != nil {
			return err
		}
		if status == "cancelled" {
			return context.Canceled
		}
		if status != "paused" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
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
	if companion, kind := motionCompanion(path); companion != "" {
		record.MediaKind = kind
		metadata, _ := json.Marshal(map[string]string{"companion_path": filepath.Base(companion), "still_source": "paired-reference-still", "extraction_method": "jpeg-reference-segment-v2"})
		record.MotionMetadataJSON = string(metadata)
	} else if kind, metadata := embeddedMotionMetadata(path); kind != MediaStill {
		record.MediaKind, record.MotionMetadataJSON = kind, metadata
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
	after, err := os.Stat(path)
	if err != nil {
		return FileRecord{}, fmt.Errorf("re-stat %q after hashing: %w", path, err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		record.Status = StatusUnstable
		record.Error = "file changed while hashing; scan will retry"
		return record, nil
	}
	if record.MediaKind != MediaStill {
		still, stillErr := referenceStillBytes(path)
		if stillErr != nil {
			record.Status = StatusCorrupt
			record.Error = "motion-photo reference still extraction failed: " + stillErr.Error()
			return record, nil
		}
		if dimensions, decodeErr := decodeDimensionsBytes(still); decodeErr == nil {
			record.Width, record.Height = dimensions.width, dimensions.height
		} else {
			record.Status = StatusCorrupt
			record.Error = "motion-photo reference still decode failed: " + decodeErr.Error()
			return record, nil
		}
	}
	if exifDate, exifErr := extractEXIFCaptureDate(path); exifErr == nil {
		record.EXIFCaptureDate = exifDate
	}
	metadata, _ := json.Marshal(map[string]any{
		"format":       strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		"capture_date": formatTime(record.EXIFCaptureDate),
		"capture_date_source": func() string {
			if record.EXIFCaptureDate.IsZero() {
				return ""
			}
			return "DateTimeOriginal"
		}(),
		"orientation_applied": true,
	})
	record.EXIFMetadataJSON = string(metadata)
	if dimensions, decodeErr := decodeDimensions(path); decodeErr == nil {
		record.Width = dimensions.width
		record.Height = dimensions.height
	} else {
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
	if err == nil {
		return struct{ width, height int }{config.Width, config.Height}, nil
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		return dimensions, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tif", ".tiff":
		return decodeTIFFDimensions(data)
	case ".webp":
		return decodeWebPDimensions(data)
	case ".heic", ".heif":
		return decodeHEIFDimensions(data)
	default:
		return dimensions, err
	}
}

func decodeTIFFDimensions(data []byte) (dimensions struct{ width, height int }, err error) {
	if len(data) < 8 {
		return dimensions, fmt.Errorf("short TIFF header")
	}
	order := binary.ByteOrder(binary.BigEndian)
	if bytes.Equal(data[:2], []byte("II")) {
		order = binary.LittleEndian
	} else if !bytes.Equal(data[:2], []byte("MM")) {
		return dimensions, fmt.Errorf("invalid TIFF byte order")
	}
	if order.Uint16(data[2:4]) != 42 {
		return dimensions, fmt.Errorf("invalid TIFF marker")
	}
	ifd := int(order.Uint32(data[4:8]))
	if ifd < 0 || ifd+2 > len(data) {
		return dimensions, fmt.Errorf("invalid TIFF IFD offset")
	}
	count := int(order.Uint16(data[ifd : ifd+2]))
	for index := 0; index < count; index++ {
		entry := ifd + 2 + index*12
		if entry+12 > len(data) {
			return dimensions, fmt.Errorf("truncated TIFF IFD")
		}
		value := tiffSingleValue(data, order, entry)
		switch order.Uint16(data[entry : entry+2]) {
		case 0x0100:
			dimensions.width = value
		case 0x0101:
			dimensions.height = value
		}
	}
	if dimensions.width <= 0 || dimensions.height <= 0 {
		return struct{ width, height int }{}, fmt.Errorf("TIFF dimensions not found")
	}
	return dimensions, nil
}

func tiffSingleValue(data []byte, order binary.ByteOrder, entry int) int {
	typ := order.Uint16(data[entry+2 : entry+4])
	count := order.Uint32(data[entry+4 : entry+8])
	if count != 1 {
		return 0
	}
	valueSize := map[uint16]int{1: 1, 3: 2, 4: 4}[typ]
	if valueSize == 0 {
		return 0
	}
	valueOffset := entry + 8
	if valueSize > 4 {
		return 0
	}
	if valueSize == 4 {
		return int(order.Uint32(data[valueOffset : valueOffset+4]))
	}
	if valueSize == 2 {
		return int(order.Uint16(data[valueOffset : valueOffset+2]))
	}
	return int(data[valueOffset])
}

func decodeWebPDimensions(data []byte) (dimensions struct{ width, height int }, err error) {
	if len(data) < 30 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || string(data[12:16]) != "VP8X" {
		return dimensions, fmt.Errorf("unsupported WebP frame")
	}
	dimensions.width = 1 + int(data[24]) | int(data[25])<<8 | int(data[26])<<16
	dimensions.height = 1 + int(data[27]) | int(data[28])<<8 | int(data[29])<<16
	return dimensions, nil
}

func decodeHEIFDimensions(data []byte) (dimensions struct{ width, height int }, err error) {
	for index := 4; index+16 <= len(data); index++ {
		if string(data[index:index+4]) != "ispe" {
			continue
		}
		dimensions.width = int(binary.BigEndian.Uint32(data[index+8 : index+12]))
		dimensions.height = int(binary.BigEndian.Uint32(data[index+12 : index+16]))
		if dimensions.width > 0 && dimensions.height > 0 {
			return dimensions, nil
		}
	}
	return dimensions, fmt.Errorf("HEIF dimensions not found")
}

func decodeDimensionsBytes(data []byte) (dimensions struct{ width, height int }, err error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return dimensions, err
	}
	return struct{ width, height int }{config.Width, config.Height}, nil
}

// referenceStillBytes extracts the JPEG reference image before any appended
// motion container bytes. Pillow/InsightFace can decode the same segment, but
// doing this during scanning validates the reference still and records a
// deterministic extraction boundary without creating a second asset/path.
func referenceStillBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return nil, fmt.Errorf("reference still is not a JPEG")
	}
	for index := 2; index+1 < len(data); index++ {
		if data[index] == 0xff && data[index+1] == 0xd9 {
			return data[:index+2], nil
		}
	}
	return nil, fmt.Errorf("JPEG end marker not found")
}

func selectCaptureDate(record FileRecord) CaptureDate {
	if !record.EXIFCaptureDate.IsZero() {
		return CaptureDate{Value: record.EXIFCaptureDate, Source: "exif"}
	}
	if !record.FilesystemCreatedAt.IsZero() {
		return CaptureDate{Value: record.FilesystemCreatedAt, Source: "filesystem-created"}
	}
	if !record.FilesystemModifiedAt.IsZero() {
		return CaptureDate{Value: record.FilesystemModifiedAt, Source: "filesystem-modified"}
	}
	return CaptureDate{Source: "none"}
}

// extractEXIFCaptureDate reads the common JPEG EXIF DateTimeOriginal tag.
// The scanner intentionally treats unsupported EXIF layouts as absent and
// falls back to filesystem timestamps. Full metadata retention belongs to the
// asset metadata extractor, while this value is needed for date search.
func extractEXIFCaptureDate(path string) (time.Time, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".tif" || ext == ".tiff" {
		return parseEXIFDateTags(data, 0x9003, 0x0132)
	}
	if ext == ".webp" {
		if exif := webPChunk(data, "EXIF"); exif != nil {
			return parseEXIFDateTags(exif, 0x9003, 0x0132)
		}
		return time.Time{}, fmt.Errorf("WebP EXIF date not found")
	}
	if ext != ".jpg" && ext != ".jpeg" {
		return time.Time{}, fmt.Errorf("unsupported metadata format")
	}
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return time.Time{}, fmt.Errorf("not a JPEG stream")
	}
	for offset := 2; offset+4 <= len(data); {
		if data[offset] != 0xff {
			return time.Time{}, fmt.Errorf("invalid JPEG marker")
		}
		marker := data[offset+1]
		offset += 2
		if marker == 0xda || marker == 0xd9 {
			break
		}
		if marker == 0xd8 || marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			continue
		}
		if offset+2 > len(data) {
			break
		}
		segmentLength := int(binary.BigEndian.Uint16(data[offset : offset+2]))
		if segmentLength < 2 || offset+segmentLength > len(data) {
			break
		}
		segment := data[offset+2 : offset+segmentLength]
		if marker == 0xe1 && bytes.HasPrefix(segment, []byte("Exif\x00\x00")) {
			return parseEXIFDateTags(segment[6:], 0x9003, 0x0132)
		}
		offset += segmentLength
	}
	return time.Time{}, fmt.Errorf("EXIF DateTimeOriginal not found")
}

func parseEXIFDate(data []byte) (time.Time, error) {
	return parseEXIFDateTags(data, 0x9003)
}

func parseEXIFDateTags(data []byte, wantedTags ...uint16) (time.Time, error) {
	if len(data) < 8 {
		return time.Time{}, fmt.Errorf("short TIFF header")
	}
	var order binary.ByteOrder = binary.BigEndian
	if bytes.Equal(data[:2], []byte("II")) {
		order = binary.LittleEndian
	} else if !bytes.Equal(data[:2], []byte("MM")) {
		return time.Time{}, fmt.Errorf("invalid TIFF byte order")
	}
	if order.Uint16(data[2:4]) != 42 {
		return time.Time{}, fmt.Errorf("invalid TIFF marker")
	}
	ifdOffset := int(order.Uint32(data[4:8]))
	if ifdOffset < 0 || ifdOffset+2 > len(data) {
		return time.Time{}, fmt.Errorf("invalid EXIF IFD offset")
	}
	count := int(order.Uint16(data[ifdOffset : ifdOffset+2]))
	for index := 0; index < count; index++ {
		entry := ifdOffset + 2 + index*12
		if entry+12 > len(data) {
			break
		}
		tag := order.Uint16(data[entry : entry+2])
		typ := order.Uint16(data[entry+2 : entry+4])
		units := order.Uint32(data[entry+4 : entry+8])
		wanted := false
		for _, wantedTag := range wantedTags {
			if tag == wantedTag {
				wanted = true
				break
			}
		}
		if !wanted || typ != 2 || units == 0 {
			continue
		}
		length := int(units)
		valueOffset := entry + 8
		if length > 4 {
			valueOffset = int(order.Uint32(data[entry+8 : entry+12]))
		}
		if valueOffset < 0 || valueOffset+length > len(data) {
			return time.Time{}, fmt.Errorf("invalid EXIF date value")
		}
		value := strings.TrimSpace(strings.TrimRight(string(data[valueOffset:valueOffset+length]), "\x00"))
		if len(value) < 19 {
			return time.Time{}, fmt.Errorf("invalid EXIF date %q", value)
		}
		return time.ParseInLocation("2006:01:02 15:04:05", value[:19], time.Local)
	}
	return time.Time{}, fmt.Errorf("DateTimeOriginal not found")
}

func webPChunk(data []byte, wanted string) []byte {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil
	}
	for offset := 12; offset+8 <= len(data); {
		kind := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		if size < 0 || start > len(data) || size > len(data)-start {
			return nil
		}
		if kind == wanted {
			return data[start : start+size]
		}
		offset = start + size
		if offset%2 != 0 {
			offset++
		}
	}
	return nil
}

func isSupported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".heic", ".heif", ".tif", ".tiff", ".webp":
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

func motionCompanion(path string) (string, MediaKind) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".jpg" && ext != ".jpeg" {
		return "", MediaStill
	}
	base := strings.TrimSuffix(path, filepath.Ext(path))
	for _, candidate := range []struct {
		ext  string
		kind MediaKind
	}{{".mov", MediaLivePhoto}, {".mp4", MediaMotionPhoto}} {
		companion := base + candidate.ext
		if info, err := os.Stat(companion); err == nil && !info.IsDir() && validMotionContainer(companion) {
			return companion, candidate.kind
		}
	}
	return "", MediaStill
}

func validMotionContainer(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	data := make([]byte, 64)
	count, err := io.ReadFull(file, data)
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	return bytes.Contains(data[:count], []byte("ftyp"))
}

func embeddedMotionMetadata(path string) (MediaKind, string) {
	if strings.ToLower(filepath.Ext(path)) != ".jpg" && strings.ToLower(filepath.Ext(path)) != ".jpeg" {
		return MediaStill, ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return MediaStill, ""
	}
	still, err := referenceStillBytes(path)
	if err != nil || len(still) == len(data) || len(data) < len(still)+8 {
		return MediaStill, ""
	}
	trailing := data[len(still):]
	if bytes.Contains(trailing, []byte("ftyp")) {
		metadata, _ := json.Marshal(map[string]string{"still_source": "embedded-reference-still", "extraction_method": "jpeg-reference-segment-v2"})
		return MediaMotionPhoto, string(metadata)
	}
	return MediaStill, ""
}
