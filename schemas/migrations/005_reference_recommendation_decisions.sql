CREATE TABLE reference_recommendation_decisions (
    id TEXT PRIMARY KEY,
    recommendation_id TEXT NOT NULL REFERENCES reference_recommendations(id),
    person_id TEXT NOT NULL REFERENCES people(id),
    face_id TEXT NOT NULL REFERENCES faces(id),
    decision TEXT NOT NULL CHECK (decision IN ('approved', 'rejected', 'superseded')),
    account_id TEXT REFERENCES accounts(id),
    created_at TEXT NOT NULL
);

CREATE INDEX reference_recommendation_decisions_recommendation_idx
    ON reference_recommendation_decisions(recommendation_id, created_at);
