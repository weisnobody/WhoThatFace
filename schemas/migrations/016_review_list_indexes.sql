-- Indexes supporting the people and cluster review list joins and filters.
CREATE INDEX current_face_assignments_person_idx
    ON current_face_assignments(person_id, face_id, state);

CREATE INDEX clusters_proposed_person_idx
    ON clusters(proposed_person_id, clustering_run_id);

CREATE INDEX clustering_runs_status_idx
    ON clustering_runs(status, id);

CREATE INDEX recognition_candidates_face_created_idx
    ON recognition_candidates(face_id, created_at DESC, rank);

CREATE INDEX photo_paths_asset_collection_idx
    ON photo_paths(asset_id, collection_id, missing_at);
