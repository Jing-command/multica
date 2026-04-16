-- name: CreateAuthAbuseEvent :one
INSERT INTO auth_abuse_event (event_type, identifier, ip)
VALUES ($1, $2, $3)
RETURNING *;

-- name: CountAuthAbuseEventsByIdentifierSince :one
SELECT count(*)::int AS count
FROM auth_abuse_event
WHERE event_type = $1
  AND identifier = $2
  AND created_at >= $3;

-- name: CountAuthAbuseEventsByIPSince :one
SELECT count(*)::int AS count
FROM auth_abuse_event
WHERE event_type = $1
  AND ip = $2
  AND created_at >= $3;

-- name: CountAuthAbuseEventsByIPAndIdentifierSince :one
SELECT count(*)::int AS count
FROM auth_abuse_event
WHERE event_type = $1
  AND ip = $2
  AND identifier = $3
  AND created_at >= $4;

-- name: DeleteOldAuthAbuseEvents :exec
DELETE FROM auth_abuse_event
WHERE created_at < now() - interval '30 days';
