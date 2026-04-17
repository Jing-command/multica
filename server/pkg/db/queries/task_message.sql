-- name: CreateTaskMessage :one
INSERT INTO task_message (task_id, seq, type, tool, content, input, output)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: ListTaskMessages :many
SELECT * FROM task_message
WHERE task_id = $1
ORDER BY seq ASC;

-- name: ListTaskMessagesForDaemonScope :many
SELECT tm.* FROM task_message tm
JOIN agent_task_queue atq ON atq.id = tm.task_id
JOIN agent_runtime ar ON ar.id = atq.runtime_id
WHERE tm.task_id = $1 AND ar.workspace_id = $2 AND ar.daemon_id = $3
ORDER BY tm.seq ASC;

-- name: ListTaskMessagesSince :many
SELECT * FROM task_message
WHERE task_id = $1 AND seq > $2
ORDER BY seq ASC;

-- name: ListTaskMessagesSinceForDaemonScope :many
SELECT tm.* FROM task_message tm
JOIN agent_task_queue atq ON atq.id = tm.task_id
JOIN agent_runtime ar ON ar.id = atq.runtime_id
WHERE tm.task_id = $1 AND tm.seq > $2 AND ar.workspace_id = $3 AND ar.daemon_id = $4
ORDER BY tm.seq ASC;

-- name: DeleteTaskMessages :exec
DELETE FROM task_message
WHERE task_id = $1;
