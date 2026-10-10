-- Initial face_me_bro schema.
-- Migration files are immutable after application.

PRAGMA foreign_keys = ON;

CREATE TABLE schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE pipelines (
    id TEXT PRIMARY KEY,
    model_family TEXT NOT NULL,
    model_version TEXT NOT NULL,
    model_checksums_json TEXT NOT NULL,
    detector_config_json TEXT NOT NULL,
    preprocessing_config_json TEXT NOT NULL,
    embedding_dimension INTEGER NOT NULL CHECK (embedding_dimension > 0),
    embedding_dtype TEXT NOT NULL,
    normalization TEXT NOT NULL,
    execution_provider TEXT NOT NULL,
    dependency_versions_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE collections (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    root_path TEXT NOT NULL UNIQUE,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE photo_assets (
    id TEXT PRIMARY KEY,
    content_sha256 TEXT NOT NULL UNIQUE,
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    media_kind TEXT NOT NULL CHECK (media_kind IN ('still', 'live_photo', 'motion_photo')),
    width INTEGER,
    height INTEGER,
    exif_capture_date TEXT,
    exif_metadata_json TEXT,
    motion_metadata_json TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE photo_paths (
    id TEXT PRIMARY KEY,
    collection_id TEXT NOT NULL REFERENCES collections(id),
    asset_id TEXT NOT NULL REFERENCES photo_assets(id),
    relative_path TEXT NOT NULL,
    filesystem_created_at TEXT,
    filesystem_modified_at TEXT,
    first_seen_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    missing_at TEXT,
    file_size INTEGER NOT NULL CHECK (file_size >= 0),
    media_kind TEXT NOT NULL CHECK (media_kind IN ('still', 'live_photo', 'motion_photo')),
    motion_metadata_json TEXT,
    UNIQUE (collection_id, relative_path)
);

CREATE INDEX photo_paths_collection_path_idx
    ON photo_paths(collection_id, relative_path);
CREATE INDEX photo_paths_asset_idx ON photo_paths(asset_id);
CREATE INDEX photo_paths_missing_idx ON photo_paths(collection_id, missing_at);

CREATE TABLE analyses (
    id TEXT PRIMARY KEY,
    asset_id TEXT NOT NULL REFERENCES photo_assets(id),
    pipeline_id TEXT NOT NULL REFERENCES pipelines(id),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'completed_with_errors', 'failed')),
    source_width INTEGER,
    source_height INTEGER,
    inference_width INTEGER,
    inference_height INTEGER,
    error_code TEXT,
    error_message TEXT,
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL,
    UNIQUE (asset_id, pipeline_id)
);

CREATE INDEX analyses_asset_idx ON analyses(asset_id);

CREATE TABLE faces (
    id TEXT PRIMARY KEY,
    analysis_id TEXT NOT NULL REFERENCES analyses(id),
    face_index INTEGER NOT NULL CHECK (face_index >= 0),
    box_left REAL NOT NULL,
    box_top REAL NOT NULL,
    box_right REAL NOT NULL,
    box_bottom REAL NOT NULL,
    detection_confidence REAL CHECK (detection_confidence BETWEEN 0.0 AND 1.0),
    quality REAL CHECK (quality BETWEEN 0.0 AND 1.0),
    embedding BLOB,
    embedding_dimension INTEGER CHECK (embedding_dimension > 0),
    embedding_dtype TEXT,
    embedding_normalized INTEGER CHECK (embedding_normalized IN (0, 1)),
    created_at TEXT NOT NULL,
    UNIQUE (analysis_id, face_index),
    CHECK (box_right >= box_left),
    CHECK (box_bottom >= box_top)
);

CREATE INDEX faces_analysis_idx ON faces(analysis_id);

CREATE TABLE face_correspondences (
    id TEXT PRIMARY KEY,
    old_face_id TEXT NOT NULL REFERENCES faces(id),
    new_face_id TEXT NOT NULL REFERENCES faces(id),
    confidence REAL NOT NULL CHECK (confidence BETWEEN 0.0 AND 1.0),
    method TEXT NOT NULL,
    is_ambiguous INTEGER NOT NULL DEFAULT 0 CHECK (is_ambiguous IN (0, 1)),
    created_at TEXT NOT NULL,
    UNIQUE (old_face_id, new_face_id)
);

CREATE TABLE people (
    id TEXT PRIMARY KEY,
    name TEXT,
    is_provisional INTEGER NOT NULL DEFAULT 0 CHECK (is_provisional IN (0, 1)),
    dont_track INTEGER NOT NULL DEFAULT 0 CHECK (dont_track IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX people_named_name_idx
    ON people(name) WHERE name IS NOT NULL;

CREATE TABLE accounts (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('system-admin', 'collection-manager', 'reviewer', 'viewer')),
    disabled_at TEXT,
    must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1)),
    failed_login_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_login_count >= 0),
    failed_login_window_started_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE collection_permissions (
    account_id TEXT NOT NULL REFERENCES accounts(id),
    collection_id TEXT NOT NULL REFERENCES collections(id),
    can_view INTEGER NOT NULL DEFAULT 0 CHECK (can_view IN (0, 1)),
    can_modify INTEGER NOT NULL DEFAULT 0 CHECK (can_modify IN (0, 1)),
    can_undo INTEGER NOT NULL DEFAULT 1 CHECK (can_undo IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (account_id, collection_id)
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts(id),
    token_hash BLOB NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    logged_out_at TEXT
);

CREATE INDEX sessions_account_idx ON sessions(account_id, expires_at);

CREATE TABLE clustering_runs (
    id TEXT PRIMARY KEY,
    algorithm TEXT NOT NULL,
    algorithm_version TEXT NOT NULL,
    k INTEGER NOT NULL CHECK (k > 0),
    similarity_threshold REAL NOT NULL CHECK (similarity_threshold BETWEEN 0.0 AND 1.0),
    neighbor_search_method TEXT NOT NULL,
    mutual_edge_policy TEXT NOT NULL,
    candidate_scope TEXT NOT NULL CHECK (candidate_scope IN ('all', 'unresolved-only', 'subset')),
    input_face_set_json TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'completed', 'active', 'superseded', 'failed', 'cancelled')),
    started_at TEXT,
    completed_at TEXT,
    accepted_at TEXT,
    superseded_by TEXT REFERENCES clustering_runs(id),
    created_at TEXT NOT NULL
);

