CREATE TABLE child_spec (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    parent_issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    child_issue_id UUID NOT NULL UNIQUE REFERENCES issue(id) ON DELETE CASCADE,
    worker_agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    orchestrator_agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'in_progress', 'awaiting_review', 'done', 'blocked')),
    max_review_rounds INT NOT NULL DEFAULT 1 CHECK (max_review_rounds >= 1),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_child_spec_workspace ON child_spec(workspace_id);
CREATE INDEX idx_child_spec_parent_issue ON child_spec(parent_issue_id);

CREATE TABLE child_acceptance_criteria (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    ordinal INT NOT NULL,
    criterion_text TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(child_spec_id, ordinal)
);

CREATE INDEX idx_child_acceptance_criteria_spec ON child_acceptance_criteria(child_spec_id);

CREATE TABLE child_review_round (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    round_number INT NOT NULL,
    reviewer_agent_id UUID REFERENCES agent(id) ON DELETE CASCADE,
    decision TEXT NOT NULL CHECK (decision IN ('submitted', 'approved', 'changes_requested', 'blocked')),
    summary TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(child_spec_id, round_number)
);

CREATE INDEX idx_child_review_round_spec ON child_review_round(child_spec_id);
CREATE UNIQUE INDEX idx_child_review_round_one_submitted_per_child
    ON child_review_round(child_spec_id)
    WHERE decision = 'submitted';

CREATE TABLE child_review_criterion_result (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    review_round_id UUID NOT NULL REFERENCES child_review_round(id) ON DELETE CASCADE,
    criterion_id UUID NOT NULL REFERENCES child_acceptance_criteria(id) ON DELETE CASCADE,
    result TEXT NOT NULL CHECK (result IN ('pass', 'fail', 'not_applicable')),
    note TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(review_round_id, criterion_id)
);

CREATE INDEX idx_child_review_criterion_result_round ON child_review_criterion_result(review_round_id);

CREATE TABLE child_escalation (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    raised_by_agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    reason TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ
);

CREATE INDEX idx_child_escalation_spec ON child_escalation(child_spec_id);

CREATE TABLE plan_revision (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    revision_number INT NOT NULL,
    requested_by_agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    reason TEXT,
    plan_content TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(child_spec_id, revision_number)
);

CREATE INDEX idx_plan_revision_spec ON plan_revision(child_spec_id);
