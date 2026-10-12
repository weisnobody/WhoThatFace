package representative

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pjg/face_me_bro/internal/db"
	"github.com/pjg/face_me_bro/internal/review"
)

func TestEnsureUsesReferencePriorityAndCreatesBoundedSquare(t *testing.T) {
	root := t.TempDir()
	photoPath := filepath.Join(root, "photo.png")
	photo := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			photo.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 20, A: 255})
		}
	}
	file, err := os.Create(photoPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, photo); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`
		INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection', 'Photos', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, width, height, created_at, updated_at) VALUES ('asset', 'hash', 1, 'still', 200, 100, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('path', 'collection', 'asset', 'photo.png', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 'still');
		INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES ('pipeline', 'test', '1', '{}', '{}', '{}', 1, 'float32', 'l2', 'CPU', '{}', CURRENT_TIMESTAMP);
		INSERT INTO analyses (id, asset_id, pipeline_id, status, source_width, source_height, created_at) VALUES ('analysis', 'asset', 'pipeline', 'completed', 200, 100, CURRENT_TIMESTAMP);
		INSERT INTO people (id, name, created_at, updated_at) VALUES ('person', 'Person', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);`, root)
	if err != nil {
		t.Fatal(err)
	}
	// The collection root is the directory; the path record stores the file name.
	if _, err := database.Exec(`UPDATE collections SET root_path = ? WHERE id = 'collection'`, root); err != nil {
		t.Fatal(err)
	}
	for _, face := range []struct {
		id, state string
		quality   float64
	}{
		{"user", "assigned-user", .99},
		{"reference", "assigned-system", .40},
		{"system", "assigned-system", .80},
	} {
		if _, err := database.Exec(`INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, detection_confidence, quality, created_at) VALUES (?, 'analysis', ?, 10, 20, 30, 40, .8, ?, CURRENT_TIMESTAMP)`, face.id, len(face.id), face.quality); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO assignment_events (id, face_id, person_id, new_state, actor_type, action, created_at) VALUES (?, ?, 'person', ?, 'user', 'test', CURRENT_TIMESTAMP)`, "event-"+face.id, face.id, face.state); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO current_face_assignments (face_id, person_id, state, event_id, updated_at) VALUES (?, 'person', ?, ?, CURRENT_TIMESTAMP)`, face.id, face.state, "event-"+face.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`INSERT INTO reference_selection_events (id, face_id, person_id, reference, reason, created_at) VALUES ('ref-event', 'reference', 'person', 1, 'test', CURRENT_TIMESTAMP); INSERT INTO current_face_references (face_id, person_id, reference, event_id, updated_at) VALUES ('reference', 'person', 1, 'ref-event', CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}

	result, err := NewStore(database).Ensure(context.Background(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if result.FaceID != "reference" || result.SelectionMode != "automatic" {
		t.Fatalf("representative = %#v, want reference automatic", result)
	}
	if result.Width != result.Height || result.Width > 96 || result.MIMEType != "image/png" || len(result.Data) == 0 {
		t.Fatalf("invalid representative image metadata: %#v", result)
	}

	if _, err := NewStore(database).Select(context.Background(), "person", "user", review.Actor{Type: "user"}); err != nil {
		t.Fatal(err)
	}
	selected, err := NewStore(database).Get(context.Background(), "person")
	if err != nil {
		t.Fatal(err)
	}
	if selected.FaceID != "user" || selected.SelectionMode != "explicit" {
		t.Fatalf("explicit representative = %#v", selected)
	}
	if _, err := database.Exec(`DELETE FROM person_representatives`); err != nil {
		t.Fatal(err)
	}
	concurrentContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errors := make(chan error, 8)
	var waitGroup sync.WaitGroup
	for range make([]struct{}, 8) {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := NewStore(database).Ensure(concurrentContext, "person")
			errors <- err
		}()
	}
	waitGroup.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent representative request failed: %v", err)
		}
	}
}
