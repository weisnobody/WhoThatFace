package worker

import "testing"

func TestValidateCapabilitiesAgainstPipeline(t *testing.T) {
	capabilities := &Capabilities{
		InsightFaceVersion: "2.0.0",
		Providers:          []string{"CPUExecutionProvider"},
		Models:             []string{"buffalo_l"},
		EmbeddingDimension: 512,
		EmbeddingDType:     "float32",
		EmbeddingMetric:    "cosine",
		ModelChecksums:     map[string]string{"model.onnx": "abc"},
		Modules:            []string{"detection", "recognition"},
		Preprocessing:      "pillow-exif-transpose-opencv-bgr-v1",
	}
	err := validateCapabilities(capabilities, &Expectation{
		Model:              "buffalo_l",
		Provider:           "CPUExecutionProvider",
		ModelChecksums:     map[string]string{"model.onnx": "abc"},
		EmbeddingDimension: 512,
		EmbeddingDType:     "float32",
		EmbeddingMetric:    "cosine",
		Modules:            []string{"detection", "recognition"},
		Preprocessing:      "pillow-exif-transpose-opencv-bgr-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestValidateCapabilitiesRejectsChecksumMismatch(t *testing.T) {
	capabilities := &Capabilities{
		InsightFaceVersion: "2.0.0", Providers: []string{"CPUExecutionProvider"},
		Models: []string{"buffalo_l"}, EmbeddingDimension: 512,
		EmbeddingDType: "float32", EmbeddingMetric: "cosine",
		ModelChecksums: map[string]string{"model.onnx": "actual"},
		Modules:        []string{"detection", "recognition"}, Preprocessing: "pillow-exif-transpose-opencv-bgr-v1",
	}
	if err := validateCapabilities(capabilities, &Expectation{Model: "buffalo_l", ModelChecksums: map[string]string{"model.onnx": "expected"}}); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}
