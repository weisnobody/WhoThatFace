// Package audit records and purges administrative and review actions.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weisnobody/whothatface/internal/id"
)

type Event struct {
	ID            string
	TargetEventID string
	AccountID     string
	Action        string
	EntityType    string
	EntityID      string
	Previous      any
	New           any
	Conflict      bool
}

type Record struct {
	ID, TargetEventID, Action, EntityType, EntityID string
	Previous, New                                   []byte
	Conflict                                        bool
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_events (id, account_id, action, entity_type, entity_id, target_event_id, previous_json, new_json, conflict, created_at) VALUES (?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)`, eventID, event.AccountID, event.Action, event.EntityType, event.EntityID, event.TargetEventID, previous, current, boolInt(event.Conflict), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s Store) Get(ctx context.Context, eventID string) (Record, error) {
	var result Record
	var previous, current sql.NullString
	var conflict int
	err := s.db.QueryRowContext(ctx, `SELECT id, COALESCE(target_event_id, ''), action, entity_type, entity_id, previous_json, new_json, conflict FROM audit_events WHERE id = ?`, eventID).Scan(&result.ID, &result.TargetEventID, &result.Action, &result.EntityType, &result.EntityID, &previous, &current, &conflict)
	if err != nil {
		return Record{}, err
	}
	if previous.Valid {
		result.Previous = []byte(previous.String)
	}
	if current.Valid {
		result.New = []byte(current.String)
	}
	result.Conflict = conflict == 1
	return result, nil
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
