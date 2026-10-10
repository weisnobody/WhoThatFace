package worker

import (
	"encoding/base64"
	"encoding/binary"
	"testing"
)

func TestDecodeEmbeddingLittleEndian(t *testing.T) {
	data := make([]byte, 8)
	binary.LittleEndian.PutUint32(data[0:4], 0x3f800000)
	binary.LittleEndian.PutUint32(data[4:8], 0xbf000000)
	face := FaceResult{EmbeddingBase64: base64.StdEncoding.EncodeToString(data), EmbeddingDimension: 2}
	values, err := face.DecodeEmbedding()
	if err != nil {
		t.Fatal(err)
	}
	if values[0] != 1 || values[1] != -0.5 {
		t.Errorf("values = %v", values)
	}
}

func TestDecodeEmbeddingRejectsDimensionMismatch(t *testing.T) {
	face := FaceResult{EmbeddingBase64: base64.StdEncoding.EncodeToString(make([]byte, 4)), EmbeddingDimension: 2}
	if _, err := face.DecodeEmbedding(); err == nil {
		t.Fatal("expected dimension error")
	}
}
