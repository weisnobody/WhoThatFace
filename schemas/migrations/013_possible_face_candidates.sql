CREATE TABLE possible_candidate_runs (
    id TEXT PRIMARY KEY,
    scope TEXT NOT NULL CHECK (scope IN ('person', 'collection', 'all')),
    scope_id TEXT,
    policy_json TEXT NOT NULL,
    pipeline_id TEXT,
    reference_set_version TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'cancelled', 'failed')),
    target_count INTEGER NOT NULL DEFAULT 0,
    candidate_count INTEGER NOT NULL DEFAULT 0,
    skipped_count INTEGER NOT NULL DEFAULT 0,
    error_message TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    FOREIGN KEY (pipeline_id) REFERENCES pipelines(id)
);

CREATE TABLE possible_face_candidates (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES possible_candidate_runs(id),
    face_id TEXT NOT NULL REFERENCES faces(id),
    person_id TEXT NOT NULL REFERENCES people(id),
    rank INTEGER NOT NULL CHECK (rank > 0),
    similarity REAL NOT NULL,
    margin REAL NOT NULL,
    support_count INTEGER NOT NULL,
    quality REAL NOT NULL,
    detection_confidence REAL NOT NULL,
    evidence TEXT NOT NULL,
    retention_reason TEXT NOT NULL,
    is_current INTEGER NOT NULL CHECK (is_current IN (0, 1)),
    created_at TEXT NOT NULL,
    UNIQUE (run_id, face_id, person_id)
);

CREATE INDEX possible_candidate_runs_scope_idx
    ON possible_candidate_runs(scope, scope_id, created_at);
CREATE INDEX possible_candidate_runs_status_idx
    ON possible_candidate_runs(status, created_at);
CREATE INDEX possible_face_candidates_face_idx
    ON possible_face_candidates(face_id, is_current, rank);
CREATE INDEX possible_face_candidates_person_idx
    ON possible_face_candidates(person_id, is_current, rank);
CREATE INDEX possible_face_candidates_run_idx
    ON possible_face_candidates(run_id, rank);
