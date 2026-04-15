DROP INDEX IF EXISTS idx_plan_revision_child_spec_idempotency;
ALTER TABLE plan_revision
    DROP COLUMN IF EXISTS idempotency_key;

DROP INDEX IF EXISTS idx_child_review_round_child_spec_idempotency;
ALTER TABLE child_review_round
    DROP COLUMN IF EXISTS submission_evidence,
    DROP COLUMN IF EXISTS idempotency_key;
