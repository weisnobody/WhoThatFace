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
	face := FaceResult{Embedding: &Embedding{Encoding: "base64", DType: "float32", ByteOrder: "little", Normalized: true, Dimension: 2, Data: base64.StdEncoding.EncodeToString(data)}}
	values, err := face.DecodeEmbedding()
	if err != nil {
		t.Fatal(err)
	}
	if values[0] != 1 || values[1] != -0.5 {
		t.Errorf("values = %v", values)
	}
}

func TestDecodeEmbeddingRejectsDimensionMismatch(t *testing.T) {
	face := FaceResult{Embedding: &Embedding{Encoding: "base64", DType: "float32", ByteOrder: "little", Normalized: true, Dimension: 2, Data: base64.StdEncoding.EncodeToString(make([]byte, 4))}}
	if _, err := face.DecodeEmbedding(); err == nil {
		t.Fatal("expected dimension error")
	}
}

func TestDecodeEmbeddingRejectsNonFiniteValues(t *testing.T) {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, 0x7f800000)
	face := FaceResult{Embedding: &Embedding{Encoding: "base64", DType: "float32", ByteOrder: "little", Normalized: true, Dimension: 1, Data: base64.StdEncoding.EncodeToString(data)}}
	if _, err := face.DecodeEmbedding(); err == nil {
		t.Fatal("expected non-finite embedding error")
	}
}
