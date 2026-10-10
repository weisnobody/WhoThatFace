package analysis

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"

	"github.com/pjg/face_me_bro/internal/db"
	"github.com/pjg/face_me_bro/internal/worker"
)

type fakeAnalyzer struct{ calls int }

func (fake *fakeAnalyzer) Analyze(context.Context, string, string, int64) (worker.Message, error) {
	fake.calls++
	return worker.Message{Type: "analysis_result", Faces: []worker.FaceResult{{FaceIndex: 0, Box: [4]float32{0, 0, 1, 1}, EmbeddingBase64: base64.StdEncoding.EncodeToString(make([]byte, 8)), EmbeddingDimension: 2, EmbeddingDType: "float32"}}}, nil
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
