-- name: CreateOTPRequest :one
INSERT INTO otp_requests (phone, code_hash, purpose, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetLatestOTPRequest :one
SELECT * FROM otp_requests
WHERE phone = $1 AND purpose = $2 AND verified_at IS NULL
ORDER BY created_at DESC
LIMIT 1;

-- name: IncrementOTPAttempts :one
UPDATE otp_requests
SET attempts = attempts + 1
WHERE id = $1
RETURNING *;

-- name: MarkOTPVerified :exec
UPDATE otp_requests
SET verified_at = NOW()
WHERE id = $1;

-- name: ExpirePreviousOTPs :exec
UPDATE otp_requests
SET expires_at = NOW() - INTERVAL '1 second'
WHERE phone = $1 AND purpose = $2 AND verified_at IS NULL AND expires_at > NOW();
