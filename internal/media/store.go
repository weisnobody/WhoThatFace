package media

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
)

// Store persists scan results behind a small media-oriented repository API.
type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

func (s Store) CreateCollection(ctx context.Context, name, root string) (string, error) {
	resolved, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve collection root: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT root_path FROM collections WHERE enabled = 1`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return "", err
		}
		if rootsOverlap(resolved, existing) {
			return "", fmt.Errorf("collection root %q overlaps existing collection root %q", resolved, existing)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	collectionID, err := id.NewUUIDv7()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO collections (id, name, root_path, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`, collectionID, name, resolved, now, now)
	if err != nil {
		return "", fmt.Errorf("create collection: %w", err)
	}
	return collectionID, nil
}

func rootsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if left == right {
		return true
	}
	relative, err := filepath.Rel(left, right)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return true
	}
	relative, err = filepath.Rel(right, left)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (s Store) DeleteCollection(ctx context.Context, collectionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM collection_permissions WHERE collection_id = ?`, collectionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM scan_items WHERE scan_run_id IN (SELECT id FROM scan_runs WHERE collection_id = ?)`, collectionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM scan_runs WHERE collection_id = ?`, collectionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM photo_paths WHERE collection_id = ?`, collectionID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM collections WHERE id = ?`, collectionID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("collection %q does not exist", collectionID)
	}
	return tx.Commit()
}

func (s Store) StartScan(ctx context.Context, collectionID string) (string, error) {
	runID, err := id.NewUUIDv7()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `INSERT INTO scan_runs (id, collection_id, status, started_at, created_at) VALUES (?, ?, 'running', ?, ?)`, runID, collectionID, now, now)
	return runID, err
}

func (s Store) FinishScan(ctx context.Context, runID, status, message string) error {
	if status != "completed" && status != "completed-with-errors" && status != "failed" && status != "cancelled" {
		return fmt.Errorf("invalid scan status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE scan_runs SET status = ?, completed_at = ?, error_message = NULLIF(?, '') WHERE id = ?`, status, time.Now().UTC().Format(time.RFC3339Nano), message, runID)
	return err
}

func (s Store) SetScanControl(ctx context.Context, runID, action string) error {
	var status string
	switch action {
	case "pause":
		status = "paused"
	case "resume":
		status = "running"
	case "cancel":
		status = "cancelled"
	case "retry":
		status = "running"
	default:
		return fmt.Errorf("invalid scan control action %q", action)
	}
	allowed := "('queued', 'running', 'paused')"
	if action == "retry" {
		allowed = "('failed', 'cancelled', 'completed-with-errors')"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE scan_runs SET status = ?, started_at = COALESCE(started_at, ?), completed_at = NULL, error_message = NULL WHERE id = ? AND status IN `+allowed, status, time.Now().UTC().Format(time.RFC3339Nano), runID)
	return err
}

// PersistScan updates assets and paths in one transaction. The input may
// contain duplicate content hashes; they become one asset with many paths.
func (s Store) PersistScan(ctx context.Context, collectionID string, records []FileRecord) error {
	runID, err := s.StartScan(ctx, collectionID)
	if err != nil {
		return err
	}
	if err := s.PersistScanRun(ctx, collectionID, runID, records); err != nil {
		_ = s.FinishScan(ctx, runID, "failed", err.Error())
		return err
	}
	return s.FinishScan(ctx, runID, "completed", "")
}

func (s Store) PersistScanRun(ctx context.Context, collectionID, runID string, records []FileRecord) error {
	for _, record := range records {
		if err := s.PersistScanRecord(ctx, collectionID, runID, record); err != nil {
			return err
		}
	}
	seen := make([]string, 0, len(records))
	for _, record := range records {
		seen = append(seen, record.RelativePath)
	}
	return s.FinalizeScan(ctx, collectionID, runID, seen)
}

// PersistScanRecord durably records one scan item and its supported path. It
// is safe to call repeatedly for the same run/path during resume.
func (s Store) PersistScanRecord(ctx context.Context, collectionID, runID string, record FileRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	itemID, err := id.NewUUIDv7()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scan_items (id, scan_run_id, relative_path, status, content_sha256, error_message, updated_at) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?) ON CONFLICT(scan_run_id, relative_path) DO UPDATE SET status = excluded.status, content_sha256 = excluded.content_sha256, error_message = excluded.error_message, updated_at = excluded.updated_at`, itemID, runID, record.RelativePath, record.Status, record.ContentSHA256, record.Error, now); err != nil {
		return err
	}
	if record.Status == StatusSupported && record.ContentSHA256 != "" {
		assetID, err := ensureAsset(ctx, tx, record, now)
		if err != nil {
			return err
		}
		pathID, err := id.NewUUIDv7()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, filesystem_created_at, filesystem_modified_at, first_seen_at, last_seen_at, missing_at, file_size, media_kind, motion_metadata_json) VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULL, ?, ?, NULLIF(?, '')) ON CONFLICT(collection_id, relative_path) DO UPDATE SET asset_id = excluded.asset_id, filesystem_created_at = excluded.filesystem_created_at, filesystem_modified_at = excluded.filesystem_modified_at, last_seen_at = excluded.last_seen_at, missing_at = NULL, file_size = excluded.file_size, media_kind = excluded.media_kind, motion_metadata_json = excluded.motion_metadata_json`, pathID, collectionID, assetID, record.RelativePath, formatTime(record.FilesystemCreatedAt), formatTime(record.FilesystemModifiedAt), now, now, record.ByteSize, record.MediaKind, record.MotionMetadataJSON); err != nil {
			return fmt.Errorf("persist path %q: %w", record.RelativePath, err)
		}
	}
	return tx.Commit()
}

// FinalizeScan marks paths not seen by the completed enumeration as missing.
func (s Store) FinalizeScan(ctx context.Context, collectionID, runID string, seen []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `SELECT relative_path FROM photo_paths WHERE collection_id = ? AND missing_at IS NULL`, collectionID)
	if err != nil {
		return err
	}
	seenSet := make(map[string]struct{}, len(seen))
	for _, path := range seen {
		seenSet[path] = struct{}{}
	}
	var existing []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, path)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, path := range existing {
		if _, ok := seenSet[path]; !ok {
			if _, err := tx.ExecContext(ctx, `UPDATE photo_paths SET missing_at = ? WHERE collection_id = ? AND relative_path = ?`, now, collectionID, path); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE collections SET updated_at = ? WHERE id = ?`, now, collectionID); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureAsset(ctx context.Context, tx *sql.Tx, record FileRecord, now string) (string, error) {
	var assetID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM photo_assets WHERE content_sha256 = ?`, record.ContentSHA256).Scan(&assetID)
	if err == nil {
		return assetID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	assetID, err = id.NewUUIDv7()
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO photo_assets (
		id, content_sha256, byte_size, media_kind, width, height,
		exif_capture_date, capture_date, capture_date_source,
			exif_metadata_json, motion_metadata_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, NULLIF(?, 0), NULLIF(?, 0), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?)`,
		assetID, record.ContentSHA256, record.ByteSize, record.MediaKind,
		record.Width, record.Height, formatTime(record.EXIFCaptureDate), formatTime(record.CaptureDate.Value), record.CaptureDate.Source, record.EXIFMetadataJSON, record.MotionMetadataJSON, now, now)
	if err != nil {
		return "", fmt.Errorf("persist asset %q: %w", record.ContentSHA256, err)
	}
	return assetID, nil
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
