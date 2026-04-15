CREATE UNIQUE INDEX IF NOT EXISTS agent_workspace_name_active_idx
ON agent (workspace_id, name)
WHERE archived_at IS NULL;