CREATE TABLE clusters (
    id TEXT PRIMARY KEY,
    clustering_run_id TEXT NOT NULL REFERENCES clustering_runs(id),
    cluster_key TEXT NOT NULL,
    proposed_person_id TEXT REFERENCES people(id),
    purity REAL CHECK (purity BETWEEN 0.0 AND 1.0),
    connectivity REAL CHECK (connectivity BETWEEN 0.0 AND 1.0),
    is_mixed INTEGER NOT NULL DEFAULT 0 CHECK (is_mixed IN (0, 1)),
    review_status TEXT NOT NULL CHECK (review_status IN ('unreviewed', 'in-progress', 'partially-reviewed', 'reviewed', 'needs-review', 'superseded')),
    created_at TEXT NOT NULL,
    UNIQUE (clustering_run_id, cluster_key)
);

CREATE INDEX clusters_run_review_idx ON clusters(clustering_run_id, review_status);

CREATE TABLE cluster_memberships (
    cluster_id TEXT NOT NULL REFERENCES clusters(id),
    face_id TEXT NOT NULL REFERENCES faces(id),
    PRIMARY KEY (cluster_id, face_id)
);

CREATE INDEX cluster_memberships_face_idx ON cluster_memberships(face_id);

CREATE TABLE assignment_events (
    id TEXT PRIMARY KEY,
    face_id TEXT NOT NULL REFERENCES faces(id),
    person_id TEXT REFERENCES people(id),
    previous_state TEXT,
    previous_person_id TEXT REFERENCES people(id),
    new_state TEXT NOT NULL CHECK (new_state IN ('unassigned', 'provisional', 'assigned-system', 'assigned-user', 'unknown', 'ambiguous')),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('system', 'user')),
    account_id TEXT REFERENCES accounts(id),
    source_pipeline_id TEXT REFERENCES pipelines(id),
    clustering_run_id TEXT REFERENCES clustering_runs(id),
    cluster_id TEXT REFERENCES clusters(id),
    similarity REAL,
    margin REAL,
    support_count INTEGER,
    quality REAL,
    detection_confidence REAL,
    purity REAL,
    connectivity REAL,
    agreement INTEGER CHECK (agreement IN (0, 1)),
    action TEXT NOT NULL,
    reason TEXT,
    supersedes_event_id TEXT REFERENCES assignment_events(id),
    created_at TEXT NOT NULL
);

