-- GIN index on comment content for LIKE '%keyword%' queries (pg_trgm).
CREATE INDEX idx_comment_content_bigm ON comment USING gin (content gin_trgm_ops);
