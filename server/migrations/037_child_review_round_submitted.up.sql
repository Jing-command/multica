ALTER TABLE child_review_round DROP CONSTRAINT IF EXISTS child_review_round_decision_check;

ALTER TABLE child_review_round
    ADD CONSTRAINT child_review_round_decision_check
    CHECK (decision IN ('submitted', 'approved', 'changes_requested', 'blocked'));

ALTER TABLE child_review_round
    ALTER COLUMN reviewer_agent_id DROP NOT NULL;
