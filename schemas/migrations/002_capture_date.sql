-- Store the selected capture date and its provenance for date-range search.
ALTER TABLE photo_assets ADD COLUMN capture_date TEXT;
ALTER TABLE photo_assets ADD COLUMN capture_date_source TEXT
    CHECK (capture_date_source IN ('exif', 'filesystem-created', 'filesystem-modified', 'none'));
