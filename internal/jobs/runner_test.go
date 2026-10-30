package jobs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/weisnobody/whothatface/internal/db"
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
	if len(finished.Summary) == 0 {
		t.Fatal("completed job has no summary")
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

func TestRunnerSummaryCountsItemsWithErrors(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "summary-errors.sqlite3"))
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
		"test": func(ctx context.Context, received Job) error {
			if err := store.EnsureItems(ctx, received.ID, []string{"ok", "error"}); err != nil {
				return err
			}
			if claimed, err := store.ClaimItem(ctx, received.ID, "ok"); err != nil || !claimed {
				return err
			}
			if err := store.CompleteItem(ctx, received.ID, "ok"); err != nil {
				return err
			}
			if claimed, err := store.ClaimItem(ctx, received.ID, "error"); err != nil || !claimed {
				return err
			}
			return store.FailItem(ctx, received.ID, "error", "analysis_failed", "image too large", false)
		},
	}}
	if worked, err := runner.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("worked=%v error=%v", worked, err)
	}
	finished, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Items map[string]int `json:"items"`
	}
	if err := json.Unmarshal(finished.Summary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Items["total"] != 2 || summary.Items["completed"] != 1 || summary.Items["errors"] != 1 {
		t.Fatalf("summary items = %#v", summary.Items)
	}
}
