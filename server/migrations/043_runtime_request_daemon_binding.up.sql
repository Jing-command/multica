ALTER TABLE runtime_ping
    ADD COLUMN workspace_id UUID,
    ADD COLUMN daemon_id TEXT;

UPDATE runtime_ping AS rp
SET workspace_id = ar.workspace_id,
    daemon_id = ar.daemon_id
FROM agent_runtime AS ar
WHERE ar.id = rp.runtime_id;

ALTER TABLE runtime_ping
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN daemon_id SET NOT NULL,
    ADD CONSTRAINT runtime_ping_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

CREATE INDEX idx_runtime_ping_workspace_daemon_runtime_status_created_at
    ON runtime_ping(workspace_id, daemon_id, runtime_id, status, created_at);

ALTER TABLE runtime_update
    ADD COLUMN workspace_id UUID,
    ADD COLUMN daemon_id TEXT;

UPDATE runtime_update AS ru
SET workspace_id = ar.workspace_id,
    daemon_id = ar.daemon_id
FROM agent_runtime AS ar
WHERE ar.id = ru.runtime_id;

ALTER TABLE runtime_update
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN daemon_id SET NOT NULL,
    ADD CONSTRAINT runtime_update_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

CREATE INDEX idx_runtime_update_workspace_daemon_runtime_status_created_at
    ON runtime_update(workspace_id, daemon_id, runtime_id, status, created_at);
