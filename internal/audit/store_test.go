package audit

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pjg/face_me_bro/internal/db"
)

func TestRecordAndPurge(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "audit.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	if err := store.Record(context.Background(), Event{Action: "assign", EntityType: "face", EntityID: "face-1", New: map[string]string{"state": "assigned-user"}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	removed, err := store.PurgeBefore(context.Background(), time.Now().Add(time.Minute))
	if err != nil || removed != 1 {
		t.Fatalf("removed=%d error=%v", removed, err)
	}
}
