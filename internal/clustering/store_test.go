package clustering

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/weisnobody/whothatface/internal/db"
)

func TestRunStorePersistsAndActivatesRuns(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "clustering.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, faceID := range []string{"face-a", "face-b"} {
		_, err := database.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES (?, ?, 1, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, "asset-"+faceID, "hash-"+faceID)
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
		_, err = database.Exec(`INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, created_at) VALUES (?, ?, 0, 0, 0, 1, 1, CURRENT_TIMESTAMP)`, faceID, "analysis-"+faceID)
		if err != nil {
			t.Fatal(err)
		}
	}
	store := NewRunStore(database)
	runID, err := store.CreateRun(context.Background(), DefaultConfig(), "all", []string{"face-a", "face-b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistResult(context.Background(), runID, Result{Clusters: []Cluster{{Key: "face-a", FaceIDs: []string{"face-a", "face-b"}, Purity: 1, Connectivity: 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM clustering_runs WHERE id = ?`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Errorf("status = %q", status)
	}
}
