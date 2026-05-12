-- name: UpsertPushToken :one
INSERT INTO push_tokens (user_id, device_session_id, fcm_token, platform)
VALUES ($1, $2, $3, $4)
ON CONFLICT (fcm_token) WHERE revoked_at IS NULL
DO UPDATE SET user_id = EXCLUDED.user_id,
              device_session_id = EXCLUDED.device_session_id,
              platform = EXCLUDED.platform,
              last_seen_at = NOW()
RETURNING *;

-- name: ListUserPushTokens :many
SELECT * FROM push_tokens
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY last_seen_at DESC;

-- name: ListUserPushTokensExceptSession :many
SELECT * FROM push_tokens
WHERE user_id = $1 AND revoked_at IS NULL
  AND (device_session_id IS NULL OR device_session_id <> $2)
ORDER BY last_seen_at DESC;

-- name: RevokePushToken :exec
UPDATE push_tokens
SET revoked_at = NOW()
WHERE fcm_token = $1 AND revoked_at IS NULL;

-- name: RevokePushTokensForSession :exec
UPDATE push_tokens
SET revoked_at = NOW()
WHERE device_session_id = $1 AND revoked_at IS NULL;
