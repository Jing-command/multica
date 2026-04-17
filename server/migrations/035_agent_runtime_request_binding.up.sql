ALTER TABLE agent_runtime
    ADD CONSTRAINT agent_runtime_id_workspace_daemon_key
    UNIQUE (id, workspace_id, daemon_id);
