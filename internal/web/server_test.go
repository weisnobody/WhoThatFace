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
	server, err := NewServer(authStore)
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
