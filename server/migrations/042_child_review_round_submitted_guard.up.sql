CREATE UNIQUE INDEX IF NOT EXISTS idx_child_review_round_one_submitted_per_child
    ON child_review_round(child_spec_id)
    WHERE decision = 'submitted';
