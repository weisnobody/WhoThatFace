CREATE TABLE person_representatives (
    person_id TEXT PRIMARY KEY REFERENCES people(id) ON DELETE CASCADE,
    face_id TEXT NOT NULL REFERENCES faces(id),
    selection_mode TEXT NOT NULL CHECK (selection_mode IN ('automatic', 'explicit')),
    face_quality REAL,
    detection_confidence REAL,
    crop_version TEXT NOT NULL,
    mime_type TEXT NOT NULL CHECK (mime_type IN ('image/jpeg', 'image/png')),
    width INTEGER NOT NULL CHECK (width > 0 AND width <= 96),
    height INTEGER NOT NULL CHECK (height > 0 AND height <= 96),
    image BLOB NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX person_representatives_face_idx ON person_representatives(face_id);
