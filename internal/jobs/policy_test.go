package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/weisnobody/whothatface/internal/db"
)

func TestManualThrottlePausePreventsClaim(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "policy.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	job, err := store.Enqueue(context.Background(), "test", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetThrottleMode(context.Background(), ThrottlePaused); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Store: store, Policy: ResourcePolicy{Mode: ThrottleAutomatic}}
	worked, err := runner.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("worked=%v error=%v", worked, err)
	}
	claimed, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != Pending {
		t.Fatalf("status=%s, want pending", claimed.Status)
	}
}

func TestRunnerMakesPauseEffectiveForRunningHandler(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "pause.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	job, err := store.Enqueue(context.Background(), "test", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	runner := Runner{Store: store, Policy: ResourcePolicy{Mode: ThrottleFullSpeed, PollInterval: 10 * time.Millisecond}, Handlers: map[string]Handler{"test": func(ctx context.Context, _ Job) error { close(started); <-ctx.Done(); close(stopped); return ctx.Err() }}}
	result := make(chan error, 1)
	go func() { _, runErr := runner.RunOnce(context.Background()); result <- runErr }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	if err := store.Pause(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("handler was not cancelled")
	}
	<-result
	paused, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != Paused {
		t.Fatalf("status=%s, want paused", paused.Status)
	}
}

func TestJobItemsAreResumable(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "items.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	job, err := store.Enqueue(context.Background(), "test", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureItems(context.Background(), job.ID, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimItem(context.Background(), job.ID, "a")
	if err != nil || !claimed {
		t.Fatalf("claimed=%v error=%v", claimed, err)
	}
	if err := store.CompleteItem(context.Background(), job.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if err := store.FailItem(context.Background(), job.ID, "b", "temporary", "retryable", false); err != nil {
		t.Fatal(err)
	}
	items, err := store.Items(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Status != "completed" || items[1].Status != "pending" {
		t.Fatalf("items=%+v", items)
	}
}
