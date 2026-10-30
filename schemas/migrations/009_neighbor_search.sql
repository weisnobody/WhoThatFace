ALTER TABLE collections ADD COLUMN neighbor_search_method TEXT NOT NULL DEFAULT 'exact'
    CHECK (neighbor_search_method IN ('exact', 'ann'));