CREATE TABLE current_face_assignments (
    face_id TEXT PRIMARY KEY REFERENCES faces(id),
    person_id TEXT REFERENCES people(id),
    state TEXT NOT NULL CHECK (state IN ('unassigned', 'provisional', 'assigned-system', 'assigned-user', 'unknown', 'ambiguous')),
    event_id TEXT NOT NULL REFERENCES assignment_events(id),
    updated_at TEXT NOT NULL
);

CREATE INDEX current_face_assignments_state_idx ON current_face_assignments(state, person_id);

CREATE TABLE face_disposition_events (
    id TEXT PRIMARY KEY,
    face_id TEXT NOT NULL REFERENCES faces(id),
    disposition TEXT NOT NULL CHECK (disposition IN ('incorrect-assignment', 'false-detection', 'dont-track')),
    active INTEGER NOT NULL CHECK (active IN (0, 1)),
    account_id TEXT REFERENCES accounts(id),
    reason TEXT,
    supersedes_event_id TEXT REFERENCES face_disposition_events(id),
    created_at TEXT NOT NULL
);

CREATE TABLE current_face_dispositions (
    face_id TEXT PRIMARY KEY REFERENCES faces(id),
    disposition TEXT NOT NULL CHECK (disposition IN ('incorrect-assignment', 'false-detection', 'dont-track')),
    event_id TEXT NOT NULL REFERENCES face_disposition_events(id),
    updated_at TEXT NOT NULL
);

CREATE TABLE person_state_events (
    id TEXT PRIMARY KEY,
    person_id TEXT NOT NULL REFERENCES people(id),
    dont_track INTEGER NOT NULL CHECK (dont_track IN (0, 1)),
    account_id TEXT REFERENCES accounts(id),
    reason TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE reference_recommendations (
    id TEXT PRIMARY KEY,
    face_id TEXT NOT NULL REFERENCES faces(id),
    person_id TEXT NOT NULL REFERENCES people(id),
    recommendation_run_id TEXT NOT NULL,
    is_current INTEGER NOT NULL CHECK (is_current IN (0, 1)),
    recommendation_score REAL,
    quality REAL,
    detection_confidence REAL,
    similarity REAL,
    margin REAL,
    support_count INTEGER,
    purity REAL,
    connectivity REAL,
    diversity_reason TEXT,
    duplicate_reason TEXT,
    reference_set_version TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX reference_recommendations_person_current_idx
    ON reference_recommendations(person_id, is_current, recommendation_score);

CREATE TABLE reference_selection_events (
    id TEXT PRIMARY KEY,
    face_id TEXT NOT NULL REFERENCES faces(id),
    person_id TEXT NOT NULL REFERENCES people(id),
    reference INTEGER NOT NULL CHECK (reference IN (0, 1)),
    account_id TEXT REFERENCES accounts(id),
    recommendation_id TEXT REFERENCES reference_recommendations(id),
    reason TEXT,
    supersedes_event_id TEXT REFERENCES reference_selection_events(id),
    created_at TEXT NOT NULL
);

CREATE TABLE current_face_references (
    face_id TEXT PRIMARY KEY REFERENCES faces(id),
    person_id TEXT NOT NULL REFERENCES people(id),
    reference INTEGER NOT NULL CHECK (reference IN (0, 1)),
    event_id TEXT NOT NULL REFERENCES reference_selection_events(id),
    updated_at TEXT NOT NULL
);

CREATE TABLE cluster_review_events (
    id TEXT PRIMARY KEY,
    cluster_id TEXT NOT NULL REFERENCES clusters(id),
    account_id TEXT REFERENCES accounts(id),
    status TEXT NOT NULL CHECK (status IN ('unreviewed', 'in-progress', 'partially-reviewed', 'reviewed', 'needs-review', 'superseded')),
    default_person_id TEXT REFERENCES people(id),
    note TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 5,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'paused', 'cancelling', 'cancelled', 'completed', 'completed-with-errors', 'failed')),
    payload_json TEXT NOT NULL,
    progress_json TEXT,
    error_code TEXT,
    error_message TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT
);

CREATE TABLE job_items (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs(id),
    item_key TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed', 'skipped')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    error_code TEXT,
    error_message TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE (job_id, item_key)
);

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    account_id TEXT REFERENCES accounts(id),
    action TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    previous_json TEXT,
    new_json TEXT,
    conflict INTEGER NOT NULL DEFAULT 0 CHECK (conflict IN (0, 1)),
    created_at TEXT NOT NULL
);

CREATE INDEX audit_events_created_idx ON audit_events(created_at);
CREATE INDEX audit_events_entity_idx ON audit_events(entity_type, entity_id);
