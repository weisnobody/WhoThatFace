package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjg/face_me_bro/internal/auth"
	"github.com/pjg/face_me_bro/internal/db"
	"github.com/pjg/face_me_bro/internal/review"
)

func TestLoginRequiresCSRFAndCreatesSession(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "web.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authStore := auth.NewStore(database)
	if _, err := authStore.CreateAccount(context.Background(), "alice", "Password1", auth.Reviewer, false); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(authStore, database)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	loginRequest := httptest.NewRequest(http.MethodGet, "/login", nil)
	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(loginPage, loginRequest)
	csrf := loginPage.Result().Cookies()[0].Value
	form := url.Values{"csrf": {csrf}, "username": {"alice"}, "password": {"Password1"}}
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(loginPage.Result().Cookies()[0])
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestReviewPeopleWorksForRestrictedCollectionAccount(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "web.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authStore := auth.NewStore(database)
	account, err := authStore.CreateAccount(ctx, "reviewer", "Password1", auth.Reviewer, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection-1', 'Photos', '/photos', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := authStore.SetCollectionPermission(ctx, account.ID, "collection-1", true, true, true); err != nil {
		t.Fatal(err)
	}
	token, _, err := authStore.CreateSession(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(authStore, database)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/review/people", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "People") {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func TestViewerCannotUseMutatingAPI(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "web.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authStore := auth.NewStore(database)
	account, err := authStore.CreateAccount(ctx, "viewer", "Password1", auth.Viewer, false)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := authStore.CreateSession(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(authStore, database)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/people", strings.NewReader(`{"name":"blocked"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	request.AddCookie(&http.Cookie{Name: "csrf", Value: "csrf-token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestEveryMutatingRouteRequiresSession(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "web.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	server, err := NewServer(auth.NewStore(database), database)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/faces/face/assignment",
		"/api/faces/face/disposition",
		"/api/faces/face/reference",
		"/api/people",
		"/api/people/person",
		"/api/clusters/cluster/review",
		"/api/audit/event/undo",
		"/api/admin/accounts",
		"/api/admin/accounts/account/password",
		"/api/admin/accounts/account/disabled",
		"/api/admin/permissions",
	} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/login" {
			t.Errorf("%s: status=%d location=%q", path, response.Code, response.Header().Get("Location"))
		}
	}
}

func TestPhotoServingRejectsSymlinkEscape(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := filepath.Join(dir, "photos")
	outside := filepath.Join(dir, "outside.jpg")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.jpg")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	database, err := db.Open(ctx, filepath.Join(dir, "web.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authStore := auth.NewStore(database)
	account, err := authStore.CreateAccount(ctx, "viewer", "Password1", auth.Viewer, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection-1', 'Photos', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, root); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES ('asset-1', 'hash', 7, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('path-1', 'collection-1', 'asset-1', 'escape.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 7, 'still')`); err != nil {
		t.Fatal(err)
	}
	if err := authStore.SetCollectionPermission(ctx, account.ID, "collection-1", true, false, false); err != nil {
		t.Fatal(err)
	}
	token, _, err := authStore.CreateSession(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(authStore, database)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/photos/path-1", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestPhotoPermissionsRespectRevocationAndDuplicateAssetCollections(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rootOne := filepath.Join(dir, "one")
	rootTwo := filepath.Join(dir, "two")
	if err := os.MkdirAll(rootOne, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootTwo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootOne, "photo.jpg"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootTwo, "photo.jpg"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(ctx, filepath.Join(dir, "web.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authStore := auth.NewStore(database)
	account, err := authStore.CreateAccount(ctx, "reviewer", "Password1", auth.Reviewer, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, collection := range []struct{ id, root string }{{"collection-1", rootOne}, {"collection-2", rootTwo}} {
		if _, err := database.ExecContext(ctx, `INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, collection.id, collection.id, collection.root); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES ('asset-1', 'same-content', 3, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for index, collectionID := range []string{"collection-1", "collection-2"} {
		if _, err := database.ExecContext(ctx, `INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES (?, ?, 'asset-1', 'photo.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 3, 'still')`, "path-"+string(rune('1'+index)), collectionID); err != nil {
			t.Fatal(err)
		}
	}
	if err := authStore.SetCollectionPermission(ctx, account.ID, "collection-1", true, false, false); err != nil {
		t.Fatal(err)
	}
	token, _, err := authStore.CreateSession(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(authStore, database)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/photos/path-1", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("permitted duplicate asset status = %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/photos/path-2", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unpermitted duplicate asset status = %d", response.Code)
	}
	if err := authStore.SetCollectionPermission(ctx, account.ID, "collection-1", false, false, false); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/photos/path-1", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("revoked permission status = %d", response.Code)
	}
}

