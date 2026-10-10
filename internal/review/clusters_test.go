package review

import (
	"context"
	"testing"
)

func TestReviewClusterRequiresExplicitSingletonDecisionAndVersion(t *testing.T) {
	database, faceID, personID := fixture(t)
	defer database.Close()
	_, err := database.Exec(`
		INSERT INTO clustering_runs (id, algorithm, algorithm_version, k, similarity_threshold, neighbor_search_method, mutual_edge_policy, candidate_scope, input_face_set_json, status, created_at)
		VALUES ('run-test', 'mutual-knn', 'v1', 1, .45, 'exact', 'mutual-top-k', 'all', '[]', 'active', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
		INSERT INTO clusters (id, clustering_run_id, cluster_key, purity, connectivity, is_mixed, review_status, created_at)
		VALUES ('cluster-test', 'run-test', 'face-test', 1, 1, 0, 'unreviewed', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO cluster_memberships (cluster_id, face_id) VALUES ('cluster-test', ?)`, faceID); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database)
	if err := store.ReviewCluster(context.Background(), "cluster-test", "", "reviewed", 0, nil, Actor{Type: "user", AccountID: "account-1"}, ""); err == nil {
		t.Fatal("expected explicit singleton decision requirement")
	}
	if err := store.ReviewCluster(context.Background(), "cluster-test", personID, "reviewed", 0, nil, Actor{Type: "user", AccountID: "account-1"}, "confirmed"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReviewCluster(context.Background(), "cluster-test", personID, "reviewed", 0, nil, Actor{Type: "user", AccountID: "account-1"}, "stale"); err == nil {
		t.Fatal("expected optimistic concurrency conflict")
	}
	var state string
	if err := database.QueryRow(`SELECT state FROM current_face_assignments WHERE face_id = ?`, faceID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(AssignedUser) {
		t.Fatalf("state=%q, want assigned-user", state)
	}
}

func TestUndoAssignmentRestoresPreviousStateAndDetectsConflict(t *testing.T) {
	database, faceID, personID := fixture(t)
	defer database.Close()
	store := NewStore(database)
	ctx := context.Background()
	if err := store.InitializeFace(ctx, faceID, Actor{Type: "system"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assign(ctx, faceID, personID, Provisional, Actor{Type: "system"}, Evidence{}, "candidate"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assign(ctx, faceID, personID, AssignedUser, Actor{Type: "user", AccountID: "account-1"}, Evidence{}, "confirmed"); err != nil {
		t.Fatal(err)
	}
	var auditID string
	if err := database.QueryRow(`SELECT id FROM audit_events WHERE action = 'assignment' AND entity_id = ? ORDER BY rowid DESC LIMIT 1`, faceID).Scan(&auditID); err != nil {
		t.Fatal(err)
	}
	if err := store.UndoAuditEvent(ctx, auditID, Actor{Type: "user", AccountID: "account-1"}); err != nil {
		t.Fatal(err)
	}
	assignment, err := store.Current(ctx, faceID)
	if err != nil {
		t.Fatal(err)
	}
	if assignment.State != Provisional {
		t.Fatalf("state=%s, want provisional", assignment.State)
	}
	if _, err := store.Assign(ctx, faceID, personID, AssignedUser, Actor{Type: "user", AccountID: "account-1"}, Evidence{}, "reconfirmed"); err != nil {
		t.Fatal(err)
	}
	if err := store.UndoAuditEvent(ctx, auditID, Actor{Type: "user", AccountID: "account-1"}); err == nil {
		t.Fatal("expected stale undo conflict")
	}
	var conflicts int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action = 'undo'`).Scan(&conflicts); err != nil {
		t.Fatal(err)
	}
	if conflicts == 0 {
		t.Fatal("expected compensating audit event")
	}
}
