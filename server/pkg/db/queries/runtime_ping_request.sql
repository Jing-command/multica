-- name: CreateRuntimePingRequest :one
INSERT INTO runtime_ping_request (workspace_id, runtime_id, daemon_id, status)
VALUES ($1, $2, $3, 'pending')
RETURNING *;

-- name: GetRuntimePingRequest :one
SELECT * FROM runtime_ping_request
WHERE id = $1;

-- name: GetRuntimePingRequestForDaemon :one
SELECT * FROM runtime_ping_request
WHERE id = $1
  AND daemon_id = $2
  AND workspace_id = $3;

-- name: ClaimNextRuntimePingRequestForDaemon :one
UPDATE runtime_ping_request AS rpr
SET status = 'running', claimed_at = now(), updated_at = now()
WHERE rpr.id = (
    SELECT pending.id
    FROM runtime_ping_request AS pending
    WHERE pending.runtime_id = $1
      AND pending.daemon_id = $2
      AND pending.workspace_id = $3
      AND pending.status = 'pending'
    ORDER BY pending.created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: CompleteRuntimePingRequestForDaemon :one
UPDATE runtime_ping_request
SET status = 'completed', output = $4, duration_ms = $5, completed_at = now(), updated_at = now()
WHERE id = $1
  AND daemon_id = $2
  AND workspace_id = $3
  AND status IN ('pending', 'running')
RETURNING *;

-- name: FailRuntimePingRequestForDaemon :one
UPDATE runtime_ping_request
SET status = 'failed', error = $4, duration_ms = $5, completed_at = now(), updated_at = now()
WHERE id = $1
  AND daemon_id = $2
  AND workspace_id = $3
  AND status IN ('pending', 'running')
RETURNING *;

-- name: TimeoutStaleRuntimePingRequests :many
UPDATE runtime_ping_request
SET status = 'timeout', error = 'daemon did not respond within timeout', completed_at = now(), updated_at = now()
WHERE status IN ('pending', 'running')
  AND created_at < now() - make_interval(secs => @stale_seconds::double precision)
RETURNING id, runtime_id;