func TestSupersededClusterReviewAPIIsReadOnly(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	root := filepath.Join(dir, "photos")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(ctx, filepath.Join(dir, "web.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authStore := auth.NewStore(database)
	account, err := authStore.CreateAccount(ctx, "reviewer", "Password1", auth.Reviewer, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection-1', 'Photos', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, width, height, created_at, updated_at) VALUES ('asset-1', 'hash', 1, 'still', 10, 10, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('path-1', 'collection-1', 'asset-1', 'photo.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 'still');
		INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES ('pipeline-1', 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'cpu', '{}', CURRENT_TIMESTAMP);
		INSERT INTO analyses (id, asset_id, pipeline_id, status, source_width, source_height, inference_width, inference_height, created_at) VALUES ('analysis-1', 'asset-1', 'pipeline-1', 'completed', 10, 10, 10, 10, CURRENT_TIMESTAMP);
		INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, detection_confidence, quality, created_at) VALUES ('face-1', 'analysis-1', 0, 1, 1, 5, 5, .9, .9, CURRENT_TIMESTAMP);
		INSERT INTO clustering_runs (id, algorithm, algorithm_version, k, similarity_threshold, neighbor_search_method, mutual_edge_policy, candidate_scope, input_face_set_json, status, created_at) VALUES ('run-old', 'test', '1', 1, .45, 'exact', 'mutual', 'all', '[]', 'superseded', CURRENT_TIMESTAMP);
		INSERT INTO clusters (id, clustering_run_id, cluster_key, purity, connectivity, is_mixed, review_status, created_at) VALUES ('cluster-old', 'run-old', 'face', 1, 1, 0, 'unreviewed', CURRENT_TIMESTAMP);
		INSERT INTO cluster_memberships (cluster_id, face_id) VALUES ('cluster-old', 'face-1')`, root); err != nil {
		t.Fatal(err)
	}
	if err := authStore.SetCollectionPermission(ctx, account.ID, "collection-1", true, true, true); err != nil {
		t.Fatal(err)
	}
	token, _, err := authStore.CreateSession(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(authStore, database)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/clusters/cluster-old/review", strings.NewReader(`{"status":"reviewed","expected_version":0}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", "csrf-token")
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	request.AddCookie(&http.Cookie{Name: "csrf", Value: "csrf-token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestReviewPersonRendersPhotosFacesAndActions(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "web.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authStore := auth.NewStore(database)
	account, err := authStore.CreateAccount(ctx, "reviewer", "Password1", auth.Reviewer, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection-1', 'Photos', '/photos', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, width, height, created_at, updated_at) VALUES ('asset-1', 'hash-1', 10, 'still', 1000, 800, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('path-1', 'collection-1', 'asset-1', 'person/photo.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 10, 'still');
		INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES ('pipeline-1', 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'cpu', '{}', CURRENT_TIMESTAMP);
		INSERT INTO analyses (id, asset_id, pipeline_id, status, source_width, source_height, created_at) VALUES ('analysis-1', 'asset-1', 'pipeline-1', 'completed', 1000, 800, CURRENT_TIMESTAMP);
		INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, detection_confidence, quality, created_at) VALUES ('face-1', 'analysis-1', 0, 100, 80, 300, 320, 0.99, 0.90, CURRENT_TIMESTAMP);
		INSERT INTO people (id, name, created_at, updated_at) VALUES ('person-1', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO people (id, name, created_at, updated_at) VALUES ('person-2', 'Second Person', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO recognition_candidates (id, face_id, person_id, rank, similarity, margin, support_count, quality, detection_confidence, evidence, created_at) VALUES ('candidate-1', 'face-1', 'person-1', 1, 0.91, 0.12, 4, 0.90, 0.99, 'high', CURRENT_TIMESTAMP);
		INSERT INTO recognition_candidates (id, face_id, person_id, rank, similarity, margin, support_count, quality, detection_confidence, evidence, created_at) VALUES ('candidate-2', 'face-1', 'person-2', 2, 0.79, 0.03, 2, 0.90, 0.99, 'medium', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := authStore.SetCollectionPermission(ctx, account.ID, "collection-1", true, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := review.NewStore(database).Assign(ctx, "face-1", "person-1", review.AssignedUser, review.Actor{Type: "user", AccountID: account.ID}, review.Evidence{}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := review.NewStore(database).SetReference(ctx, "face-1", "person-1", true, review.Actor{Type: "user", AccountID: account.ID}, "test"); err != nil {
		t.Fatal(err)
	}
	token, _, err := authStore.CreateSession(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(authStore, database)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/review/people/person-1", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	request.AddCookie(&http.Cookie{Name: "csrf", Value: "csrf-token"})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if policy := response.Header().Get("Content-Security-Policy"); !strings.Contains(policy, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP does not allow the page styles: %s", policy)
	}
	for _, value := range []string{"person-1", "/api/photos/path-1", "class=\"box reference\"", "class=\"card\"", "class=\"grid\"", "User:", "90.0%", "Unknown", "Ambiguous", "Evaluation", "Key Photo", "Save", "/static/person.js", "Similarity Max:", "All non-assigned", "id=\"page-assignment-state\"", `value="person-1" selected`} {
		if !strings.Contains(response.Body.String(), value) {
			t.Errorf("person page is missing %q: %s", value, response.Body.String())
		}
	}
	if !strings.Contains(response.Body.String(), "Max: 91.0%, Min: 91.0%") {
		t.Fatalf("person confidence summary was not derived from the candidate: %s", response.Body.String())
	}
	headEnd := strings.Index(response.Body.String(), "</head>")
	gridStyle := strings.Index(response.Body.String(), ".grid{")
	if headEnd < 0 || gridStyle < 0 || gridStyle > headEnd {
		t.Fatalf("person photo styles were not rendered in the document head: %s", response.Body.String())
	}
	renameRequest := httptest.NewRequest(http.MethodPost, "/api/people/person-1", strings.NewReader(`{"name":"Renamed Person"}`))
	renameRequest.Header.Set("Content-Type", "application/json")
	renameRequest.Header.Set("X-CSRF-Token", "csrf-token")
	renameRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	renameRequest.AddCookie(&http.Cookie{Name: "csrf", Value: "csrf-token"})
	renameResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(renameResponse, renameRequest)
	if renameResponse.Code != http.StatusOK {
		t.Fatalf("rename status = %d, body = %s", renameResponse.Code, renameResponse.Body.String())
	}
	var renamed string
	if err := database.QueryRowContext(ctx, `SELECT name FROM people WHERE id = 'person-1'`).Scan(&renamed); err != nil || renamed != "Renamed Person" {
		t.Fatalf("renamed person = %q, err = %v", renamed, err)
	}
	var renameAuditCount int
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action = 'person-rename' AND entity_id = 'person-1'`).Scan(&renameAuditCount); err != nil || renameAuditCount != 1 {
		t.Fatalf("rename audit count = %d, err = %v", renameAuditCount, err)
	}
	recommendRequest := httptest.NewRequest(http.MethodPost, "/api/people/person-1/reference-reprocess", nil)
	recommendRequest.Header.Set("X-CSRF-Token", "csrf-token")
	recommendRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	recommendRequest.AddCookie(&http.Cookie{Name: "csrf", Value: "csrf-token"})
	recommendResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(recommendResponse, recommendRequest)
	if recommendResponse.Code != http.StatusAccepted {
		t.Fatalf("recommendation status = %d, body = %s", recommendResponse.Code, recommendResponse.Body.String())
	}
	var recommendationKind, recommendationPayload string
	if err := database.QueryRowContext(ctx, `SELECT kind, payload_json FROM jobs WHERE kind = 'reference-reprocess' ORDER BY created_at DESC LIMIT 1`).Scan(&recommendationKind, &recommendationPayload); err != nil || recommendationKind != "reference-reprocess" || !strings.Contains(recommendationPayload, "person-1") {
		t.Fatalf("recommendation job = %q %q, err = %v", recommendationKind, recommendationPayload, err)
	}
	photoRequest := httptest.NewRequest(http.MethodGet, "/review/photos?path_id=path-1", nil)
	photoRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	photoRequest.AddCookie(&http.Cookie{Name: "csrf", Value: "csrf-token"})
	photoResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(photoResponse, photoRequest)
	if photoResponse.Code != http.StatusOK {
		t.Fatalf("photo review status = %d, body = %s", photoResponse.Code, photoResponse.Body.String())
	}
	for _, value := range []string{"Face 1", "photo-box", "data-action=\"unknown\"", "data-action=\"ambiguous\"", "data-action=\"assign\"", "data-action=\"role\"", "/static/photo.js"} {
		if !strings.Contains(photoResponse.Body.String(), value) {
			t.Errorf("photo review is missing %q: %s", value, photoResponse.Body.String())
		}
	}
}
