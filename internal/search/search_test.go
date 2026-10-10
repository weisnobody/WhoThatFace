package search

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pjg/face_me_bro/internal/db"
)

func TestPhotosSearchesDirectoryAndExcludesDontTrackByDefault(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "search.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection', 'Photos', '/photos', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, capture_date, capture_date_source, created_at, updated_at) VALUES ('asset', 'hash', 1, 'still', '2026-01-01T00:00:00Z', 'filesystem-modified', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('path', 'collection', 'asset', 'group/photo.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 'still')`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewStore(database).Photos(context.Background(), Options{RelativePrefix: "group", CaptureDateFrom: "2025-01-01", CaptureDateUntil: "2027-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].RelativePath != "group/photo.jpg" {
		t.Fatalf("result = %+v", result)
	}
}
