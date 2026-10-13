ALTER TABLE jobs ADD COLUMN available_at TEXT;

UPDATE jobs
SET available_at = created_at
WHERE available_at IS NULL;

CREATE INDEX jobs_pending_available_idx ON jobs(status, available_at, priority, created_at);
