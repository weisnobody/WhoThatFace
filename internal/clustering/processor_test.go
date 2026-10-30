package clustering

import (
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/weisnobody/whothatface/internal/db"
)

func TestProcessorRunsAndActivatesClustering(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "processor.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, faceID := range []string{"face-a", "face-b"} {
		_, err = database.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES (?, ?, 1, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "asset-"+faceID, "hash-"+faceID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = database.Exec(`INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES (?, 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'cpu', '{}', CURRENT_TIMESTAMP)`, "pipeline-"+faceID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = database.Exec(`INSERT INTO analyses (id, asset_id, pipeline_id, status, created_at) VALUES (?, ?, ?, 'completed', CURRENT_TIMESTAMP)`, "analysis-"+faceID, "asset-"+faceID, "pipeline-"+faceID)
		if err != nil {
			t.Fatal(err)
		}
		blob := make([]byte, 8)
		if faceID == "face-a" {
			binary.LittleEndian.PutUint32(blob[0:], math.Float32bits(1))
			binary.LittleEndian.PutUint32(blob[4:], math.Float32bits(0))
		} else {
			binary.LittleEndian.PutUint32(blob[0:], math.Float32bits(.99))
			binary.LittleEndian.PutUint32(blob[4:], math.Float32bits(.1))
		}
		_, err = database.Exec(`INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, embedding, embedding_dimension, embedding_dtype, embedding_normalized, created_at) VALUES (?, ?, 0, 0, 0, 1, 1, ?, 2, 'float32', 1, CURRENT_TIMESTAMP)`, faceID, "analysis-"+faceID, blob)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO people (id, name, is_provisional, created_at, updated_at) VALUES ('recognized-person', 'Recognized', 0, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP); INSERT INTO recognition_candidates (id, face_id, person_id, rank, similarity, margin, support_count, quality, detection_confidence, evidence, created_at) VALUES ('candidate-face-a', 'face-a', 'recognized-person', 1, 0.10, 0.01, 1, 0.5, 0.5, 'manual-review', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	runID, err := NewProcessor(database, Config{K: 1, SimilarityThreshold: .9}).Run(context.Background(), "all")
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM clustering_runs WHERE id = ?`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Errorf("status = %q", status)
	}
	var assignmentState, assignedPerson, reviewStatus string
	if err := database.QueryRow(`SELECT COALESCE(ca.state, ''), COALESCE(ca.person_id, ''), c.review_status FROM current_face_assignments ca JOIN cluster_memberships cm ON cm.face_id = ca.face_id JOIN clusters c ON c.id = cm.cluster_id WHERE ca.face_id = 'face-a' AND c.clustering_run_id = ?`, runID).Scan(&assignmentState, &assignedPerson, &reviewStatus); err != nil {
		t.Fatal(err)
	}
	if assignmentState != "provisional" || assignedPerson == "recognized-person" || reviewStatus != "needs-review" {
		t.Fatalf("disagreement assignment = %q/%q, cluster review status = %q; want provisional cluster person and needs-review", assignmentState, assignedPerson, reviewStatus)
	}
}
