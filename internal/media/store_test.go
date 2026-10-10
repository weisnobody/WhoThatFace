package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjg/face_me_bro/internal/db"
)

func TestPersistScanDeduplicatesAssetsAndRetainsPaths(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "media.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	collectionID, err := store.CreateCollection(context.Background(), "Photos", "/photos")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writePNG(t, filepath.Join(root, "photo.png"))
	records, err := (Scanner{}).Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	records = append(records, FileRecord{
		RelativePath: "copies/photo.png", Status: StatusSupported, MediaKind: MediaStill,
		ContentSHA256: records[0].ContentSHA256, ByteSize: records[0].ByteSize, Width: 2, Height: 3,
	})
	if err := store.PersistScan(context.Background(), collectionID, records); err != nil {
		t.Fatal(err)
	}
	var scanRuns, scanItems int
	if err := database.QueryRow(`SELECT COUNT(*) FROM scan_runs WHERE collection_id = ? AND status = 'completed'`, collectionID).Scan(&scanRuns); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM scan_items`).Scan(&scanItems); err != nil {
		t.Fatal(err)
	}
	if scanRuns != 1 || scanItems != 2 {
		t.Fatalf("scan runs/items=%d/%d, want 1/2", scanRuns, scanItems)
	}
	var assets, paths int
	if err := database.QueryRow(`SELECT COUNT(*) FROM photo_assets`).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM photo_paths`).Scan(&paths); err != nil {
		t.Fatal(err)
	}
	if assets != 1 || paths != 2 {
		t.Errorf("assets=%d paths=%d, want 1 and 2", assets, paths)
	}
	if err := os.Remove(filepath.Join(root, "photo.png")); err != nil {
		t.Fatal(err)
	}
	records, err = (Scanner{}).Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistScan(context.Background(), collectionID, records); err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := database.QueryRow(`SELECT COUNT(*) FROM photo_paths WHERE missing_at IS NOT NULL`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 2 {
		t.Errorf("missing=%d, want 2", missing)
	}
}
