CREATE TABLE runtime_update_request (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    runtime_id UUID NOT NULL,
    daemon_id TEXT NOT NULL,
    target_version TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'completed', 'failed', 'timeout')),
    output TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    claimed_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (runtime_id, workspace_id, daemon_id)
        REFERENCES agent_runtime(id, workspace_id, daemon_id)
        ON DELETE CASCADE
);

CREATE INDEX idx_runtime_update_request_runtime_created
    ON runtime_update_request(runtime_id, created_at ASC);

CREATE INDEX idx_runtime_update_request_pending
    ON runtime_update_request(created_at ASC)
    WHERE status = 'pending';
