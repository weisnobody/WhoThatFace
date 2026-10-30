-- Link audit records to the immutable event they can compensate.
ALTER TABLE audit_events ADD COLUMN target_event_id TEXT;
CREATE INDEX audit_events_target_idx ON audit_events(target_event_id);
