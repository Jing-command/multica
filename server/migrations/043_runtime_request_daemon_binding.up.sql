ALTER TABLE runtime_ping
    ADD COLUMN workspace_id UUID,
    ADD COLUMN daemon_id TEXT;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM runtime_ping AS rp
        JOIN agent_runtime AS ar ON ar.id = rp.runtime_id
        WHERE ar.daemon_id IS NULL
    ) THEN
        RAISE EXCEPTION 'runtime_ping contains rows for runtimes without daemon bindings';
    END IF;
END $$;

UPDATE runtime_ping AS rp
SET workspace_id = ar.workspace_id,
    daemon_id = ar.daemon_id
FROM agent_runtime AS ar
WHERE ar.id = rp.runtime_id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM runtime_ping
        WHERE workspace_id IS NULL OR daemon_id IS NULL
    ) THEN
        RAISE EXCEPTION 'runtime_ping backfill failed';
    END IF;
END $$;

ALTER TABLE runtime_ping
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN daemon_id SET NOT NULL,
    ADD CONSTRAINT runtime_ping_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

CREATE INDEX idx_runtime_ping_runtime_workspace_daemon_created
    ON runtime_ping(runtime_id, workspace_id, daemon_id, created_at);

ALTER TABLE runtime_update
    ADD COLUMN workspace_id UUID,
    ADD COLUMN daemon_id TEXT;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM runtime_update AS ru
        JOIN agent_runtime AS ar ON ar.id = ru.runtime_id
        WHERE ar.daemon_id IS NULL
    ) THEN
        RAISE EXCEPTION 'runtime_update contains rows for runtimes without daemon bindings';
    END IF;
END $$;

UPDATE runtime_update AS ru
SET workspace_id = ar.workspace_id,
    daemon_id = ar.daemon_id
FROM agent_runtime AS ar
WHERE ar.id = ru.runtime_id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM runtime_update
        WHERE workspace_id IS NULL OR daemon_id IS NULL
    ) THEN
        RAISE EXCEPTION 'runtime_update backfill failed';
    END IF;
END $$;

ALTER TABLE runtime_update
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN daemon_id SET NOT NULL,
    ADD CONSTRAINT runtime_update_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE CASCADE;

CREATE INDEX idx_runtime_update_runtime_workspace_daemon_created
    ON runtime_update(runtime_id, workspace_id, daemon_id, created_at);
