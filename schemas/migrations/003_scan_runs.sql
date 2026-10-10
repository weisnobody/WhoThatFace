CREATE TABLE scan_runs (
    id TEXT PRIMARY KEY,
    collection_id TEXT NOT NULL REFERENCES collections(id),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'paused', 'completed', 'completed-with-errors', 'failed', 'cancelled')),
    started_at TEXT,
    completed_at TEXT,
    error_message TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE scan_items (
    id TEXT PRIMARY KEY,
    scan_run_id TEXT NOT NULL REFERENCES scan_runs(id),
    relative_path TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('supported', 'unsupported', 'corrupt', 'unstable', 'failed')),
    content_sha256 TEXT,
    error_message TEXT,
    updated_at TEXT NOT NULL,
    UNIQUE (scan_run_id, relative_path)
);

CREATE INDEX scan_runs_collection_idx ON scan_runs(collection_id, created_at);
CREATE INDEX scan_items_run_status_idx ON scan_items(scan_run_id, status);
