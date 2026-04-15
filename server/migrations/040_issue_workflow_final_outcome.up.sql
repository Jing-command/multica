ALTER TABLE issue
    ADD COLUMN IF NOT EXISTS workflow_final_outcome TEXT
    CHECK (workflow_final_outcome IN ('complete', 'blocked'));
