package candidates

import (
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/weisnobody/whothatface/internal/db"
	"github.com/weisnobody/whothatface/internal/recognition"
)

func TestGeneratorFindsCandidatesAndRecordsRun(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "candidates.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	const pipelineID = "pipeline-test"
	if _, err := database.Exec(`INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES (?, 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'CPU', '{}', '2026-01-01T00:00:00Z')`, pipelineID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES ('asset-test', 'hash-test', 1, 'still', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, person := range []struct{ id string }{{"person-test"}, {"person-other"}} {
		if _, err := database.Exec(`INSERT INTO people (id, name, is_provisional, created_at, updated_at) VALUES (?, ?, 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, person.id, person.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO analyses (id, asset_id, pipeline_id, status, source_width, source_height, created_at) VALUES ('analysis-test', 'asset-test', ?, 'completed', 100, 100, '2026-01-01T00:00:00Z')`, pipelineID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, detection_confidence, quality, embedding, embedding_dimension, embedding_dtype, embedding_normalized, created_at) VALUES ('face-target', 'analysis-test', 0, 1, 1, 10, 10, .9, .8, ?, 2, 'float32', 1, '2026-01-01T00:00:00Z'), ('face-reference', 'analysis-test', 1, 1, 1, 10, 10, .95, .9, ?, 2, 'float32', 1, '2026-01-01T00:00:00Z')`, embedding(1, 0), embedding(.9, .1)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO assignment_events (id, face_id, new_state, actor_type, action, reason, created_at) VALUES ('assignment-test', 'face-target', 'unassigned', 'system', 'assignment', 'test', '2026-01-01T00:00:00Z'); INSERT INTO current_face_assignments (face_id, state, event_id, updated_at) VALUES ('face-target', 'unassigned', 'assignment-test', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO reference_selection_events (id, face_id, person_id, reference, reason, created_at) VALUES ('reference-event', 'face-reference', 'person-test', 1, 'test', '2026-01-01T00:00:00Z'); INSERT INTO current_face_references (face_id, person_id, reference, event_id, updated_at) VALUES ('face-reference', 'person-test', 1, 'reference-event', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO assignment_events (id, face_id, person_id, new_state, actor_type, action, reason, created_at) VALUES ('reference-assignment', 'face-reference', 'person-test', 'assigned-user', 'user', 'assignment', 'test', '2026-01-01T00:00:00Z'); INSERT INTO current_face_assignments (face_id, person_id, state, event_id, updated_at) VALUES ('face-reference', 'person-test', 'assigned-user', 'reference-assignment', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	generator := NewGenerator(database, DefaultConfig(), recognition.DefaultThresholds(), pipelineID)
	run, err := generator.Run(ctx, Request{Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "completed" || run.TargetCount != 1 || run.CandidateCount != 1 {
		t.Fatalf("run = %+v", run)
	}
	var personID string
	if err := database.QueryRow(`SELECT person_id FROM possible_face_candidates WHERE run_id = ? AND is_current = 1`, run.ID).Scan(&personID); err != nil {
		t.Fatal(err)
	}
	if personID != "person-test" {
		t.Fatalf("candidate person = %q", personID)
	}
	var eligibility, targetStatus string
	if err := database.QueryRow(`SELECT eligibility_reason, status FROM possible_candidate_targets WHERE run_id = ? AND face_id = 'face-target'`, run.ID).Scan(&eligibility, &targetStatus); err != nil {
		t.Fatal(err)
	}
	if eligibility != "unassigned" || targetStatus != "eligible" {
		t.Fatalf("target = %q/%q", eligibility, targetStatus)
	}
	var auditCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE entity_type = 'candidate-run' AND entity_id = ?`, run.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 {
		t.Fatalf("candidate run audit count = %d, want 3", auditCount)
	}
}

func embedding(first, second float32) []byte {
	result := make([]byte, 8)
	binary.LittleEndian.PutUint32(result[0:4], math.Float32bits(first))
	binary.LittleEndian.PutUint32(result[4:8], math.Float32bits(second))
	return result
}
