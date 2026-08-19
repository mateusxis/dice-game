-- name: InsertAuditLog :one
-- INSERT is the only operation the audit_logs table permits; a trigger raises
-- on UPDATE, DELETE and TRUNCATE.
INSERT INTO audit_logs (id, occurred_at, actor_id, channel, endpoint_or_event, http_method, action, error, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id, occurred_at, actor_id, channel, endpoint_or_event, http_method, action, error, payload;

-- name: ListAuditLogs :many
SELECT id, occurred_at, actor_id, channel, endpoint_or_event, http_method, action, error, payload
FROM audit_logs
ORDER BY occurred_at DESC
LIMIT $1 OFFSET $2;

-- name: ListAuditLogsByActor :many
SELECT id, occurred_at, actor_id, channel, endpoint_or_event, http_method, action, error, payload
FROM audit_logs
WHERE actor_id = $1
ORDER BY occurred_at DESC
LIMIT $2 OFFSET $3;
