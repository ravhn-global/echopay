-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByPhone :one
SELECT * FROM users WHERE phone = $1;

-- name: CreateUser :one
INSERT INTO users (phone)
VALUES ($1)
RETURNING *;

-- name: UpdateUserKYC :one
UPDATE users
SET full_name = $2,
    bvn_hash = $3,
    date_of_birth = $4,
    kyc_tier = $5,
    kyc_status = $6,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateUserReceiveAccount :one
UPDATE users
SET nuban = $2,
    bank_code = $3,
    account_name = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateUserLimits :one
UPDATE users
SET per_tx_limit_kobo = $2,
    per_day_limit_kobo = $3,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: SumUserSentLast24h :one
SELECT COALESCE(SUM(amount_kobo), 0)::BIGINT AS total_kobo
FROM payments
WHERE sender_user_id = $1
  AND status IN ('initiated', 'debiting', 'transferring', 'settled')
  AND created_at > NOW() - INTERVAL '24 hours';
