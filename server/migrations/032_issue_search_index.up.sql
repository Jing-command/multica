-- Enable bigram/trigram search support for LIKE '%keyword%' queries.
-- Prefer pg_bigm when available, but fall back to built-in pg_trgm.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'pg_bigm') THEN
        EXECUTE 'CREATE EXTENSION IF NOT EXISTS pg_bigm';
        EXECUTE 'CREATE INDEX idx_issue_title_bigm ON issue USING gin (title gin_bigm_ops)';
        EXECUTE 'CREATE INDEX idx_issue_description_bigm ON issue USING gin (COALESCE(description, '''') gin_bigm_ops)';
    ELSE
        EXECUTE 'CREATE EXTENSION IF NOT EXISTS pg_trgm';
        EXECUTE 'CREATE INDEX idx_issue_title_bigm ON issue USING gin (title gin_trgm_ops)';
        EXECUTE 'CREATE INDEX idx_issue_description_bigm ON issue USING gin (COALESCE(description, '''') gin_trgm_ops)';
    END IF;
END
$$;
