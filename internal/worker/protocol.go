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
	ProtocolVersion int           `json:"protocol_version,omitempty"`
	ID              string        `json:"id,omitempty"`
	Command         string        `json:"command,omitempty"`
	Path            string        `json:"path,omitempty"`
	ExpectedSHA256  string        `json:"expected_sha256,omitempty"`
	ExpectedSize    int64         `json:"expected_size,omitempty"`
	MaxPixels       int64         `json:"max_pixels,omitempty"`
	OK              *bool         `json:"ok,omitempty"`
	Capabilities    *Capabilities `json:"capabilities,omitempty"`
	Model           *ModelInfo    `json:"model,omitempty"`
	Image           *ImageInfo    `json:"image,omitempty"`
	Faces           []FaceResult  `json:"faces,omitempty"`
	Error           *WorkerError  `json:"error,omitempty"`
}

type ImageInfo struct {
	Width           int `json:"width,omitempty"`
	Height          int `json:"height,omitempty"`
	InferenceWidth  int `json:"inference_width,omitempty"`
	InferenceHeight int `json:"inference_height,omitempty"`
}

type ModelInfo struct {
	PipelineID string `json:"pipeline_id,omitempty"`
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
	DetectorSize       string            `json:"detector_size,omitempty"`
}

// Expectation describes the pipeline contract that a worker must satisfy.
// Empty optional fields mean that the caller does not constrain that value.
type Expectation struct {
	Model              string
	Provider           string
	ModelChecksums     map[string]string
	EmbeddingDimension int
	EmbeddingDType     string
	EmbeddingMetric    string
	Modules            []string
	Preprocessing      string
	DetectorSize       string
}

type WorkerError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Detail    string `json:"detail,omitempty"`
}

type FaceResult struct {
	FaceIndex           int         `json:"face_index"`
	Box                 BoundingBox `json:"box"`
	DetectionConfidence float32     `json:"detection_confidence"`
	Quality             float32     `json:"quality"`
	Embedding           *Embedding  `json:"embedding,omitempty"`
}

type BoundingBox struct {
	Left   float32 `json:"left"`
	Top    float32 `json:"top"`
	Right  float32 `json:"right"`
	Bottom float32 `json:"bottom"`
}

type Embedding struct {
	Encoding   string `json:"encoding"`
	DType      string `json:"dtype"`
	ByteOrder  string `json:"byte_order"`
	Dimension  int    `json:"dimension"`
	Normalized bool   `json:"normalized"`
	Data       string `json:"data"`
}

// DecodeEmbedding validates and decodes a little-endian float32 embedding.
func (f FaceResult) DecodeEmbedding() ([]float32, error) {
	if f.Embedding == nil || f.Embedding.Data == "" {
		return nil, nil
	}
	if f.Embedding.Encoding != "base64" || f.Embedding.DType != "float32" || f.Embedding.ByteOrder != "little" || !f.Embedding.Normalized {
		return nil, fmt.Errorf("unsupported embedding encoding")
	}
	data, err := base64.StdEncoding.DecodeString(f.Embedding.Data)
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
	if f.Embedding.Dimension <= 0 || f.Embedding.Dimension != len(values) {
		return nil, fmt.Errorf("embedding dimension %d does not match %d values", f.Embedding.Dimension, len(values))
	}
	return values, nil
}
