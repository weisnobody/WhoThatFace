package review

import (
	"context"
	"fmt"
	"time"

	"github.com/pjg/face_me_bro/internal/audit"
	"github.com/pjg/face_me_bro/internal/id"
)

type ClusterFaceDecision struct {
	FaceID      string          `json:"face_id"`
	PersonID    string          `json:"person_id"`
	State       AssignmentState `json:"state"`
	Disposition string          `json:"disposition"`
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
	var currentVersion, faceCount int
	if err := s.db.QueryRowContext(ctx, `SELECT clustering_run_id, review_version, (SELECT COUNT(*) FROM cluster_memberships WHERE cluster_id = c.id) FROM clusters c WHERE id = ?`, clusterID).Scan(&runID, &currentVersion, &faceCount); err != nil {
		return err
	}
	if expectedVersion != currentVersion {
		return fmt.Errorf("cluster review conflict: version %d is current, received %d", currentVersion, expectedVersion)
	}
	if faceCount == 1 && status == "reviewed" && defaultPersonID == "" && len(decisions) == 0 {
		return fmt.Errorf("singleton cluster requires an explicit assignment, unknown, ambiguous, false-detection, or don't-track decision")
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
	if err := tx.Commit(); err != nil {
		return err
	}

	// Apply decisions outside the review-event transaction so each assignment
	// retains its own immutable history and audit record.
	for _, decision := range decisions {
		if decision.FaceID == "" {
			continue
		}
		if decision.State != "" {
			var member int
			if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM cluster_memberships WHERE cluster_id = ? AND face_id = ?`, clusterID, decision.FaceID).Scan(&member); err != nil {
				return fmt.Errorf("face %q is not a member of cluster %q", decision.FaceID, clusterID)
			}
			if _, err := s.Assign(ctx, decision.FaceID, decision.PersonID, decision.State, actor, Evidence{ClusteringRunID: runID, ClusterID: clusterID}, "cluster-review"); err != nil {
				return err
			}
		}
		if decision.Disposition != "" {
			if err := s.SetDisposition(ctx, decision.FaceID, decision.Disposition, true, actor, "cluster-review"); err != nil {
				return err
			}
		}
	}
	if defaultPersonID != "" {
		rows, err := s.db.QueryContext(ctx, `SELECT cm.face_id FROM cluster_memberships cm LEFT JOIN current_face_assignments ca ON ca.face_id = cm.face_id WHERE cm.cluster_id = ? AND ca.face_id IS NULL`, clusterID)
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
			if _, err := s.Assign(ctx, faceID, defaultPersonID, AssignedUser, actor, Evidence{ClusteringRunID: runID, ClusterID: clusterID}, "cluster-default"); err != nil {
				return err
			}
		}
	}
	return audit.NewStore(s.db).Record(ctx, audit.Event{AccountID: actor.AccountID, Action: "cluster-review", EntityType: "cluster", EntityID: clusterID, New: map[string]any{"status": status, "default_person_id": defaultPersonID, "decisions": len(decisions)}})
}
