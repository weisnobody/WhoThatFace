package jobs

import (
	"context"
	"testing"

	"github.com/pjg/face_me_bro/internal/db"
)

func TestStoreEnqueueClaimAndComplete(t *testing.T) {
	database, err := db.Open(context.Background(), t.TempDir()+"/jobs.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	job, err := store.Enqueue(context.Background(), "scan", 1, map[string]string{"root": "/photos"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != job.ID || claimed.Status != Running {
		t.Fatalf("claimed = %+v", claimed)
	}
	if err := store.UpdateProgress(context.Background(), job.ID, map[string]int{"done": 2}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(context.Background(), job.ID, Completed, "", ""); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != Completed || finished.CompletedAt == nil {
		t.Fatalf("finished = %+v", finished)
	}
}

func TestClaimReturnsHighestPriorityFirst(t *testing.T) {
	database, err := db.Open(context.Background(), t.TempDir()+"/jobs.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	if _, err := store.Enqueue(context.Background(), "low", 9, nil); err != nil {
		t.Fatal(err)
	}
	high, err := store.Enqueue(context.Background(), "high", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != high.ID {
		t.Errorf("claimed %q, want %q", claimed.ID, high.ID)
	}
}
