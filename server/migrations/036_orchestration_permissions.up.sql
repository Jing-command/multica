CREATE TABLE repo_permission_policy (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    repository TEXT NOT NULL,
    default_mode TEXT NOT NULL CHECK (default_mode IN ('read', 'write')),
    created_by UUID REFERENCES "user"(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(workspace_id, repository)
);

CREATE INDEX idx_repo_permission_policy_workspace ON repo_permission_policy(workspace_id);

CREATE TABLE child_permission_snapshot (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    child_spec_id UUID NOT NULL REFERENCES child_spec(id) ON DELETE CASCADE,
    repository TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('read', 'write')),
    source_policy_id UUID REFERENCES repo_permission_policy(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_child_permission_snapshot_spec ON child_permission_snapshot(child_spec_id);
