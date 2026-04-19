-- name: CreateRuntimePing :one
INSERT INTO runtime_ping (runtime_id, workspace_id, daemon_id, status)
VALUES ($1, $2, $3, 'pending')
RETURNING *;

-- name: GetRuntimePing :one
SELECT * FROM runtime_ping
WHERE id = $1;

-- name: GetRuntimePingForWorkspace :one
SELECT * FROM runtime_ping
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3;

-- name: GetRuntimePingForDaemon :one
SELECT * FROM runtime_ping
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4;

-- name: PopPendingRuntimePing :many
WITH next_ping AS (
    SELECT id
    FROM runtime_ping
    WHERE runtime_ping.runtime_id = $1 AND runtime_ping.status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE runtime_ping
SET status = 'running', updated_at = now()
WHERE runtime_ping.id = (SELECT id FROM next_ping)
RETURNING *;

-- name: PopPendingRuntimePingForDaemon :many
WITH next_ping AS (
    SELECT id
    FROM runtime_ping
    WHERE runtime_ping.runtime_id = $1
      AND runtime_ping.workspace_id = $2
      AND runtime_ping.daemon_id = $3
      AND runtime_ping.status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE runtime_ping
SET status = 'running', updated_at = now()
WHERE runtime_ping.id = (SELECT id FROM next_ping)
RETURNING *;

-- name: SetRuntimePingCompleted :one
UPDATE runtime_ping
SET status = 'completed', output = $2, duration_ms = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRuntimePingCompletedForDaemon :one
UPDATE runtime_ping
SET status = 'completed', output = $5, duration_ms = $6, updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
RETURNING *;

-- name: SetRuntimePingFailed :one
UPDATE runtime_ping
SET status = 'failed', error = $2, duration_ms = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRuntimePingFailedForDaemon :one
UPDATE runtime_ping
SET status = 'failed', error = $5, duration_ms = $6, updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
RETURNING *;

-- name: SetRuntimePingTimeout :one
UPDATE runtime_ping
SET status = 'timeout', error = 'daemon did not respond within 60 seconds', updated_at = now()
WHERE id = $1 AND status IN ('pending', 'running')
RETURNING *;

-- name: SetRuntimePingTimeoutForDaemon :one
UPDATE runtime_ping
SET status = 'timeout', error = 'daemon did not respond within 60 seconds', updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4 AND status IN ('pending', 'running')
RETURNING *;

-- name: CreateRuntimeUpdate :one
INSERT INTO runtime_update (runtime_id, workspace_id, daemon_id, status, target_version)
VALUES ($1, $2, $3, 'pending', $4)
RETURNING *;

-- name: GetRuntimeUpdate :one
SELECT * FROM runtime_update
WHERE id = $1;

-- name: GetRuntimeUpdateForWorkspace :one
SELECT * FROM runtime_update
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3;

-- name: GetRuntimeUpdateForDaemon :one
SELECT * FROM runtime_update
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4;

-- name: PopPendingRuntimeUpdate :many
WITH next_update AS (
    SELECT id
    FROM runtime_update
    WHERE runtime_update.runtime_id = $1 AND runtime_update.status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE runtime_update
SET status = 'running', updated_at = now()
WHERE runtime_update.id = (SELECT id FROM next_update)
RETURNING *;

-- name: PopPendingRuntimeUpdateForDaemon :many
WITH next_update AS (
    SELECT id
    FROM runtime_update
    WHERE runtime_update.runtime_id = $1
      AND runtime_update.workspace_id = $2
      AND runtime_update.daemon_id = $3
      AND runtime_update.status = 'pending'
    ORDER BY created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE runtime_update
SET status = 'running', updated_at = now()
WHERE runtime_update.id = (SELECT id FROM next_update)
RETURNING *;

-- name: SetRuntimeUpdateCompleted :one
UPDATE runtime_update
SET status = 'completed', output = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRuntimeUpdateCompletedForDaemon :one
UPDATE runtime_update
SET status = 'completed', output = $5, updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
RETURNING *;

-- name: SetRuntimeUpdateFailed :one
UPDATE runtime_update
SET status = 'failed', error = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRuntimeUpdateFailedForDaemon :one
UPDATE runtime_update
SET status = 'failed', error = $5, updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4
RETURNING *;

-- name: SetRuntimeUpdateTimeout :one
UPDATE runtime_update
SET status = 'timeout', error = 'update did not complete within 120 seconds', updated_at = now()
WHERE id = $1 AND status IN ('pending', 'running')
RETURNING *;

-- name: SetRuntimeUpdateTimeoutForDaemon :one
UPDATE runtime_update
SET status = 'timeout', error = 'update did not complete within 120 seconds', updated_at = now()
WHERE id = $1 AND runtime_id = $2 AND workspace_id = $3 AND daemon_id = $4 AND status IN ('pending', 'running')
RETURNING *;
