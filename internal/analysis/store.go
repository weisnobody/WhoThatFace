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

// MarkRunning creates or updates the durable analysis state before work is
// sent to the worker. A later retry reuses this row rather than creating a
// second analysis for the same asset and pipeline.
func (s Store) MarkRunning(ctx context.Context, assetID, pipelineID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	analysisID, err := id.NewUUIDv7()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO analyses (id, asset_id, pipeline_id, status, started_at, created_at)
		VALUES (?, ?, ?, 'running', ?, ?)
		ON CONFLICT(asset_id, pipeline_id) DO UPDATE SET status = 'running', error_code = NULL, error_message = NULL, started_at = excluded.started_at, completed_at = NULL`,
		analysisID, assetID, pipelineID, now, now)
	return err
}

func (s Store) RecordFailure(ctx context.Context, assetID, pipelineID, code, message string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	analysisID, err := id.NewUUIDv7()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO analyses (id, asset_id, pipeline_id, status, error_code, error_message, completed_at, created_at)
		VALUES (?, ?, ?, 'failed', ?, ?, ?, ?)
		ON CONFLICT(asset_id, pipeline_id) DO UPDATE SET status = 'failed', error_code = excluded.error_code, error_message = excluded.error_message, completed_at = excluded.completed_at`,
		analysisID, assetID, pipelineID, code, message, now, now)
	return err
}

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
	if response.Command != "analysis_result" || response.OK == nil || !*response.OK {
		return "", fmt.Errorf("worker response command %q is not a successful analysis_result", response.Command)
	}
	if response.Model != nil && response.Model.PipelineID != "" && response.Model.PipelineID != pipelineID {
		return "", fmt.Errorf("worker pipeline %q does not match requested pipeline %q", response.Model.PipelineID, pipelineID)
	}
	if response.Image != nil && (response.Image.Width <= 0 || response.Image.Height <= 0 || response.Image.InferenceWidth <= 0 || response.Image.InferenceHeight <= 0) {
		return "", fmt.Errorf("worker returned invalid image dimensions")
	}
	var analysisID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM analyses WHERE asset_id = ? AND pipeline_id = ?`, assetID, pipelineID).Scan(&analysisID); err == sql.ErrNoRows {
		analysisID, err = id.NewUUIDv7()
		if err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var width, height, inferenceWidth, inferenceHeight int
	if response.Image != nil {
		width, height = response.Image.Width, response.Image.Height
		inferenceWidth, inferenceHeight = response.Image.InferenceWidth, response.Image.InferenceHeight
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM faces WHERE analysis_id = ?`, analysisID)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO analyses (
			id, asset_id, pipeline_id, status, source_width, source_height,
			inference_width, inference_height, completed_at, created_at
		) VALUES (?, ?, ?, 'completed', NULLIF(?, 0), NULLIF(?, 0), NULLIF(?, 0), NULLIF(?, 0), ?, ?)
		ON CONFLICT(asset_id, pipeline_id) DO UPDATE SET status = 'completed', source_width = excluded.source_width, source_height = excluded.source_height, inference_width = excluded.inference_width, inference_height = excluded.inference_height, error_code = NULL, error_message = NULL, completed_at = excluded.completed_at`,
		analysisID, assetID, pipelineID, width, height, inferenceWidth, inferenceHeight, now, now)
	if err != nil {
		return "", fmt.Errorf("persist analysis: %w", err)
	}
	for expectedIndex, face := range response.Faces {
		if face.FaceIndex != expectedIndex {
			return "", fmt.Errorf("face ordering is not deterministic at index %d", expectedIndex)
		}
		if !finiteRange(float64(face.DetectionConfidence), 0, 1) || !finiteRange(float64(face.Quality), 0, 1) {
			return "", fmt.Errorf("face %d has invalid confidence or quality", face.FaceIndex)
		}
		if face.Box.Right < face.Box.Left || face.Box.Bottom < face.Box.Top {
			return "", fmt.Errorf("face %d has an invalid bounding box", face.FaceIndex)
		}
		if response.Image != nil && (face.Box.Left < 0 || face.Box.Top < 0 || face.Box.Right > float32(response.Image.Width) || face.Box.Bottom > float32(response.Image.Height)) {
			return "", fmt.Errorf("face %d bounding box is outside image dimensions", face.FaceIndex)
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
			faceID, analysisID, face.FaceIndex, face.Box.Left, face.Box.Top, face.Box.Right, face.Box.Bottom,
			face.DetectionConfidence, face.Quality, embedding, embeddingDimension(face),
			embeddingDType(face), boolInt(embeddingNormalized(face)), now)
		if err != nil {
			return "", fmt.Errorf("persist face %d: %w", face.FaceIndex, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return analysisID, nil
}

func finiteRange(value, minimum, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= minimum && value <= maximum
}

func embeddingDimension(face worker.FaceResult) int {
	if face.Embedding == nil {
		return 0
	}
	return face.Embedding.Dimension
}

func embeddingDType(face worker.FaceResult) string {
	if face.Embedding == nil {
		return ""
	}
	return face.Embedding.DType
}

func embeddingNormalized(face worker.FaceResult) bool {
	return face.Embedding != nil && face.Embedding.Normalized
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
