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
	"github.com/pjg/face_me_bro/internal/jobs"
	"github.com/pjg/face_me_bro/internal/review"
)

func TestPossibleFaceForPersonOnlyIncludesActionableAssignments(t *testing.T) {
	candidates := []possibleCandidateView{{PersonID: "person-1"}}
	for _, testCase := range []struct {
		name                          string
		state, assignedPerson, person string
		want                          bool
	}{
		{name: "assigned system to person", state: string(review.AssignedSystem), assignedPerson: "person-1", person: "person-1", want: false},
		{name: "assigned user to person", state: string(review.AssignedUser), assignedPerson: "person-1", person: "person-1", want: false},
		{name: "unknown candidate", state: string(review.Unknown), assignedPerson: "", person: "person-1", want: true},
		{name: "provisional candidate for another person", state: string(review.Provisional), assignedPerson: "person-2", person: "person-1", want: true},
		{name: "provisional candidate for same person", state: string(review.Provisional), assignedPerson: "person-1", person: "person-1", want: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := possibleFaceForPerson(testCase.state, testCase.assignedPerson, testCase.person, candidates); got != testCase.want {
				t.Fatalf("possibleFaceForPerson() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestCandidateRenderersFormatAssignedPerson(t *testing.T) {
	recognition := renderRecognitionCandidates([]recognitionCandidateView{
		{PersonID: "person-1", Name: "One", Similarity: 0.91},
		{PersonID: "person-2", Name: "Two", Similarity: 0.79},
	}, "face-1", "person-1", false)
	for _, want := range []string{
		`<strong>Candidates:</strong><div class="candidate-list"><strong><a href="/review/people/person-1">One</a></strong>`,
		`<br><a href="/review/people/person-2">Two</a>`,
	} {
		if !strings.Contains(recognition, want) {
			t.Fatalf("candidate rendering missing %q: %s", want, recognition)
		}
	}

	possible := renderPossibleMatches([]possibleCandidateView{{PersonID: "person-1", PersonName: "One"}}, "face-1", "person-1", false)
	if !strings.Contains(possible, `<strong><a href="/review/people/person-1">One</a></strong>`) {
		t.Fatalf("possible-match rendering did not emphasize the assigned person: %s", possible)
	}
}

func TestClusterScriptHandlesCandidateAssignments(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.clusterScript(response, httptest.NewRequest(http.MethodGet, "/static/cluster.js", nil))
	body := response.Body.String()
	if !strings.Contains(body, `data-action="assign-candidate"`) || !strings.Contains(body, `/api/faces/`) || !strings.Contains(body, `candidate_id:button.dataset.candidateId`) {
		t.Fatalf("cluster script does not handle candidate assignments: %s", body)
	}
}

func TestClusterPeopleHTMLLimitsDisplayedPeople(t *testing.T) {
	body := clusterPeopleHTML(clusterListRow{People: []clusterListPerson{
		{ID: "person-1", Name: "One", Count: 3},
		{ID: "person-2", Name: "Two", Count: 2},
		{ID: "person-3", Name: "Three", Count: 1},
		{ID: "person-4", Name: "Four", Count: 1},
	}})
	if !strings.Contains(body, "One") || !strings.Contains(body, "Two") || !strings.Contains(body, "2 more") {
		t.Fatalf("cluster people summary omitted visible people or count: %s", body)
	}
	if strings.Contains(body, "Three") || strings.Contains(body, "Four") {
		t.Fatalf("cluster people summary rendered more than two people: %s", body)
	}
}

func TestClusterPersonNameFilter(t *testing.T) {
	people := []clusterListPerson{{ID: "person-1", Name: "Unnamed", Named: false}, {ID: "person-2", Name: "Named", Named: true}}
	if !clusterMatchesPersonNameFilter(people, "all") || !clusterMatchesPersonNameFilter(people, "named") || !clusterMatchesPersonNameFilter(people, "unnamed") {
		t.Fatal("mixed cluster should match all person-name filter modes")
	}
	if clusterMatchesPersonNameFilter([]clusterListPerson{{ID: "person-2", Name: "Named", Named: true}}, "unnamed") {
		t.Fatal("named-only cluster matched unnamed person filter")
	}
	if clusterMatchesPersonNameFilter([]clusterListPerson{{ID: "person-1", Name: "Unnamed", Named: false}}, "named") {
		t.Fatal("unnamed-only cluster matched named person filter")
	}
}

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
	if !strings.Contains(response.Body.String(), `<strong>Sort</strong> <select name="sort">`) || !strings.Contains(response.Body.String(), `value="references">References</option>`) || !strings.Contains(response.Body.String(), `value="clusters">Clusters</option>`) || !strings.Contains(response.Body.String(), `<th>Clusters</th><th>References</th>`) || !strings.Contains(response.Body.String(), `name="collection_id"`) || !strings.Contains(response.Body.String(), `value="collection-1">Photos</option>`) || !strings.Contains(response.Body.String(), `name="min_faces"`) || !strings.Contains(response.Body.String(), `name="max_faces"`) || !strings.Contains(response.Body.String(), `</select></label><label><strong>Direction</strong>`) || !strings.Contains(response.Body.String(), `<strong>Per Page</strong>`) || !strings.Contains(response.Body.String(), `<button>Apply</button>`) || !strings.Contains(response.Body.String(), `class="people-controls-break"`) || !strings.Contains(response.Body.String(), `#people-controls>button{margin-left:4rem}`) || !strings.Contains(response.Body.String(), `<label>Jump to Page <input type="number" name="page"`) {
		t.Fatalf("people filter form has malformed sort labels: %s", response.Body.String())
	}
	for _, value := range []string{`<select name="name">`, `value="all" selected>All</option>`, `>With name</option>`, `>Without name</option>`, `<select name="view">`, `>All</option>`, `>Possibles</option>`, `>Don't Track</option>`, `>No Faces</option>`, `<strong>Assignment:</strong>`, `name="assignment_user"`, `name="assignment_system"`, `name="assignment_provisional"`, `name="assignment_other"`} {
		if !strings.Contains(response.Body.String(), value) {
			t.Errorf("people filter form is missing %q: %s", value, response.Body.String())
		}
	}
	if strings.Index(response.Body.String(), `<hr><form id="create-person"`) < strings.Index(response.Body.String(), `<table class="people-table">`) {
		t.Fatalf("new person form should be below the people table: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `<script src="/static/people.js" defer></script>`) || strings.Contains(response.Body.String(), `<script>(function(){const form=document.getElementById('create-person')`) {
		t.Fatalf("create-person form should use an external CSP-compatible script: %s", response.Body.String())
	}
	if strings.Count(response.Body.String(), `name="assignment_user" value="true" checked`) != 1 || strings.Count(response.Body.String(), `name="assignment_system" value="true" checked`) != 1 || strings.Count(response.Body.String(), `name="assignment_provisional" value="true" checked`) != 1 || strings.Count(response.Body.String(), `name="assignment_other" value="true" checked`) != 1 {
		t.Fatalf("assignment filters are not checked by default: %s", response.Body.String())
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/people", strings.NewReader(`{"name":"Created Person"}`))
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.Header.Set("X-CSRF-Token", "csrf-token")
	createRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	createRequest.AddCookie(&http.Cookie{Name: "csrf", Value: "csrf-token"})
	createResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("create person status = %d, body = %s", createResponse.Code, createResponse.Body.String())
	}
	var createdName string
	if err := database.QueryRowContext(ctx, `SELECT name FROM people WHERE name = 'Created Person'`).Scan(&createdName); err != nil || createdName != "Created Person" {
		t.Fatalf("created person name = %q, err = %v", createdName, err)
	}
}

func TestAdminJobsShowsItemErrors(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "admin-jobs.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authStore := auth.NewStore(database)
	account, err := authStore.CreateAccount(ctx, "admin", "Password1", auth.SystemAdmin, false)
	if err != nil {
		t.Fatal(err)
	}
	jobStore := jobs.NewStore(database)
	job, err := jobStore.Enqueue(ctx, "analysis", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobStore.EnsureItems(ctx, job.ID, []string{"asset-1"}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := jobStore.ClaimItem(ctx, job.ID, "asset-1"); err != nil || !claimed {
		t.Fatalf("claim item = %v, %v", claimed, err)
	}
	if err := jobStore.FailItem(ctx, job.ID, "asset-1", "analysis_failed", "image too large", true); err != nil {
		t.Fatal(err)
	}
	if err := jobStore.SetStatus(ctx, job.ID, jobs.CompletedWithError, "", ""); err != nil {
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
	request := httptest.NewRequest(http.MethodGet, "/admin/jobs", nil)
	request.AddCookie(&http.Cookie{Name: "session", Value: token})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Show Errors") {
		t.Fatalf("jobs page did not show error button: status=%d body=%s", response.Code, response.Body.String())
	}
	detailRequest := httptest.NewRequest(http.MethodGet, "/admin/jobs?show_errors="+url.QueryEscape(job.ID), nil)
	detailRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	detailResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(detailResponse, detailRequest)
	if detailResponse.Code != http.StatusOK || !strings.Contains(detailResponse.Body.String(), "image too large") || !strings.Contains(detailResponse.Body.String(), "asset-1") {
		t.Fatalf("jobs page did not show errors: status=%d body=%s", detailResponse.Code, detailResponse.Body.String())
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
		"/api/faces/assignments",
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
		INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, width, height, created_at, updated_at) VALUES ('asset-1', 'hash-1', 10, 'still', 800, 1000, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('path-1', 'collection-1', 'asset-1', 'person/photo.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 10, 'still');
		INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('path-duplicate', 'collection-1', 'asset-1', 'z-person/photo-duplicate.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 10, 'still');
		INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES ('pipeline-1', 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'cpu', '{}', CURRENT_TIMESTAMP);
		INSERT INTO analyses (id, asset_id, pipeline_id, status, source_width, source_height, created_at) VALUES ('analysis-1', 'asset-1', 'pipeline-1', 'completed', 1000, 800, CURRENT_TIMESTAMP);
		INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, detection_confidence, quality, created_at) VALUES ('face-1', 'analysis-1', 0, 100, 80, 300, 320, 0.99, 0.90, CURRENT_TIMESTAMP);
		INSERT INTO people (id, name, created_at, updated_at) VALUES ('person-1', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO people (id, name, created_at, updated_at) VALUES ('person-2', 'Second Person', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO people (id, name, created_at, updated_at) VALUES ('person-3', 'Stale Candidate', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO recognition_candidates (id, face_id, person_id, rank, similarity, margin, support_count, quality, detection_confidence, evidence, created_at) VALUES ('candidate-1', 'face-1', 'person-1', 1, 0.91, 0.12, 4, 0.90, 0.99, 'high', CURRENT_TIMESTAMP);
		INSERT INTO recognition_candidates (id, face_id, person_id, rank, similarity, margin, support_count, quality, detection_confidence, evidence, created_at) VALUES ('candidate-2', 'face-1', 'person-2', 2, 0.79, 0.03, 2, 0.90, 0.99, 'medium', CURRENT_TIMESTAMP);
		INSERT INTO recognition_candidates (id, face_id, person_id, rank, similarity, margin, support_count, quality, detection_confidence, evidence, created_at) VALUES ('candidate-old', 'face-1', 'person-1', 1, 0.80, 0.10, 1, 0.90, 0.99, 'medium', '2020-01-01T00:00:00Z');
		INSERT INTO recognition_candidates (id, face_id, person_id, rank, similarity, margin, support_count, quality, detection_confidence, evidence, created_at) VALUES ('candidate-stale', 'face-1', 'person-3', 3, 0.70, 0.05, 1, 0.90, 0.99, 'medium', '2020-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO clustering_runs (id, algorithm, algorithm_version, k, similarity_threshold, neighbor_search_method, mutual_edge_policy, candidate_scope, input_face_set_json, status, accepted_at, created_at) VALUES ('run-active', 'test', '1', 1, .45, 'exact', 'mutual', 'all', '[]', 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO clusters (id, clustering_run_id, cluster_key, proposed_person_id, purity, connectivity, is_mixed, review_status, created_at) VALUES ('cluster-active', 'run-active', 'face-1', 'person-1', 1, 1, 0, 'unreviewed', CURRENT_TIMESTAMP);
		INSERT INTO cluster_memberships (cluster_id, face_id) VALUES ('cluster-active', 'face-1')`); err != nil {
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
	for _, testCase := range []struct {
		query string
		want  string
		avoid string
	}{
		{query: "view=no-faces", want: "person-2", avoid: "person-1"},
		{query: "assignment_user=true", want: "person-1", avoid: "person-2"},
		{query: "name=named&view=no-faces", want: "Second Person", avoid: "person-1"},
		{query: "name=unnamed&include_no_faces=true", want: "person-1", avoid: "Second Person"},
		{query: "min_faces=2", want: "People (0/0)", avoid: "person-1"},
		{query: "collection_id=collection-1", want: "person-1", avoid: "Second Person"},
	} {
		filteredRequest := httptest.NewRequest(http.MethodGet, "/review/people?"+testCase.query, nil)
		filteredRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
		filteredResponse := httptest.NewRecorder()
		server.Handler().ServeHTTP(filteredResponse, filteredRequest)
		if filteredResponse.Code != http.StatusOK || !strings.Contains(filteredResponse.Body.String(), testCase.want) || strings.Contains(filteredResponse.Body.String(), testCase.avoid) {
			t.Fatalf("people filter %q: status=%d body=%s", testCase.query, filteredResponse.Code, filteredResponse.Body.String())
		}
	}
	clusterRequest := httptest.NewRequest(http.MethodGet, "/review/clusters/cluster-active", nil)
	clusterRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	clusterResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(clusterResponse, clusterRequest)
	if clusterResponse.Code != http.StatusOK || !strings.Contains(clusterResponse.Body.String(), "Sp: 4") || strings.Contains(clusterResponse.Body.String(), "support 1") {
		t.Fatalf("cluster detail did not restrict candidates to the latest batch: status=%d body=%s", clusterResponse.Code, clusterResponse.Body.String())
	}
	if !strings.Contains(clusterResponse.Body.String(), `left:10.000%;top:10.000%;width:20.000%;height:30.000%`) {
		t.Fatalf("cluster detail did not render face boxes using analysis dimensions: %s", clusterResponse.Body.String())
	}
	if !strings.Contains(clusterResponse.Body.String(), "<dt>Candidates</dt><dd>N/A</dd>") {
		t.Fatalf("cluster detail did not render N/A for an all-assigned cluster: %s", clusterResponse.Body.String())
	}
	if !strings.Contains(clusterResponse.Body.String(), `value="person-2"`) {
		t.Fatalf("cluster detail omitted named person with no assigned faces: %s", clusterResponse.Body.String())
	}
	for _, value := range []string{"Face-Me-Bro", `class="breadcrumb">› <a href="/review/clusters">Clusters</a>`, "cluster-detail-controls", `value="to-assign">To Assign</option>`, "Proposed person", "Consistency", "Review version", "Candidate confidence", "class=\"grid cluster-grid\"", "class=\"card cluster-face assigned assigned-user\"", `value="person-1" selected`, "User:</strong> Cs:", `data-action="unknown"`, `data-action="ambiguous"`, `data-disposition="incorrect-assignment"`, `data-disposition="false-detection">No Face`, `data-disposition="dont-track">Don't Track`, "person-select-filter", "data-action=\"assign\"", "/review/photos?path_id=path-1", "/review/people/person-1"} {
		if !strings.Contains(clusterResponse.Body.String(), value) {
			t.Errorf("cluster detail missing %q: %s", value, clusterResponse.Body.String())
		}
	}
	filteredClusterRequest := httptest.NewRequest(http.MethodGet, "/review/clusters/cluster-active?assignment_state=assigned-user&min_quality=80&sort=quality&direction=desc", nil)
	filteredClusterRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	filteredClusterResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(filteredClusterResponse, filteredClusterRequest)
	if filteredClusterResponse.Code != http.StatusOK || !strings.Contains(filteredClusterResponse.Body.String(), `name="min_quality"`) || !strings.Contains(filteredClusterResponse.Body.String(), `value="80"`) || !strings.Contains(filteredClusterResponse.Body.String(), `value="desc" selected`) {
		t.Fatalf("cluster detail filters were not preserved: status=%d body=%s", filteredClusterResponse.Code, filteredClusterResponse.Body.String())
	}
	toAssignRequest := httptest.NewRequest(http.MethodGet, "/review/clusters/cluster-active?assignment_state=to-assign", nil)
	toAssignRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	toAssignResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(toAssignResponse, toAssignRequest)
	if toAssignResponse.Code != http.StatusOK || !strings.Contains(toAssignResponse.Body.String(), `value="to-assign" selected`) || strings.Contains(toAssignResponse.Body.String(), `id="face-face-1"`) {
		t.Fatalf("cluster detail To Assign filter failed: status=%d body=%s", toAssignResponse.Code, toAssignResponse.Body.String())
	}
	clusterListRequest := httptest.NewRequest(http.MethodGet, "/review/clusters", nil)
	clusterListRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	clusterListResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(clusterListResponse, clusterListRequest)
	if clusterListResponse.Code != http.StatusOK || !strings.Contains(clusterListResponse.Body.String(), `name="person_name"`) || !strings.Contains(clusterListResponse.Body.String(), ">1</td><td>N/A</td><td>0</td><td>0</td><td>1</td>") {
		t.Fatalf("cluster list did not count latest candidates only: status=%d body=%s", clusterListResponse.Code, clusterListResponse.Body.String())
	}
	bulkRequest := httptest.NewRequest(http.MethodPost, "/api/faces/assignments", strings.NewReader(`{"face_ids":["face-1"],"person_id":"person-1","state":"assigned-user"}`))
	bulkRequest.Header.Set("Content-Type", "application/json")
	bulkRequest.Header.Set("X-CSRF-Token", "csrf-token")
	bulkRequest.AddCookie(&http.Cookie{Name: "session", Value: token})
	bulkRequest.AddCookie(&http.Cookie{Name: "csrf", Value: "csrf-token"})
	bulkResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(bulkResponse, bulkRequest)
	if bulkResponse.Code != http.StatusOK || !strings.Contains(bulkResponse.Body.String(), `"assignments"`) {
		t.Fatalf("bulk assignment status=%d body=%s", bulkResponse.Code, bulkResponse.Body.String())
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
	if !strings.Contains(response.Body.String(), `value="person-2"`) {
		t.Fatalf("person detail omitted named person with no assigned faces: %s", response.Body.String())
	}
	for _, value := range []string{"person-1", `class="breadcrumb">› <a href="/review/people">People</a>`, "/api/photos/path-1", "class=\"box reference\"", "class=\"card assigned\"", "class=\"grid\"", "dark green indicates", "User:", "90.0%", "Unknown", "Ambiguous", "Evaluation", "Key Photo", "Save", "/static/person.js", `class="person-summary-table"`, "<dt>Similarity</dt><dd>91.0% - 91.0%", "<dt>Cluster Min/Max</dt>", "<dt>Faces</dt><dd>1 / 1", "<dt>Possibles</dt><dd>0 / 0", "<dt>References</dt><dd>1", "<strong>Filter:</strong>", "<strong>Sort:</strong>", "All non-assigned", `value="assigned-system">assigned-system</option>`, "id=\"page-assignment-state\"", `data-person-filter="assignment-options-face-1"`, `value="person-1" selected`, `value="collection"`, `>Collection</option>`, `value="cluster"`, `>Cluster</option>`, `value="consistency"`, `>Consistency</option>`, `value="possibles"`, `>Possibles</option>`, `value="ref_recs"`, `>Ref Recs</option>`, `value="reference-or-recommended"`, `>Ref / Rec Refs</option>`, `value="without" selected`, `>With Out</option>`, `value="only"`, `>Only</option>`, `value="all"`, `>All</option>`} {
		if !strings.Contains(response.Body.String(), value) {
			t.Errorf("person page is missing %q: %s", value, response.Body.String())
		}
	}
	if strings.Index(response.Body.String(), `data-action="representative"`) > strings.Index(response.Body.String(), `data-action="role"`) {
		t.Fatalf("person photo card buttons are in the wrong order: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "<dt>Similarity</dt><dd>91.0% - 91.0%") {
		t.Fatalf("person confidence summary was not derived from the candidate: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Candidates:") || !strings.Contains(response.Body.String(), "S: 91.0% M: 12.0% Sp: 4 Q: 90.0% D: 99.0%") {
		t.Fatalf("person page did not render the latest recognition candidates: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `left:10.000%;top:10.000%;width:20.000%;height:30.000%`) {
		t.Fatalf("person page did not render face boxes using analysis dimensions: %s", response.Body.String())
	}
	if strings.Count(response.Body.String(), `id="face-face-1"`) != 1 {
		t.Fatalf("person page rendered a face more than once for duplicate asset paths: %s", response.Body.String())
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
	for _, value := range []string{`class="breadcrumb">› <a href="/review/photos">Photos</a>`, "person/photo.jpg", "Face 1", "photo-box", "Faces", "People", "Collections", "/review/people/person-1", "/review/clusters/cluster-active", "/static/photo.js"} {
		if !strings.Contains(photoResponse.Body.String(), value) {
			t.Errorf("photo review is missing %q: %s", value, photoResponse.Body.String())
		}
	}
	for _, value := range []string{`data-action="unknown"`, `data-action="ambiguous"`, `data-action="assign"`, `data-action="role"`, `data-disposition="dont-track"`} {
		if strings.Contains(photoResponse.Body.String(), value) {
			t.Errorf("assigned photo face should not render %q: %s", value, photoResponse.Body.String())
		}
	}
	if !strings.Contains(photoResponse.Body.String(), `left:10.000%;top:10.000%;width:20.000%;height:30.000%`) {
		t.Fatalf("photo page did not render face boxes using analysis dimensions: %s", photoResponse.Body.String())
	}
	assignmentIndex := strings.Index(response.Body.String(), `class="person-assignment"`)
	articleEnd := strings.Index(response.Body.String(), `</article>`)
	if assignmentIndex < 0 || articleEnd < 0 || assignmentIndex > articleEnd {
		t.Fatalf("person-assignment is outside the first photo article")
	}
}
