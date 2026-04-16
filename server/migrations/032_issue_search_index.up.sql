-- Enable pg_trgm extension for trigram-based LIKE search acceleration.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- GIN index on issue title for LIKE '%keyword%' queries.
CREATE INDEX idx_issue_title_bigm ON issue USING gin (title gin_trgm_ops);

-- GIN index on issue description (nullable, use COALESCE).
CREATE INDEX idx_issue_description_bigm ON issue USING gin ((COALESCE(description, '')) gin_trgm_ops);
