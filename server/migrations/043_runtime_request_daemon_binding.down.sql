DROP INDEX IF EXISTS idx_runtime_update_workspace_daemon_runtime_status_created_at;
ALTER TABLE runtime_update
    DROP CONSTRAINT IF EXISTS runtime_update_workspace_id_fkey,
    DROP COLUMN IF EXISTS workspace_id,
    DROP COLUMN IF EXISTS daemon_id;

DROP INDEX IF EXISTS idx_runtime_ping_workspace_daemon_runtime_status_created_at;
ALTER TABLE runtime_ping
    DROP CONSTRAINT IF EXISTS runtime_ping_workspace_id_fkey,
    DROP COLUMN IF EXISTS workspace_id,
    DROP COLUMN IF EXISTS daemon_id;
