package auth

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/weisnobody/whothatface/internal/db"
)

func TestPasswordHashAndSessionLifecycle(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "auth.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	account, err := store.CreateAccount(context.Background(), "alice", "Password1", Reviewer, true)
	if err != nil {
		t.Fatal(err)
	}
	if account.MustChangePassword != true {
		t.Errorf("account = %+v", account)
	}
	if _, err := store.Authenticate(context.Background(), "alice", "wrongpass1"); err == nil {
		t.Fatal("expected bad password")
	}
	verified, err := store.Authenticate(context.Background(), "alice", "Password1")
	if err != nil || verified.ID != account.ID {
		t.Fatalf("authenticate = %+v, error=%v", verified, err)
	}
	token, _, err := store.CreateSession(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	sessionAccount, err := store.ValidateSession(context.Background(), token)
	if err != nil || sessionAccount.ID != account.ID {
		t.Fatalf("session account = %+v, error=%v", sessionAccount, err)
	}
	if err := store.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ValidateSession(context.Background(), token); err == nil {
		t.Fatal("expected logged-out session to fail")
	}
}

func TestPasswordPolicyAndLockout(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected short password rejection")
	}
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "lockout.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	account, err := store.CreateAccount(context.Background(), "bob", "Password1", Reviewer, false)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		_, _ = store.Authenticate(context.Background(), "bob", "Wrongpass1")
	}
	if _, err := store.Authenticate(context.Background(), "bob", "Password1"); err == nil {
		t.Fatal("expected disabled account")
	}
	var disabled string
	if err := database.QueryRow(`SELECT disabled_at FROM accounts WHERE id = ?`, account.ID).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled == "" {
		t.Fatal("account was not disabled")
	}
}
