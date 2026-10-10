// Package review contains review-state services shared by the CLI and web UI.
package review

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
)

type AssignmentState string

const (
	Unassigned     AssignmentState = "unassigned"
	Provisional    AssignmentState = "provisional"
	AssignedSystem AssignmentState = "assigned-system"
	AssignedUser   AssignmentState = "assigned-user"
	Unknown        AssignmentState = "unknown"
	Ambiguous      AssignmentState = "ambiguous"
)

type Actor struct {
	Type      string
	AccountID string
}

type Evidence struct {
	PipelineID      string
	ClusteringRunID string
	ClusterID       string
	Similarity      *float32
	Margin          *float32
	Support         *int
	Quality         *float32
	Detection       *float32
	Purity          *float32
	Connectivity    *float32
	Agreement       *bool
}

type Assignment struct {
	FaceID    string
	PersonID  string
	State     AssignmentState
	EventID   string
	UpdatedAt time.Time
}

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

// InitializeFace creates the initial unassigned state once analysis discovers
// a face. It is safe to call repeatedly.
func (s Store) InitializeFace(ctx context.Context, faceID string, actor Actor) error {
	var exists int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM current_face_assignments WHERE face_id = ?", faceID).Scan(&exists)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = s.setAssignment(ctx, faceID, "", Unassigned, actor, Evidence{}, "face-seen", "")
	return err
}

// Assign changes the current state. Automatic actors cannot replace a
// user-confirmed assignment.
func (s Store) Assign(ctx context.Context, faceID, personID string, state AssignmentState, actor Actor, evidence Evidence, reason string) (Assignment, error) {
	if err := validateAssignment(state, personID); err != nil {
		return Assignment{}, err
	}
	current, err := s.Current(ctx, faceID)
	if err != nil && err != sql.ErrNoRows {
		return Assignment{}, err
	}
	if err == nil && current.State == AssignedUser && actor.Type != "user" {
		return current, fmt.Errorf("user-confirmed assignment is protected")
	}
	return s.setAssignment(ctx, faceID, personID, state, actor, evidence, "assignment", reason)
}

