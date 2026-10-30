package analysis

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/weisnobody/whothatface/internal/db"
	"github.com/weisnobody/whothatface/internal/worker"
)

func TestRegisterPipelineIsDeterministicAndPersistStoresFaces(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "analysis.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewStore(database)
	pipeline := Pipeline{ModelFamily: "buffalo_l", ModelVersion: "2.0.0", EmbeddingDimension: 2, EmbeddingDType: "float32", Normalization: "l2", ExecutionProvider: "CPUExecutionProvider"}
	first, err := store.RegisterPipeline(context.Background(), pipeline)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RegisterPipeline(context.Background(), pipeline)
	if err != nil || first != second {
		t.Fatalf("pipeline IDs: %q %q, error=%v", first, second, err)
	}
	var embedding [8]byte
	binary.LittleEndian.PutUint32(embedding[0:4], 0x3f800000)
	binary.LittleEndian.PutUint32(embedding[4:8], 0)
	ok := true
	response := worker.Message{Command: "analysis_result", OK: &ok, Model: &worker.ModelInfo{PipelineID: first}, Image: &worker.ImageInfo{Width: 100, Height: 80, InferenceWidth: 100, InferenceHeight: 80}, Faces: []worker.FaceResult{{FaceIndex: 0, Box: worker.BoundingBox{Left: 1, Top: 2, Right: 20, Bottom: 30}, DetectionConfidence: 0.9, Quality: 0.8, Embedding: &worker.Embedding{Encoding: "base64", DType: "float32", ByteOrder: "little", Data: base64.StdEncoding.EncodeToString(embedding[:]), Dimension: 2, Normalized: true}}}}
	var assetID string
	if err := database.QueryRow(`SELECT id FROM photo_assets LIMIT 1`).Scan(&assetID); err != nil {
		// Create a minimal asset through SQL for this focused repository test.
		assetID = "asset-test"
		_, err = database.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES (?, ?, 1, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, assetID, "test-hash")
		if err != nil {
			t.Fatal(err)
		}
	}
	analysisID, err := store.Persist(context.Background(), assetID, first, response)
	if err != nil {
		t.Fatal(err)
	}
	var faceCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM faces WHERE analysis_id = ?`, analysisID).Scan(&faceCount); err != nil {
		t.Fatal(err)
	}
	if faceCount != 1 {
		t.Errorf("face count = %d", faceCount)
	}
}
