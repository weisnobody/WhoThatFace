package recognition

import (
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/weisnobody/whothatface/internal/db"
)

func TestDefaultThresholdsDirectAssignmentSimilarity(t *testing.T) {
	if got := DefaultThresholds().DirectAssignmentSimilarity; got != 0.75 {
		t.Fatalf("direct assignment similarity = %v, want 0.75", got)
	}
}

func TestClassifyStandaloneIgnoresClusterMetrics(t *testing.T) {
	candidate := Candidate{Similarity: .8, Margin: .1, Support: 6, Quality: .8, Detection: .8, Purity: 0, Connectivity: 0}
	if got := ClassifyStandalone(candidate, DefaultThresholds()); got != EvidenceHigh {
		t.Fatalf("level = %q", got)
	}
}

func TestProcessDiscoveryDirectlyAssignsUnrelatedFacesAndProtectsAssignedFaces(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "recognition.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	const pipelineID = "pipeline-test"
	if _, err := database.Exec(`INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES (?, 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'CPU', '{}', '2026-01-01T00:00:00Z')`, pipelineID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES ('asset-test', 'hash-test', 1, 'still', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'); INSERT INTO analyses (id, asset_id, pipeline_id, status, source_width, source_height, created_at) VALUES ('analysis-test', 'asset-test', ?, 'completed', 100, 100, '2026-01-01T00:00:00Z')`, pipelineID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO people (id, name, is_provisional, created_at, updated_at) VALUES ('person-target', 'Target', 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'), ('person-other', 'Other', 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	faces := []struct {
		id        string
		index     int
		embedding []byte
		state     string
		personID  string
	}{
		{"face-reference", 0, testEmbedding(1, 0), "assigned-user", "person-target"},
		{"face-unrelated-high", 1, testEmbedding(1, 0), "unknown", ""},
		{"face-unrelated-equal", 2, testEmbedding(.75, float32(math.Sqrt(1-.75*.75))), "ambiguous", ""},
		{"face-protected-user", 3, testEmbedding(1, 0), "assigned-user", "person-other"},
		{"face-protected-system", 4, testEmbedding(1, 0), "assigned-system", "person-other"},
	}
	for _, face := range faces {
		if _, err := database.Exec(`INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, detection_confidence, quality, embedding, embedding_dimension, embedding_dtype, embedding_normalized, created_at) VALUES (?, 'analysis-test', ?, 1, 1, 10, 10, .9, .9, ?, 2, 'float32', 1, '2026-01-01T00:00:00Z')`, face.id, face.index, face.embedding); err != nil {
			t.Fatal(err)
		}
		eventID := face.id + "-event"
		if _, err := database.Exec(`INSERT INTO assignment_events (id, face_id, person_id, new_state, actor_type, action, reason, created_at) VALUES (?, ?, NULLIF(?, ''), ?, 'system', 'assignment', 'test', '2026-01-01T00:00:00Z')`, eventID, face.id, face.personID, face.state); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO current_face_assignments (face_id, person_id, state, event_id, updated_at) VALUES (?, NULLIF(?, ''), ?, ?, '2026-01-01T00:00:00Z')`, face.id, face.personID, face.state, eventID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO reference_selection_events (id, face_id, person_id, reference, reason, created_at) VALUES ('reference-event', 'face-reference', 'person-target', 1, 'test', '2026-01-01T00:00:00Z'); INSERT INTO current_face_references (face_id, person_id, reference, event_id, updated_at) VALUES ('face-reference', 'person-target', 1, 'reference-event', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	if err := NewProcessor(database, DefaultThresholds(), pipelineID).ProcessDiscovery(ctx, "person-target", nil); err != nil {
		t.Fatal(err)
	}
	for _, faceID := range []string{"face-unrelated-high", "face-unrelated-equal"} {
		var personID, state string
		if err := database.QueryRow(`SELECT COALESCE(person_id, ''), state FROM current_face_assignments WHERE face_id = ?`, faceID).Scan(&personID, &state); err != nil {
			t.Fatal(err)
		}
		if personID != "person-target" || state != "assigned-system" {
			t.Fatalf("%s assignment = %q/%q, want person-target/assigned-system", faceID, personID, state)
		}
		var reason string
		if err := database.QueryRow(`SELECT reason FROM assignment_events WHERE face_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, faceID).Scan(&reason); err != nil {
			t.Fatal(err)
		}
		if reason != "direct-similarity" {
			t.Fatalf("%s reason = %q, want direct-similarity", faceID, reason)
		}
	}
	for _, faceID := range []string{"face-protected-user", "face-protected-system"} {
		var personID, state string
		if err := database.QueryRow(`SELECT COALESCE(person_id, ''), state FROM current_face_assignments WHERE face_id = ?`, faceID).Scan(&personID, &state); err != nil {
			t.Fatal(err)
		}
		if personID != "person-other" || (faceID == "face-protected-user" && state != "assigned-user") || (faceID == "face-protected-system" && state != "assigned-system") {
			t.Fatalf("%s assignment changed to %q/%q", faceID, personID, state)
		}
	}
}

func testEmbedding(first, second float32) []byte {
	result := make([]byte, 8)
	binary.LittleEndian.PutUint32(result[0:4], math.Float32bits(first))
	binary.LittleEndian.PutUint32(result[4:8], math.Float32bits(second))
	return result
}
