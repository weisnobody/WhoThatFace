package clustering

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
)

type RunStore struct{ db *sql.DB }

func NewRunStore(database *sql.DB) RunStore { return RunStore{db: database} }

func (s RunStore) CreateRun(ctx context.Context, config Config, scope string, faceIDs []string) (string, error) {
	runID, err := id.NewUUIDv7()
	if err != nil {
		return "", err
	}
	input, err := json.Marshal(faceIDs)
	if err != nil {
		return "", err
	}
	algorithmVersion := config.AlgorithmVersion
	if algorithmVersion == "" {
		algorithmVersion = "mutual-knn-v1"
	}
	method := config.NeighborSearchMethod
	if method == "" {
		method = "exact"
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO clustering_runs (
			id, algorithm, algorithm_version, k, similarity_threshold,
			neighbor_search_method, mutual_edge_policy, candidate_scope,
			input_face_set_json, status, created_at
		) VALUES (?, 'mutual-knn', ?, ?, ?, ?, 'mutual-top-k', ?, ?, 'queued', ?)`,
		runID, algorithmVersion, config.K, config.SimilarityThreshold, method, scope, input, time.Now().UTC().Format(time.RFC3339Nano))
	return runID, err
}

func (s RunStore) PersistResult(ctx context.Context, runID string, result Result) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE clustering_runs SET status = 'completed', completed_at = ? WHERE id = ?`, now, runID); err != nil {
		return err
	}
	for _, cluster := range result.Clusters {
		clusterID, err := id.NewUUIDv7()
		if err != nil {
			return err
		}
		status := "unreviewed"
		if cluster.Mixed {
			status = "needs-review"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO clusters (id, clustering_run_id, cluster_key, proposed_person_id, purity, connectivity, is_mixed, review_status, created_at)
			VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`, clusterID, runID, cluster.Key, cluster.ProposedPersonID, nullableFloat(cluster.Purity), nullableFloat(cluster.Connectivity), boolInt(cluster.Mixed), status, now); err != nil {
			return fmt.Errorf("persist cluster %q: %w", cluster.Key, err)
		}
		for _, faceID := range cluster.FaceIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO cluster_memberships (cluster_id, face_id) VALUES (?, ?)`, clusterID, faceID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// Activate makes a completed run current and retains the prior run as
// superseded. Existing viewers can continue querying the old run.
func (s RunStore) Activate(ctx context.Context, runID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM clustering_runs WHERE id = ?", runID).Scan(&status); err != nil {
		return err
	}
	if status != "completed" && status != "active" {
		return fmt.Errorf("clustering run %q is not complete", runID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE clustering_runs SET status = 'superseded', superseded_by = ? WHERE status = 'active' AND id <> ?`, runID, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE clustering_runs SET status = 'active', accepted_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), runID); err != nil {
		return err
	}
	return tx.Commit()
}

func nullableFloat(value float32) any {
	if value == 0 {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
