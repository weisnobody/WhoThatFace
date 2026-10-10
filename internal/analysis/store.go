// Package analysis persists pipeline provenance and worker face results.
package analysis

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/pjg/face_me_bro/internal/id"
	"github.com/pjg/face_me_bro/internal/worker"
)

type Pipeline struct {
	ModelFamily         string
	ModelVersion        string
	ModelChecksums      map[string]string
	DetectorConfig      any
	PreprocessingConfig any
	EmbeddingDimension  int
	EmbeddingDType      string
	Normalization       string
	ExecutionProvider   string
	DependencyVersions  map[string]string
}

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

// RegisterPipeline returns a deterministic ID derived from the complete
// serialized pipeline configuration.
func (s Store) RegisterPipeline(ctx context.Context, pipeline Pipeline) (string, error) {
	configJSON, err := json.Marshal(pipeline)
	if err != nil {
		return "", fmt.Errorf("marshal pipeline configuration: %w", err)
	}
	digest := sha256.Sum256(configJSON)
	pipelineID := "pipeline-" + hex.EncodeToString(digest[:])
	checksums, _ := json.Marshal(pipeline.ModelChecksums)
	detector, _ := json.Marshal(pipeline.DetectorConfig)
	preprocessing, _ := json.Marshal(pipeline.PreprocessingConfig)
	dependencies, _ := json.Marshal(pipeline.DependencyVersions)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO pipelines (
			id, model_family, model_version, model_checksums_json,
			detector_config_json, preprocessing_config_json, embedding_dimension,
			embedding_dtype, normalization, execution_provider,
			dependency_versions_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pipelineID, pipeline.ModelFamily, pipeline.ModelVersion, checksums,
		detector, preprocessing, pipeline.EmbeddingDimension,
		pipeline.EmbeddingDType, pipeline.Normalization, pipeline.ExecutionProvider,
		dependencies, now)
	if err != nil {
		return "", fmt.Errorf("register pipeline: %w", err)
	}
	return pipelineID, nil
}

// Persist stores one successful worker response. A no-face response is a
// valid completed analysis and inserts zero rows into faces.
func (s Store) Persist(ctx context.Context, assetID, pipelineID string, response worker.Message) (string, error) {
	if response.Type != "analysis_result" {
		return "", fmt.Errorf("worker response type %q is not analysis_result", response.Type)
	}
	if response.PipelineID != "" && response.PipelineID != pipelineID {
		return "", fmt.Errorf("worker pipeline %q does not match requested pipeline %q", response.PipelineID, pipelineID)
	}
	analysisID, err := id.NewUUIDv7()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO analyses (
			id, asset_id, pipeline_id, status, source_width, source_height,
			inference_width, inference_height, completed_at, created_at
		) VALUES (?, ?, ?, 'completed', NULLIF(?, 0), NULLIF(?, 0), NULLIF(?, 0), NULLIF(?, 0), ?, ?)`,
		analysisID, assetID, pipelineID, response.SourceWidth, response.SourceHeight,
		response.InferenceWidth, response.InferenceHeight, now, now)
	if err != nil {
		return "", fmt.Errorf("persist analysis: %w", err)
	}
	for _, face := range response.Faces {
		if face.Box[2] < face.Box[0] || face.Box[3] < face.Box[1] {
			return "", fmt.Errorf("face %d has an invalid bounding box", face.FaceIndex)
		}
		embedding, err := encodeEmbedding(face)
		if err != nil {
			return "", fmt.Errorf("face %d embedding: %w", face.FaceIndex, err)
		}
		faceID, err := id.NewUUIDv7()
		if err != nil {
			return "", err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO faces (
				id, analysis_id, face_index, box_left, box_top, box_right,
				box_bottom, detection_confidence, quality, embedding,
				embedding_dimension, embedding_dtype, embedding_normalized, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			faceID, analysisID, face.FaceIndex, face.Box[0], face.Box[1], face.Box[2], face.Box[3],
			face.DetectionConfidence, face.Quality, embedding, face.EmbeddingDimension,
			face.EmbeddingDType, boolInt(face.Normalized), now)
		if err != nil {
			return "", fmt.Errorf("persist face %d: %w", face.FaceIndex, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return analysisID, nil
}

func encodeEmbedding(face worker.FaceResult) ([]byte, error) {
	values, err := face.DecodeEmbedding()
	if err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, nil
	}
	data := make([]byte, len(values)*4)
	for index, value := range values {
		binary.LittleEndian.PutUint32(data[index*4:], math.Float32bits(value))
	}
	return data, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
