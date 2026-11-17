-- Distinguish an operator pause from a system interruption that requires
-- resolving an external issue before the job is resumed.
ALTER TABLE jobs ADD COLUMN pause_reason TEXT NOT NULL DEFAULT 'none'
    CHECK (pause_reason IN ('none', 'user', 'system'));
