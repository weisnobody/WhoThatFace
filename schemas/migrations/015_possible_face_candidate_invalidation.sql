-- Preserve candidate history when a referenced face or person is removed.
DROP TRIGGER IF EXISTS possible_candidate_face_delete;
DROP TRIGGER IF EXISTS possible_candidate_person_delete;

CREATE TABLE possible_face_candidates_rebuild (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES possible_candidate_runs(id),
    face_id TEXT,
    person_id TEXT,
    rank INTEGER NOT NULL CHECK (rank > 0),
    similarity REAL NOT NULL,
    margin REAL NOT NULL,
    support_count INTEGER NOT NULL,
    quality REAL NOT NULL,
    detection_confidence REAL NOT NULL,
    evidence TEXT NOT NULL,
    retention_reason TEXT NOT NULL,
    is_current INTEGER NOT NULL CHECK (is_current IN (0, 1)),
    invalidated_at TEXT,
    invalidated_reason TEXT,
    created_at TEXT NOT NULL,
    UNIQUE (run_id, face_id, person_id)
);

INSERT INTO possible_face_candidates_rebuild
    (id, run_id, face_id, person_id, rank, similarity, margin, support_count,
     quality, detection_confidence, evidence, retention_reason, is_current,
     created_at)
SELECT id, run_id, face_id, person_id, rank, similarity, margin, support_count,
       quality, detection_confidence, evidence, retention_reason, is_current,
       created_at
FROM possible_face_candidates;

DROP TABLE possible_face_candidates;
ALTER TABLE possible_face_candidates_rebuild RENAME TO possible_face_candidates;

CREATE INDEX possible_face_candidates_face_idx
    ON possible_face_candidates(face_id, is_current, rank);
CREATE INDEX possible_face_candidates_person_idx
    ON possible_face_candidates(person_id, is_current, rank);
CREATE INDEX possible_face_candidates_run_idx
    ON possible_face_candidates(run_id, rank);

CREATE TABLE possible_candidate_targets_rebuild (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES possible_candidate_runs(id) ON DELETE CASCADE,
    face_id TEXT,
    eligibility_reason TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('eligible', 'no-candidates', 'skipped')),
    skip_reason TEXT,
    invalidated_at TEXT,
    invalidated_reason TEXT,
    created_at TEXT NOT NULL,
    UNIQUE (run_id, face_id)
);

INSERT INTO possible_candidate_targets_rebuild
    (id, run_id, face_id, eligibility_reason, status, skip_reason, created_at)
SELECT id, run_id, face_id, eligibility_reason, status, skip_reason, created_at
FROM possible_candidate_targets;

DROP TABLE possible_candidate_targets;
ALTER TABLE possible_candidate_targets_rebuild RENAME TO possible_candidate_targets;

CREATE INDEX possible_candidate_targets_run_idx
    ON possible_candidate_targets(run_id, status, face_id);
CREATE INDEX possible_candidate_targets_face_idx
    ON possible_candidate_targets(face_id, created_at);

CREATE TRIGGER possible_candidate_face_delete
BEFORE DELETE ON faces
BEGIN
    UPDATE possible_face_candidates
    SET face_id = NULL, is_current = 0,
        invalidated_at = CURRENT_TIMESTAMP,
        invalidated_reason = 'face-removed'
    WHERE face_id = OLD.id;
    UPDATE possible_candidate_targets
    SET face_id = NULL, status = 'skipped',
        invalidated_at = CURRENT_TIMESTAMP,
        invalidated_reason = 'face-removed',
        skip_reason = 'face-removed'
    WHERE face_id = OLD.id;
END;

CREATE TRIGGER possible_candidate_person_delete
BEFORE DELETE ON people
BEGIN
    UPDATE possible_face_candidates
    SET person_id = NULL, is_current = 0,
        invalidated_at = CURRENT_TIMESTAMP,
        invalidated_reason = 'person-removed'
    WHERE person_id = OLD.id;
END;
