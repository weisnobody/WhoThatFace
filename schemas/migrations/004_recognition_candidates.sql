CREATE TABLE recognition_candidates (
    id TEXT PRIMARY KEY,
    face_id TEXT NOT NULL REFERENCES faces(id),
    person_id TEXT NOT NULL REFERENCES people(id),
    rank INTEGER NOT NULL CHECK (rank > 0),
    similarity REAL NOT NULL,
    margin REAL NOT NULL,
    support_count INTEGER NOT NULL CHECK (support_count >= 0),
    quality REAL NOT NULL,
    detection_confidence REAL NOT NULL,
    evidence TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX recognition_candidates_face_idx ON recognition_candidates(face_id, rank);
