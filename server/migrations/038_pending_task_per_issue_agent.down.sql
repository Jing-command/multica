DELETE FROM agent_task_queue a
USING agent_task_queue b
WHERE a.ctid < b.ctid
  AND a.issue_id = b.issue_id
  AND a.agent_id <> b.agent_id
  AND a.status IN ('queued', 'dispatched')
  AND b.status IN ('queued', 'dispatched');

DROP INDEX IF EXISTS idx_one_pending_task_per_issue_agent;

CREATE UNIQUE INDEX IF NOT EXISTS idx_one_pending_task_per_issue
    ON agent_task_queue (issue_id)
    WHERE status IN ('queued', 'dispatched');
