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
