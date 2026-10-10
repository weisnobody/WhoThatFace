// Package audit records and purges administrative and review actions.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
)

type Event struct {
	AccountID  string
	Action     string
	EntityType string
	EntityID   string
	Previous   any
	New        any
	Conflict   bool
}

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

func (s Store) Record(ctx context.Context, event Event) error {
	eventID, err := id.NewUUIDv7()
	if err != nil {
		return err
	}
	previous, err := marshalOptional(event.Previous)
	if err != nil {
		return err
	}
	current, err := marshalOptional(event.New)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_events (id, account_id, action, entity_type, entity_id, previous_json, new_json, conflict, created_at) VALUES (?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?)`, eventID, event.AccountID, event.Action, event.EntityType, event.EntityID, previous, current, boolInt(event.Conflict), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s Store) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM audit_events WHERE created_at < ?`, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("purge audit events: %w", err)
	}
	return result.RowsAffected()
}

func marshalOptional(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	return data, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
