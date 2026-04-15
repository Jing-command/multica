DROP INDEX IF EXISTS idx_one_pending_task_per_issue;

CREATE UNIQUE INDEX IF NOT EXISTS idx_one_pending_task_per_issue_agent
    ON agent_task_queue (issue_id, agent_id)
    WHERE status IN ('queued', 'dispatched');
