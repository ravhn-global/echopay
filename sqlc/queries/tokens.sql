-- name: CreateToken :one
INSERT INTO tokens (code, issued_by_user_id, amount_kobo, note, expires_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetTokenByCode :one
SELECT * FROM tokens WHERE code = $1;

-- name: GetTokenByID :one
SELECT * FROM tokens WHERE id = $1;

-- name: ClaimToken :one
UPDATE tokens
SET status = 'claimed',
    claimed_by_user_id = $2,
    claimed_at = NOW(),
    updated_at = NOW()
WHERE id = $1 AND status = 'created' AND expires_at > NOW()
RETURNING *;

-- name: MarkTokenSettled :one
UPDATE tokens
SET status = 'settled',
    settled_payment_id = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: MarkTokenFailed :one
UPDATE tokens
SET status = 'failed',
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ExpireStaleTokens :exec
UPDATE tokens
SET status = 'expired',
    updated_at = NOW()
WHERE status = 'created' AND expires_at <= NOW();
