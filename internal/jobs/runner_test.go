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

func TestRunnerRetriesThenPersistsPermanentFailure(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "retry.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	job, err := store.Enqueue(context.Background(), "test", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Store: store, Handlers: map[string]Handler{
		"test": func(context.Context, Job) error { return context.DeadlineExceeded },
	}}
	for attempt := 1; attempt <= 3; attempt++ {
		worked, runErr := runner.RunOnce(context.Background())
		if !worked || runErr == nil {
			t.Fatalf("attempt %d worked=%v error=%v", attempt, worked, runErr)
		}
		got, getErr := store.Get(context.Background(), job.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if got.Attempts != attempt {
			t.Fatalf("attempts=%d, want %d", got.Attempts, attempt)
		}
		if attempt < 3 && got.Status != Pending {
			t.Fatalf("attempt %d status=%s, want pending", attempt, got.Status)
		}
		if attempt == 3 && (got.Status != Failed || got.CompletedAt == nil) {
			t.Fatalf("final job=%+v", got)
		}
	}
}
