-- Optimistic concurrency token for cluster review mutations.
ALTER TABLE clusters ADD COLUMN review_version INTEGER NOT NULL DEFAULT 0 CHECK (review_version >= 0);
