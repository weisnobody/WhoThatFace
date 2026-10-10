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
