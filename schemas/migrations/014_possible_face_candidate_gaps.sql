-- Preserve target eligibility decisions, make duplicate discovery requests
-- atomic, and allow derived face removal to invalidate candidate projections.
CREATE TABLE possible_candidate_targets (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES possible_candidate_runs(id) ON DELETE CASCADE,
    face_id TEXT NOT NULL REFERENCES faces(id),
    eligibility_reason TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('eligible', 'no-candidates', 'skipped')),
    skip_reason TEXT,
    created_at TEXT NOT NULL,
    UNIQUE (run_id, face_id)
);

CREATE INDEX possible_candidate_targets_run_idx
    ON possible_candidate_targets(run_id, status, face_id);
CREATE INDEX possible_candidate_targets_face_idx
    ON possible_candidate_targets(face_id, created_at);

CREATE UNIQUE INDEX jobs_possible_candidates_pending_idx
    ON jobs(kind, payload_json)
    WHERE kind = 'possible-candidates' AND status = 'pending';

-- The face catalog is replaceable during re-analysis. Candidate projections
-- are derived data, so remove them before the referenced face is removed while
-- retaining the run record and its counts for audit.
CREATE TRIGGER possible_candidate_face_delete
BEFORE DELETE ON faces
BEGIN
    DELETE FROM possible_candidate_targets WHERE face_id = OLD.id;
    DELETE FROM possible_face_candidates WHERE face_id = OLD.id;
END;

CREATE TRIGGER possible_candidate_person_delete
BEFORE DELETE ON people
BEGIN
    DELETE FROM possible_face_candidates WHERE person_id = OLD.id;
END;
