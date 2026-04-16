-- name: CreateRuntimePing :one
INSERT INTO runtime_ping (runtime_id, status)
VALUES ($1, 'pending')
RETURNING *;

-- name: GetRuntimePing :one
SELECT * FROM runtime_ping
WHERE id = $1;

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

-- name: SetRuntimePingCompleted :one
UPDATE runtime_ping
SET status = 'completed', output = $2, duration_ms = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRuntimePingFailed :one
UPDATE runtime_ping
SET status = 'failed', error = $2, duration_ms = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRuntimePingTimeout :one
UPDATE runtime_ping
SET status = 'timeout', error = 'daemon did not respond within 60 seconds', updated_at = now()
WHERE id = $1 AND status IN ('pending', 'running')
RETURNING *;

-- name: CreateRuntimeUpdate :one
INSERT INTO runtime_update (runtime_id, status, target_version)
VALUES ($1, 'pending', $2)
RETURNING *;

-- name: GetRuntimeUpdate :one
SELECT * FROM runtime_update
WHERE id = $1;

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

-- name: SetRuntimeUpdateCompleted :one
UPDATE runtime_update
SET status = 'completed', output = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRuntimeUpdateFailed :one
UPDATE runtime_update
SET status = 'failed', error = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetRuntimeUpdateTimeout :one
UPDATE runtime_update
SET status = 'timeout', error = 'update did not complete within 120 seconds', updated_at = now()
WHERE id = $1 AND status IN ('pending', 'running')
RETURNING *;
