package analysis

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/weisnobody/whothatface/internal/db"
	"github.com/weisnobody/whothatface/internal/worker"
)

type fakeAnalyzer struct{ calls int }

func (fake *fakeAnalyzer) Analyze(context.Context, string, string, int64) (worker.Message, error) {
	fake.calls++
	ok := true
	return worker.Message{Command: "analysis_result", OK: &ok, Model: &worker.ModelInfo{PipelineID: "pipeline"}, Image: &worker.ImageInfo{Width: 100, Height: 100, InferenceWidth: 100, InferenceHeight: 100}, Faces: []worker.FaceResult{{FaceIndex: 0, Box: worker.BoundingBox{Right: 1, Bottom: 1}, DetectionConfidence: 0.9, Quality: 0.9, Embedding: &worker.Embedding{Encoding: "base64", DType: "float32", ByteOrder: "little", Normalized: true, Data: base64.StdEncoding.EncodeToString(make([]byte, 8)), Dimension: 2}}}}, nil
}

func TestProcessCollectionAnalyzesDuplicateAssetOnce(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "processor.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection', 'Photos', '/photos', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES ('asset', 'hash', 1, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"one.jpg", "two.jpg"} {
		_, err = database.Exec(`INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES (?, 'collection', 'asset', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 'still')`, "path-"+path, path)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = database.Exec(`INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES ('pipeline', 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'cpu', '{}', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAnalyzer{}
	processor := NewProcessor(database, fake, "pipeline", 0)
	if err := processor.ProcessCollection(context.Background(), "collection"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 {
		t.Errorf("analyzer calls = %d, want 1", fake.calls)
	}
	var faces, assignments int
	if err := database.QueryRow(`SELECT COUNT(*) FROM faces`).Scan(&faces); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM current_face_assignments`).Scan(&assignments); err != nil {
		t.Fatal(err)
	}
	if faces != 1 || assignments != 1 {
		t.Errorf("faces=%d assignments=%d", faces, assignments)
	}
}