// Reject restores the previous state recorded by the current event, or starts
// an unassigned state when there is no prior event.
func (s Store) Reject(ctx context.Context, faceID string, actor Actor, reason string) (Assignment, error) {
	current, err := s.Current(ctx, faceID)
	if err != nil {
		return Assignment{}, err
	}
	var state, personID sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT previous_state, previous_person_id
		FROM assignment_events WHERE id = ?`, current.EventID).Scan(&state, &personID)
	if err != nil {
		return Assignment{}, err
	}
	newState := Unassigned
	if state.Valid {
		newState = AssignmentState(state.String)
	}
	newPerson := ""
	if personID.Valid {
		newPerson = personID.String
	}
	return s.setAssignment(ctx, faceID, newPerson, newState, actor, Evidence{}, "reject", reason)
}

func (s Store) Current(ctx context.Context, faceID string) (Assignment, error) {
	var result Assignment
	var updated string
	err := s.db.QueryRowContext(ctx, `
		SELECT face_id, COALESCE(person_id, ''), state, event_id, updated_at
		FROM current_face_assignments WHERE face_id = ?`, faceID).Scan(&result.FaceID, &result.PersonID, &result.State, &result.EventID, &updated)
	if err != nil {
		return Assignment{}, err
	}
	result.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	return result, err
}

func (s Store) SetReference(ctx context.Context, faceID, personID string, reference bool, actor Actor, reason string) error {
	eventID, err := id.NewUUIDv7()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previousEvent sql.NullString
	_ = tx.QueryRowContext(ctx, "SELECT event_id FROM current_face_references WHERE face_id = ?", faceID).Scan(&previousEvent)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO reference_selection_events (id, face_id, person_id, reference, account_id, reason, supersedes_event_id, created_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?)`, eventID, faceID, personID, boolInt(reference), actor.AccountID, reason, previousEvent.String, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO current_face_references (face_id, person_id, reference, event_id, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(face_id) DO UPDATE SET person_id = excluded.person_id, reference = excluded.reference, event_id = excluded.event_id, updated_at = excluded.updated_at`, faceID, personID, boolInt(reference), eventID, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s Store) SetDisposition(ctx context.Context, faceID, disposition string, active bool, actor Actor, reason string) error {
	if disposition != "incorrect-assignment" && disposition != "false-detection" && disposition != "dont-track" {
		return fmt.Errorf("invalid disposition %q", disposition)
	}
	eventID, err := id.NewUUIDv7()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO face_disposition_events (id, face_id, disposition, active, account_id, reason, created_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?)`, eventID, faceID, disposition, boolInt(active), actor.AccountID, reason, now)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO current_face_dispositions (face_id, disposition, event_id, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(face_id) DO UPDATE SET disposition = excluded.disposition, event_id = excluded.event_id, updated_at = excluded.updated_at`, faceID, disposition, eventID, now)
	return err
}

func (s Store) setAssignment(ctx context.Context, faceID, personID string, state AssignmentState, actor Actor, evidence Evidence, action, reason string) (Assignment, error) {
	current, currentErr := s.Current(ctx, faceID)
	if currentErr != nil && currentErr != sql.ErrNoRows {
		return Assignment{}, currentErr
	}
	eventID, err := id.NewUUIDv7()
	if err != nil {
		return Assignment{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Assignment{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO assignment_events (
			id, face_id, person_id, previous_state, previous_person_id, new_state,
			actor_type, account_id, source_pipeline_id, clustering_run_id,
			cluster_id, similarity, margin, support_count, quality,
			detection_confidence, purity, connectivity, agreement, action, reason, created_at
		) VALUES (?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, faceID, personID, previousState(current, currentErr), previousPerson(current, currentErr), state,
		actor.Type, actor.AccountID, evidence.PipelineID, evidence.ClusteringRunID, evidence.ClusterID,
		evidence.Similarity, evidence.Margin, evidence.Support, evidence.Quality, evidence.Detection,
		evidence.Purity, evidence.Connectivity, nullableBool(evidence.Agreement), action, reason, now)
	if err != nil {
		return Assignment{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO current_face_assignments (face_id, person_id, state, event_id, updated_at)
		VALUES (?, NULLIF(?, ''), ?, ?, ?)
		ON CONFLICT(face_id) DO UPDATE SET person_id = excluded.person_id, state = excluded.state, event_id = excluded.event_id, updated_at = excluded.updated_at`, faceID, personID, state, eventID, now)
	if err != nil {
		return Assignment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Assignment{}, err
	}
	parsed, _ := time.Parse(time.RFC3339Nano, now)
	return Assignment{FaceID: faceID, PersonID: personID, State: state, EventID: eventID, UpdatedAt: parsed}, nil
}

func validateAssignment(state AssignmentState, personID string) error {
	if state != Unassigned && state != Provisional && state != AssignedSystem && state != AssignedUser && state != Unknown && state != Ambiguous {
		return fmt.Errorf("invalid assignment state %q", state)
	}
	if (state == Provisional || state == AssignedSystem || state == AssignedUser) && personID == "" {
		return fmt.Errorf("state %q requires a person", state)
	}
	if (state == Unassigned || state == Unknown || state == Ambiguous) && personID != "" {
		return fmt.Errorf("state %q cannot have a person", state)
	}
	return nil
}

func previousState(current Assignment, err error) string {
	if err != nil {
		return ""
	}
	return string(current.State)
}

func previousPerson(current Assignment, err error) string {
	if err != nil {
		return ""
	}
	return current.PersonID
}

func nullableBool(value *bool) any {
	if value == nil {
		return nil
	}
	return boolInt(*value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
