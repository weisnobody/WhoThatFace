package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/weisnobody/whothatface/internal/db"
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

func TestPossibleCandidateJobsCoalesceIdenticalPendingRequests(t *testing.T) {
	database, err := db.Open(context.Background(), t.TempDir()+"/candidate-jobs.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	payload := map[string]any{"scope": "all", "reference_set_version": "1:x"}
	first, queued, err := store.EnqueuePossibleCandidates(context.Background(), payload)
	if err != nil || !queued {
		t.Fatalf("first enqueue = %v, %t", err, queued)
	}
	second, queued, err := store.EnqueuePossibleCandidates(context.Background(), payload)
	if err != nil || !queued || second.ID != first.ID {
		t.Fatalf("second enqueue = %v, %t, want %s", err, queued, first.ID)
	}
}

func TestReferenceReprocessRequiresApprovedReferenceAndHonorsDelay(t *testing.T) {
	database, err := db.Open(context.Background(), t.TempDir()+"/reference-reprocess.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	if job, queued, err := store.EnqueueReferenceReprocess(context.Background(), "person-without-references"); err != nil {
		t.Fatal(err)
	} else if !queued || job.ID == "" {
		t.Fatal("reference reprocess was not queued without an approved reference")
	}
	job, err := store.enqueueAt(context.Background(), "reference-reprocess", 1, map[string]string{"person_id": "person"}, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("delayed job was claimed early: %s", claimed.ID)
	}
	if job.AvailableAt == nil || !job.AvailableAt.After(time.Now().UTC()) {
		t.Fatalf("available_at = %v, want future time", job.AvailableAt)
	}
	loaded, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AvailableAt == nil || !loaded.AvailableAt.After(time.Now().UTC()) {
		t.Fatalf("available_at = %v, want future time", loaded.AvailableAt)
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

func TestPauseResumeAndRetry(t *testing.T) {
	database, err := db.Open(context.Background(), t.TempDir()+"/job-controls.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	job, err := store.Enqueue(context.Background(), "test", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Pause(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Resume(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(context.Background(), job.ID, Failed, "test", "failed"); err != nil {
		t.Fatal(err)
	}
	if err := store.Retry(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), job.ID)
	if err != nil || got.Status != Pending {
		t.Fatalf("got %+v, error=%v", got, err)
	}
}
