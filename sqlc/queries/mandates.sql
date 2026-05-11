-- name: CreateMandate :one
INSERT INTO mandates (user_id, authorization_code, bank_name, bank_code, last4, channel, reusable, is_default)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetMandateByID :one
SELECT * FROM mandates WHERE id = $1;

-- name: GetDefaultMandate :one
SELECT * FROM mandates
WHERE user_id = $1 AND status = 'active' AND is_default = TRUE
LIMIT 1;

-- name: ListUserMandates :many
SELECT * FROM mandates
WHERE user_id = $1 AND status = 'active'
ORDER BY is_default DESC, created_at DESC;

-- name: UnsetDefaultMandate :exec
UPDATE mandates
SET is_default = FALSE
WHERE user_id = $1 AND is_default = TRUE;

-- name: SetDefaultMandate :one
UPDATE mandates
SET is_default = TRUE
WHERE id = $1 AND user_id = $2
RETURNING *;

-- name: RevokeMandate :exec
UPDATE mandates
SET status = 'revoked', revoked_at = NOW(), is_default = FALSE
WHERE id = $1 AND user_id = $2;
