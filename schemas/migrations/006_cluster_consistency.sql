ALTER TABLE clusters ADD COLUMN consistency REAL CHECK (consistency BETWEEN 0.0 AND 1.0);
