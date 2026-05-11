-- name: CreateDeviceSession :one
INSERT INTO device_sessions (
    user_id, jti, fingerprint, model, os_name, os_version, app_version
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetActiveDeviceSessionByJTI :one
SELECT * FROM device_sessions
WHERE jti = $1 AND revoked_at IS NULL
LIMIT 1;

-- name: TouchDeviceSession :exec
UPDATE device_sessions
SET last_seen_at = NOW()
WHERE jti = $1 AND revoked_at IS NULL AND last_seen_at < $2;

-- name: ListUserDeviceSessions :many
SELECT * FROM device_sessions
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY last_seen_at DESC;

-- name: RevokeDeviceSession :exec
UPDATE device_sessions
SET revoked_at = NOW()
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: RevokeAllOtherDeviceSessions :exec
UPDATE device_sessions
SET revoked_at = NOW()
WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL;
