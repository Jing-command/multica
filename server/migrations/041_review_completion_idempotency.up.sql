CREATE TABLE child_review_completion (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    review_round_id UUID NOT NULL UNIQUE REFERENCES child_review_round(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    escalation_id UUID REFERENCES child_escalation(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(child_spec_id, idempotency_key)
);

CREATE INDEX idx_child_review_completion_spec ON child_review_completion(child_spec_id);
