// Package auth implements local accounts, sessions, and permissions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
)

const sessionDuration = 7 * 24 * time.Hour

type Role string

const (
	SystemAdmin       Role = "system-admin"
	CollectionManager Role = "collection-manager"
	Reviewer          Role = "reviewer"
	Viewer            Role = "viewer"
)

type Account struct {
	ID                 string
	Username           string
	Role               Role
	Disabled           bool
	MustChangePassword bool
}

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

func (s Store) CreateAccount(ctx context.Context, username, password string, role Role, mustChange bool) (Account, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return Account{}, err
	}
	accountID, err := id.NewUUIDv7()
	if err != nil {
		return Account{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `INSERT INTO accounts (id, username, password_hash, role, must_change_password, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, accountID, username, hash, role, boolInt(mustChange), now, now)
	if err != nil {
		return Account{}, fmt.Errorf("create account: %w", err)
	}
	return s.account(ctx, accountID)
}

func (s Store) EnsureBootstrapAdmin(ctx context.Context) (Account, error) {
	var accountID string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM accounts WHERE username = 'admin'").Scan(&accountID)
	if err == nil {
		return s.account(ctx, accountID)
	}
	if err != sql.ErrNoRows {
		return Account{}, err
	}
	hash, err := hashPassword("admin", false)
	if err != nil {
		return Account{}, err
	}
	adminID, err := id.NewUUIDv7()
	if err != nil {
		return Account{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO accounts (id, username, password_hash, role, must_change_password, created_at, updated_at) VALUES (?, 'admin', ?, ?, 1, ?, ?)`, adminID, hash, SystemAdmin, now, now); err != nil {
		return Account{}, err
	}
	return s.account(ctx, adminID)
}

func (s Store) ResetPassword(ctx context.Context, accountID, password string, mustChange bool) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE accounts SET password_hash = ?, must_change_password = ?, failed_login_count = 0, failed_login_window_started_at = NULL, updated_at = ? WHERE id = ?`, hash, boolInt(mustChange), time.Now().UTC().Format(time.RFC3339Nano), accountID)
	return err
}

func (s Store) SetDisabled(ctx context.Context, accountID string, disabled bool) error {
	var value any
	if disabled {
		value = time.Now().UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE accounts SET disabled_at = ?, updated_at = ? WHERE id = ?`, value, time.Now().UTC().Format(time.RFC3339Nano), accountID)
	return err
}

// Authenticate updates lockout state transactionally. Ten failures inside a
// rolling thirty-minute window disable the account.
func (s Store) Authenticate(ctx context.Context, username, password string) (Account, error) {
	var accountID, hash, role string
	var disabledAt, windowStarted sql.NullString
	var failed int
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash, role, disabled_at, failed_login_count, failed_login_window_started_at FROM accounts WHERE username = ?`, username).Scan(&accountID, &hash, &role, &disabledAt, &failed, &windowStarted)
	if err != nil {
		return Account{}, fmt.Errorf("authenticate: %w", err)
	}
	if disabledAt.Valid {
		return Account{}, fmt.Errorf("account is disabled")
	}
	valid, err := VerifyPassword(password, hash)
	if err != nil {
		return Account{}, err
	}
	now := time.Now().UTC()
	if !valid {
		if !windowStarted.Valid || parsedAge(windowStarted.String, now) >= 30*time.Minute {
			failed = 0
		}
		failed++
		if failed >= 10 {
			_, err = s.db.ExecContext(ctx, `UPDATE accounts SET failed_login_count = ?, failed_login_window_started_at = ?, disabled_at = ?, updated_at = ? WHERE id = ?`, failed, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), accountID)
		} else {
			_, err = s.db.ExecContext(ctx, `UPDATE accounts SET failed_login_count = ?, failed_login_window_started_at = ?, updated_at = ? WHERE id = ?`, failed, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), accountID)
		}
		return Account{}, fmt.Errorf("invalid credentials")
	}
	_, err = s.db.ExecContext(ctx, `UPDATE accounts SET failed_login_count = 0, failed_login_window_started_at = NULL, updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), accountID)
	if err != nil {
		return Account{}, err
	}
	return s.account(ctx, accountID)
}

func (s Store) CreateSession(ctx context.Context, accountID string) (string, time.Time, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", time.Time{}, err
	}
	tokenValue := base64.RawURLEncoding.EncodeToString(token[:])
	hash := sha256.Sum256(token[:])
	sessionID, err := id.NewUUIDv7()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(sessionDuration)
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (id, account_id, token_hash, expires_at, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?)`, sessionID, accountID, hash[:], expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return tokenValue, expires, err
}

func (s Store) ValidateSession(ctx context.Context, token string) (Account, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Account{}, fmt.Errorf("invalid session token")
	}
	hash := sha256.Sum256(decoded)
	var accountID string
	var expires string
	var disabled sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT s.account_id, s.expires_at, a.disabled_at FROM sessions s JOIN accounts a ON a.id = s.account_id WHERE s.token_hash = ? AND s.logged_out_at IS NULL`, hash[:]).Scan(&accountID, &expires, &disabled)
	if err != nil || disabled.Valid {
		return Account{}, fmt.Errorf("invalid session")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !time.Now().UTC().Before(expiresAt) {
		return Account{}, fmt.Errorf("session expired")
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, time.Now().UTC().Format(time.RFC3339Nano), hash[:])
	return s.account(ctx, accountID)
}

func (s Store) Logout(ctx context.Context, token string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil
	}
	hash := sha256.Sum256(decoded)
	_, err = s.db.ExecContext(ctx, `UPDATE sessions SET logged_out_at = ? WHERE token_hash = ?`, time.Now().UTC().Format(time.RFC3339Nano), hash[:])
	return err
}

func (s Store) CanAccessCollection(ctx context.Context, accountID, collectionID string, modify, undo bool) (bool, error) {
	var role string
	if err := s.db.QueryRowContext(ctx, `SELECT role FROM accounts WHERE id = ?`, accountID).Scan(&role); err != nil {
		return false, err
	}
	if role == string(SystemAdmin) {
		return true, nil
	}
	var view, canModify, canUndo int
	err := s.db.QueryRowContext(ctx, `SELECT can_view, can_modify, can_undo FROM collection_permissions WHERE account_id = ? AND collection_id = ?`, accountID, collectionID).Scan(&view, &canModify, &canUndo)
	if err != nil {
		return false, nil
	}
	if undo {
		return canUndo == 1, nil
	}
	if modify {
		return canModify == 1, nil
	}
	return view == 1, nil
}

func (s Store) account(ctx context.Context, accountID string) (Account, error) {
	var account Account
	var disabled, mustChange int
	err := s.db.QueryRowContext(ctx, `SELECT id, username, role, disabled_at IS NOT NULL, must_change_password FROM accounts WHERE id = ?`, accountID).Scan(&account.ID, &account.Username, &account.Role, &disabled, &mustChange)
	account.Disabled, account.MustChangePassword = disabled == 1, mustChange == 1
	return account, err
}

func parsedAge(value string, now time.Time) time.Duration {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 31 * time.Minute
	}
	return now.Sub(parsed)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
