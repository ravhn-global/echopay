-- name: CreatePendingLimitChange :one
INSERT INTO pending_limit_changes (user_id, per_tx_limit_kobo, per_day_limit_kobo, applies_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetPendingLimitChange :one
SELECT * FROM pending_limit_changes
WHERE user_id = $1 AND applied_at IS NULL AND cancelled_at IS NULL
LIMIT 1;

-- name: CancelPendingLimitChanges :exec
UPDATE pending_limit_changes
SET cancelled_at = NOW()
WHERE user_id = $1 AND applied_at IS NULL AND cancelled_at IS NULL;

-- name: ListDuePendingLimits :many
SELECT * FROM pending_limit_changes
WHERE applied_at IS NULL AND cancelled_at IS NULL AND applies_at <= $1
ORDER BY applies_at ASC
LIMIT $2;

-- name: MarkPendingLimitApplied :exec
UPDATE pending_limit_changes
SET applied_at = NOW()
WHERE id = $1;
