package review

import (
	"context"
	"fmt"
	"time"

	"github.com/pjg/face_me_bro/internal/audit"
	"github.com/pjg/face_me_bro/internal/id"
)

type ClusterFaceDecision struct {
	FaceID            string          `json:"face_id"`
	PersonID          string          `json:"person_id"`
	State             AssignmentState `json:"state"`
	Disposition       string          `json:"disposition"`
	DispositionActive *bool           `json:"disposition_active,omitempty"`
}

// ReviewCluster records the cluster review event first, then applies explicit
// per-face decisions. A default person only affects faces without an existing
// individual assignment.
func (s Store) ReviewCluster(ctx context.Context, clusterID string, defaultPersonID string, status string, expectedVersion int, decisions []ClusterFaceDecision, actor Actor, note string) error {
	validStatus := map[string]bool{"in-progress": true, "partially-reviewed": true, "reviewed": true, "needs-review": true}
	if !validStatus[status] {
		return fmt.Errorf("invalid cluster review status %q", status)
	}
	var runID string
	var currentVersion, faceCount, mixed int
	if err := s.db.QueryRowContext(ctx, `SELECT clustering_run_id, review_version, is_mixed, (SELECT COUNT(*) FROM cluster_memberships WHERE cluster_id = c.id) FROM clusters c WHERE id = ?`, clusterID).Scan(&runID, &currentVersion, &mixed, &faceCount); err != nil {
		return err
	}
	if mixed != 0 && defaultPersonID != "" {
		return fmt.Errorf("mixed cluster requires explicit per-face decisions; default assignment is not allowed")
	}
	if expectedVersion != currentVersion {
		return fmt.Errorf("cluster review conflict: version %d is current, received %d", currentVersion, expectedVersion)
	}
	if faceCount == 1 && status == "reviewed" && defaultPersonID == "" && len(decisions) == 0 {
		return fmt.Errorf("singleton cluster requires an explicit assignment, unknown, ambiguous, false-detection, or don't-track decision")
	}
	if defaultPersonID != "" {
		var exists int
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM people WHERE id = ?)`, defaultPersonID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("default person %q does not exist", defaultPersonID)
		}
	}
	for _, decision := range decisions {
		if decision.FaceID == "" {
			continue
		}
		var member int
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cluster_memberships WHERE cluster_id = ? AND face_id = ?)`, clusterID, decision.FaceID).Scan(&member); err != nil {
			return err
		}
		if member == 0 {
			return fmt.Errorf("face %q is not a member of cluster %q", decision.FaceID, clusterID)
		}
		if decision.State != "" {
			if err := validateAssignment(decision.State, decision.PersonID); err != nil {
				return err
			}
		}
		if decision.Disposition != "" && decision.Disposition != "incorrect-assignment" && decision.Disposition != "false-detection" && decision.Disposition != "dont-track" {
			return fmt.Errorf("invalid disposition %q", decision.Disposition)
		}
	}
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO cluster_review_events (id, cluster_id, account_id, status, default_person_id, note, created_at) VALUES (?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?)`, eventID, clusterID, actor.AccountID, status, defaultPersonID, note, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE clusters SET review_status = ?, review_version = review_version + 1 WHERE id = ? AND review_version = ?`, status, clusterID, expectedVersion)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("cluster review conflict: cluster changed while saving")
	}
	var auditEvents []audit.Event
	for _, decision := range decisions {
		if decision.FaceID == "" {
			continue
		}
		if decision.State != "" {
			if _, auditEvent, err := s.setAssignmentInTx(ctx, tx, decision.FaceID, decision.PersonID, decision.State, actor, Evidence{ClusteringRunID: runID, ClusterID: clusterID}, "cluster-review", ""); err != nil {
				return err
			} else {
				auditEvents = append(auditEvents, auditEvent)
			}
		}
		if decision.Disposition != "" {
			active := true
			if decision.DispositionActive != nil {
				active = *decision.DispositionActive
			}
			if auditEvent, err := s.setDispositionInTx(ctx, tx, decision.FaceID, decision.Disposition, active, actor, "cluster-review"); err != nil {
				return err
			} else {
				auditEvents = append(auditEvents, auditEvent)
			}
		}
	}
	if defaultPersonID != "" {
		rows, err := tx.QueryContext(ctx, `SELECT cm.face_id FROM cluster_memberships cm LEFT JOIN current_face_assignments ca ON ca.face_id = cm.face_id WHERE cm.cluster_id = ? AND (ca.face_id IS NULL OR ca.state = 'unassigned')`, clusterID)
		if err != nil {
			return err
		}
		var faceIDs []string
		for rows.Next() {
			var faceID string
			if err := rows.Scan(&faceID); err != nil {
				rows.Close()
				return err
			}
			faceIDs = append(faceIDs, faceID)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, faceID := range faceIDs {
			if _, auditEvent, err := s.setAssignmentInTx(ctx, tx, faceID, defaultPersonID, AssignedUser, actor, Evidence{ClusteringRunID: runID, ClusterID: clusterID}, "cluster-default", ""); err != nil {
				return err
			} else {
				auditEvents = append(auditEvents, auditEvent)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, auditEvent := range auditEvents {
		if err := audit.NewStore(s.db).Record(ctx, auditEvent); err != nil {
			return err
		}
	}
	return audit.NewStore(s.db).Record(ctx, audit.Event{AccountID: actor.AccountID, TargetEventID: eventID, Action: "cluster-review", EntityType: "cluster", EntityID: clusterID, New: map[string]any{"status": status, "default_person_id": defaultPersonID, "decisions": len(decisions)}})
}
