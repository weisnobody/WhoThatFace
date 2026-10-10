package jobs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pjg/face_me_bro/internal/db"
)

func TestRunnerRunsAndCompletesHandler(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "runner.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	job, err := store.Enqueue(context.Background(), "test", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	runner := Runner{Store: store, Handlers: map[string]Handler{"test": func(_ context.Context, received Job) error { called = received.ID == job.ID; return nil }}}
	worked, err := runner.RunOnce(context.Background())
	if err != nil || !worked || !called {
		t.Fatalf("worked=%v called=%v error=%v", worked, called, err)
	}
	finished, err := store.Get(context.Background(), job.ID)
	if err != nil || finished.Status != Completed {
		t.Fatalf("finished=%+v error=%v", finished, err)
	}
}
