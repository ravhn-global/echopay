-- name: InsertAuditEvent :one
INSERT INTO audit_events (
    user_id, actor_user_id, action, target_type, target_id, metadata, ip, user_agent
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListUserAuditEvents :many
SELECT * FROM audit_events
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;
