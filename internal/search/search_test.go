package search

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pjg/face_me_bro/internal/db"
)

func TestPhotosSearchesDirectoryAndExcludesDontTrackByDefault(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "search.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('collection', 'Photos', '/photos', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, capture_date, capture_date_source, created_at, updated_at) VALUES ('asset', 'hash', 1, 'still', '2026-01-01T00:00:00Z', 'filesystem-modified', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('path', 'collection', 'asset', 'group/photo.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 'still')`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewStore(database).Photos(context.Background(), Options{RelativePrefix: "group", CaptureDateFrom: "2025-01-01", CaptureDateUntil: "2027-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].RelativePath != "group/photo.jpg" {
		t.Fatalf("result = %+v", result)
	}
}

func TestOnlyConfirmedExcludesAssignedSystem(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "confirmed.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`
		INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('c', 'Photos', '/photos', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES ('a', 'hash-confirmed', 1, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES ('p', 'c', 'a', 'photo.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 'still');
		INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES ('pipe', 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'cpu', '{}', CURRENT_TIMESTAMP);
		INSERT INTO analyses (id, asset_id, pipeline_id, status, source_width, source_height, created_at) VALUES ('an', 'a', 'pipe', 'completed', 10, 10, CURRENT_TIMESTAMP);
		INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, detection_confidence, quality, created_at) VALUES ('f', 'an', 0, 0, 0, 1, 1, .9, .9, CURRENT_TIMESTAMP);
		INSERT INTO people (id, name, created_at, updated_at) VALUES ('person', 'Person', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO assignment_events (id, face_id, person_id, new_state, actor_type, action, created_at) VALUES ('event', 'f', 'person', 'assigned-system', 'system', 'test', CURRENT_TIMESTAMP);
		INSERT INTO current_face_assignments (face_id, person_id, state, event_id, updated_at) VALUES ('f', 'person', 'assigned-system', 'event', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewStore(database).Photos(context.Background(), Options{OnlyConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Fatalf("assigned-system result = %+v, want excluded", result)
	}
	if _, err := database.Exec(`UPDATE current_face_assignments SET state = 'assigned-user' WHERE face_id = 'f'`); err != nil {
		t.Fatal(err)
	}
	result, err = NewStore(database).Photos(context.Background(), Options{OnlyConfirmed: true})
	if err != nil || len(result) != 1 {
		t.Fatalf("assigned-user result = %+v, error=%v", result, err)
	}
}

func TestReviewPhotosSupportsFaceAndPersonFilters(t *testing.T) {
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "review-photos.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	_, err = database.Exec(`
		INSERT INTO collections (id, name, root_path, created_at, updated_at) VALUES ('c', 'Photos', '/photos', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_assets (id, content_sha256, byte_size, media_kind, created_at, updated_at) VALUES
			('a-with-face', 'hash-with-face', 1, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			('a-without-face', 'hash-without-face', 1, 'still', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO photo_paths (id, collection_id, asset_id, relative_path, first_seen_at, last_seen_at, file_size, media_kind) VALUES
			('p-with-face', 'c', 'a-with-face', 'with-face.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 'still'),
			('p-without-face', 'c', 'a-without-face', 'without-face.jpg', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 1, 'still');
		INSERT INTO pipelines (id, model_family, model_version, model_checksums_json, detector_config_json, preprocessing_config_json, embedding_dimension, embedding_dtype, normalization, execution_provider, dependency_versions_json, created_at) VALUES
			('pipe', 'test', '1', '{}', '{}', '{}', 2, 'float32', 'l2', 'cpu', '{}', CURRENT_TIMESTAMP);
		INSERT INTO analyses (id, asset_id, pipeline_id, status, source_width, source_height, created_at) VALUES
			('analysis', 'a-with-face', 'pipe', 'completed', 10, 10, CURRENT_TIMESTAMP);
		INSERT INTO faces (id, analysis_id, face_index, box_left, box_top, box_right, box_bottom, detection_confidence, quality, created_at) VALUES
			('face', 'analysis', 0, 0, 0, 1, 1, .9, .9, CURRENT_TIMESTAMP);
		INSERT INTO people (id, name, created_at, updated_at) VALUES ('person', 'Alice Example', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
		INSERT INTO assignment_events (id, face_id, person_id, new_state, actor_type, action, created_at) VALUES ('assignment', 'face', 'person', 'assigned-user', 'user', 'test', CURRENT_TIMESTAMP);
		INSERT INTO current_face_assignments (face_id, person_id, state, event_id, updated_at) VALUES ('face', 'person', 'assigned-user', 'assignment', CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}

	store := NewStore(database)
	result, err := store.ReviewPhotos(context.Background(), ReviewPhotoOptions{FaceMode: "with", AssignmentStates: []string{"assigned-user"}, PersonQuery: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].PathID != "p-with-face" || len(result[0].Faces) != 1 || result[0].AssignedCount != 1 || result[0].NeedsReviewCount != 0 {
		t.Fatalf("filtered review photos = %+v", result)
	}

	result, err = store.ReviewPhotos(context.Background(), ReviewPhotoOptions{FaceMode: "without"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].PathID != "p-without-face" {
		t.Fatalf("no-face review photos = %+v", result)
	}

	page, err := store.ReviewPhotosPage(context.Background(), ReviewPhotoOptions{Sort: "path", Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Photos) != 1 || page.Photos[0].PathID != "p-without-face" {
		t.Fatalf("paged review photos = %+v", page)
	}
	page, err = store.ReviewPhotosPage(context.Background(), ReviewPhotoOptions{Sort: "path", Limit: 1, Offset: 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Photos) != 1 || page.Photos[0].PathID != "p-without-face" {
		t.Fatalf("clamped review photos = %+v", page)
	}
}
