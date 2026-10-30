package review

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/weisnobody/whothatface/internal/audit"
)

var ErrUndoConflict = errors.New("undo conflict")

// UndoAuditEvent compensates a face assignment, reference selection, or face
// disposition only when the targeted event is still the current event.
func (s Store) UndoAuditEvent(ctx context.Context, auditID string, actor Actor) error {
	event, err := audit.NewStore(s.db).Get(ctx, auditID)
	if err != nil {
		return err
	}
	if event.TargetEventID == "" {
		return fmt.Errorf("audit event cannot be undone")
	}
	switch event.Action {
	case "assignment", "cluster-review", "reject":
		if event.EntityType != "face" {
			return fmt.Errorf("cluster review undo requires a new review decision")
		}
		var faceID, previousState, previousPerson string
		if err := s.db.QueryRowContext(ctx, `SELECT face_id, COALESCE(previous_state, 'unassigned'), COALESCE(previous_person_id, '') FROM assignment_events WHERE id = ?`, event.TargetEventID).Scan(&faceID, &previousState, &previousPerson); err != nil {
			return err
		}
		var currentEvent string
		if err := s.db.QueryRowContext(ctx, `SELECT event_id FROM current_face_assignments WHERE face_id = ?`, faceID).Scan(&currentEvent); err != nil {
			return err
		}
		if currentEvent != event.TargetEventID {
			return ErrUndoConflict
		}
		_, err = s.setAssignment(ctx, faceID, previousPerson, AssignmentState(previousState), actor, Evidence{}, "undo", auditID)
	case "reference-selection":
		var faceID, personID string
		var reference int
		if err := s.db.QueryRowContext(ctx, `SELECT face_id, person_id, reference FROM reference_selection_events WHERE id = ?`, event.TargetEventID).Scan(&faceID, &personID, &reference); err != nil {
			return err
		}
		var currentEvent string
		if err := s.db.QueryRowContext(ctx, `SELECT event_id FROM current_face_references WHERE face_id = ?`, faceID).Scan(&currentEvent); err != nil {
			return err
		}
		if currentEvent != event.TargetEventID {
			return ErrUndoConflict
		}
		var priorPerson string
		var priorReference int
		priorPerson, priorReference = personID, 0
		var priorEvent sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT supersedes_event_id FROM reference_selection_events WHERE id = ?`, event.TargetEventID).Scan(&priorEvent); err == nil && priorEvent.Valid {
			_ = s.db.QueryRowContext(ctx, `SELECT person_id, reference FROM reference_selection_events WHERE id = ?`, priorEvent.String).Scan(&priorPerson, &priorReference)
		}
		err = s.SetReference(ctx, faceID, priorPerson, priorReference == 1, actor, "undo")
	case "face-disposition":
		var faceID, disposition string
		if err := s.db.QueryRowContext(ctx, `SELECT face_id, disposition FROM face_disposition_events WHERE id = ?`, event.TargetEventID).Scan(&faceID, &disposition); err != nil {
			return err
		}
		var currentEvent string
		if err := s.db.QueryRowContext(ctx, `SELECT event_id FROM current_face_dispositions WHERE face_id = ?`, faceID).Scan(&currentEvent); err != nil {
			return err
		}
		if currentEvent != event.TargetEventID {
			return ErrUndoConflict
		}
		err = s.SetDisposition(ctx, faceID, disposition, false, actor, "undo")
	default:
		return fmt.Errorf("audit action %q cannot be undone", event.Action)
	}
	if err != nil {
		return err
	}
	return audit.NewStore(s.db).Record(ctx, audit.Event{AccountID: actor.AccountID, Action: "undo", EntityType: event.EntityType, EntityID: event.EntityID, Previous: json.RawMessage(event.New), New: map[string]any{"target_audit_event": auditID}})
}
