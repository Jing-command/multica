-- name: CreateRuntimeUpdateRequest :one
INSERT INTO runtime_update_request (workspace_id, runtime_id, daemon_id, target_version, status)
VALUES ($1, $2, $3, $4, 'pending')
RETURNING *;

-- name: GetRuntimeUpdateRequest :one
SELECT * FROM runtime_update_request
WHERE id = $1;

-- name: GetRuntimeUpdateRequestForDaemon :one
SELECT * FROM runtime_update_request
WHERE id = $1
  AND daemon_id = $2
  AND workspace_id = $3;

-- name: ClaimNextRuntimeUpdateRequestForDaemon :one
UPDATE runtime_update_request AS rur
SET status = 'running', claimed_at = now(), updated_at = now()
WHERE rur.id = (
    SELECT pending.id
    FROM runtime_update_request AS pending
    WHERE pending.runtime_id = $1
      AND pending.daemon_id = $2
      AND pending.workspace_id = $3
      AND pending.status = 'pending'
    ORDER BY pending.created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: CompleteRuntimeUpdateRequestForDaemon :one
UPDATE runtime_update_request
SET status = 'completed', output = $4, completed_at = now(), updated_at = now()
WHERE id = $1
  AND daemon_id = $2
  AND workspace_id = $3
  AND status IN ('pending', 'running')
RETURNING *;

-- name: FailRuntimeUpdateRequestForDaemon :one
UPDATE runtime_update_request
SET status = 'failed', error = $4, completed_at = now(), updated_at = now()
WHERE id = $1
  AND daemon_id = $2
  AND workspace_id = $3
  AND status IN ('pending', 'running')
RETURNING *;

-- name: TimeoutStaleRuntimeUpdateRequests :many
UPDATE runtime_update_request
SET status = 'timeout', error = 'update did not complete within timeout', completed_at = now(), updated_at = now()
WHERE status IN ('pending', 'running')
  AND created_at < now() - make_interval(secs => @stale_seconds::double precision)
RETURNING id, runtime_id;

-- name: CountActiveRuntimeUpdateRequests :one
SELECT count(*) FROM runtime_update_request
WHERE runtime_id = $1
  AND status IN ('pending', 'running');
