package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	for _, value := range []string{"person-1", "/api/photos/path-1", "person-box reference", "Assignment:", "assigned-user", "Face quality:", "Model candidates", "Second Person", "Remove assignment", "Make evaluation", "Save assignment", "/static/person.js"} {
		if !strings.Contains(response.Body.String(), value) {
			t.Errorf("person page is missing %q: %s", value, response.Body.String())
		}
	}
}
