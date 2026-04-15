ALTER TABLE child_review_round
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT,
    ADD COLUMN IF NOT EXISTS submission_evidence JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE UNIQUE INDEX IF NOT EXISTS idx_child_review_round_child_spec_idempotency
    ON child_review_round(child_spec_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

ALTER TABLE plan_revision
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_plan_revision_child_spec_idempotency
    ON plan_revision(child_spec_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
