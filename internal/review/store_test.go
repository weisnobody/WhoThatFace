package review

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pjg/face_me_bro/internal/db"
)

func TestAssignmentTransitionsProtectUserStateAndRejectRestoresIt(t *testing.T) {
	database, faceID, personID := fixture(t)
	defer database.Close()
	store := NewStore(database)
	actorSystem := Actor{Type: "system"}
	actorUser := Actor{Type: "user", AccountID: "account-1"}
	if err := store.InitializeFace(context.Background(), faceID, actorSystem); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assign(context.Background(), faceID, personID, Provisional, actorSystem, Evidence{}, "candidate"); err != nil {
		t.Fatal(err)
	}
	assigned, err := store.Assign(context.Background(), faceID, personID, AssignedUser, actorUser, Evidence{}, "confirmed")
	if err != nil {
		t.Fatal(err)
	}
	if assigned.State != AssignedUser {
		t.Fatalf("assigned = %+v", assigned)
	}
	if _, err := store.Assign(context.Background(), faceID, "", Unknown, actorSystem, Evidence{}, "automatic"); err == nil {
		t.Fatal("expected automatic reassignment to be rejected")
	}
	rejected, err := store.Reject(context.Background(), faceID, actorUser, "mistake")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.State != Provisional || rejected.PersonID != personID {
		t.Fatalf("rejected = %+v", rejected)
	}
}

func TestReferenceAndDispositionAreIndependent(t *testing.T) {
	database, faceID, personID := fixture(t)
	defer database.Close()
	store := NewStore(database)
	actor := Actor{Type: "user", AccountID: "account-1"}
	if err := store.InitializeFace(context.Background(), faceID, Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assign(context.Background(), faceID, personID, AssignedUser, actor, Evidence{}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetReference(context.Background(), faceID, personID, true, actor, "selected"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDisposition(context.Background(), faceID, "dont-track", true, actor, "not important"); err != nil {
		t.Fatal(err)
	}
	var reference, disposition string
	if err := database.QueryRow(`SELECT reference FROM current_face_references WHERE face_id = ?`, faceID).Scan(&reference); err != nil {
		t.Fatal(err)
	}
	if reference != "1" {
		t.Errorf("reference = %q", reference)
	}
	if err := database.QueryRow(`SELECT disposition FROM current_face_dispositions WHERE face_id = ?`, faceID).Scan(&disposition); err != nil {
		t.Fatal(err)
	}
	if disposition != "dont-track" {
		t.Errorf("disposition = %q", disposition)
	}
}

func TestReferenceRequiresEligibleAssignment(t *testing.T) {
	database, faceID, personID := fixture(t)
	defer database.Close()
	if err := NewStore(database).SetReference(context.Background(), faceID, personID, true, Actor{Type: "user", AccountID: "account-1"}, "invalid"); err == nil {
		t.Fatal("expected unassigned face to be rejected as a reference")
	}
}

func fixture(t *testing.T) (database *sql.DB, faceID, personID string) {
	t.Helper()
	dbHandle, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "review.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	faceID = "face-test"
	personID = "person-test"
	_, err = dbHandle.Exec(`INSERT INTO people (id, name, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, personID, "Person")
	if err != nil {
		dbHandle.Close()
		t.Fatal(err)
	}
	_, err = dbHandle.Exec(`INSERT INTO accounts (id, username, password_hash, role, created_at, updated_at) VALUES ('account-1', 'reviewer', 'hash', 'reviewer', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		dbHandle.Close()
		t.Fatal(err)
	}
	_, err = dbHandle.Exec(`INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES ('pipeline-test', 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'cpu', '{}', CURRENT_TIMESTAMP)`)
	if err != nil {
		dbHandle.Close()
		t.Fatal(err)
	}
	_, err = dbHandle.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES ('asset-test', 'hash-test', 1, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		dbHandle.Close()
		t.Fatal(err)
	}
	_, err = dbHandle.Exec(`INSERT INTO analyses (id, asset_id, pipeline_id, status, created_at) VALUES ('analysis-test', 'asset-test', 'pipeline-test', 'completed', CURRENT_TIMESTAMP)`)
	if err != nil {
		dbHandle.Close()
		t.Fatal(err)
	}
	_, err = dbHandle.Exec(`INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, created_at) VALUES (?, 'analysis-test', 0, 0, 0, 1, 1, CURRENT_TIMESTAMP)`, faceID)
	if err != nil {
		dbHandle.Close()
		t.Fatal(err)
	}
	return dbHandle, faceID, personID
}
