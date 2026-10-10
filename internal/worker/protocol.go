// Package worker defines the versioned NDJSON contract between Go and the
// Python image-analysis worker.
package worker

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
)

const (
	ProtocolMajor = 1
	ProtocolMinor = 0
)

type Message struct {
	Type            string        `json:"type"`
	ID              string        `json:"id,omitempty"`
	ProtocolMajor   int           `json:"protocol_major,omitempty"`
	ProtocolMinor   int           `json:"protocol_minor,omitempty"`
	Capabilities    *Capabilities `json:"capabilities,omitempty"`
	PipelineID      string        `json:"pipeline_id,omitempty"`
	ImagePath       string        `json:"image_path,omitempty"`
	ExpectedSHA256  string        `json:"expected_sha256,omitempty"`
	ExpectedSize    int64         `json:"expected_size,omitempty"`
	MaxPixels       int64         `json:"max_pixels,omitempty"`
	Faces           []FaceResult  `json:"faces,omitempty"`
	Error           *WorkerError  `json:"error,omitempty"`
	SourceWidth     int           `json:"source_width,omitempty"`
	SourceHeight    int           `json:"source_height,omitempty"`
	InferenceWidth  int           `json:"inference_width,omitempty"`
	InferenceHeight int           `json:"inference_height,omitempty"`
}

type Capabilities struct {
	WorkerVersion      string            `json:"worker_version"`
	PythonVersion      string            `json:"python_version"`
	InsightFaceVersion string            `json:"insightface_version"`
	Providers          []string          `json:"providers"`
	Models             []string          `json:"models"`
	EmbeddingDimension int               `json:"embedding_dimension"`
	EmbeddingDType     string            `json:"embedding_dtype"`
	EmbeddingMetric    string            `json:"embedding_metric"`
	ModelChecksums     map[string]string `json:"model_checksums,omitempty"`
	Modules            []string          `json:"modules,omitempty"`
	Preprocessing      string            `json:"preprocessing,omitempty"`
}

type WorkerError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Detail    string `json:"detail,omitempty"`
}

type FaceResult struct {
	FaceIndex           int        `json:"face_index"`
	Box                 [4]float32 `json:"box"`
	DetectionConfidence float32    `json:"detection_confidence"`
	Quality             float32    `json:"quality"`
	EmbeddingBase64     string     `json:"embedding_base64,omitempty"`
	EmbeddingDimension  int        `json:"embedding_dimension,omitempty"`
	EmbeddingDType      string     `json:"embedding_dtype,omitempty"`
	Normalized          bool       `json:"normalized,omitempty"`
}

// DecodeEmbedding validates and decodes a little-endian float32 embedding.
func (f FaceResult) DecodeEmbedding() ([]float32, error) {
	if f.EmbeddingBase64 == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(f.EmbeddingBase64)
	if err != nil {
		return nil, fmt.Errorf("decode embedding base64: %w", err)
	}
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("embedding byte length %d is not divisible by 4", len(data))
	}
	values := make([]float32, len(data)/4)
	for index := range values {
		values[index] = mathFloat32frombits(binary.LittleEndian.Uint32(data[index*4:]))
	}
	if f.EmbeddingDimension <= 0 || f.EmbeddingDimension != len(values) {
		return nil, fmt.Errorf("embedding dimension %d does not match %d values", f.EmbeddingDimension, len(values))
	}
	return values, nil
}
