-- name: CreatePayment :one
INSERT INTO payments (
    token_id, sender_user_id, receiver_user_id, sender_mandate_id,
    amount_kobo, idempotency_key, refunds_payment_id
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetPaymentByID :one
SELECT * FROM payments WHERE id = $1;

-- name: GetPaymentByIdempotencyKey :one
SELECT * FROM payments WHERE idempotency_key = $1;

-- name: GetPaymentByChargeReference :one
SELECT * FROM payments WHERE charge_reference = $1;

-- name: GetPaymentByTransferReference :one
SELECT * FROM payments WHERE transfer_reference = $1;

-- name: UpdatePaymentCharge :one
UPDATE payments
SET charge_reference = $2,
    charge_status = $3,
    status = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdatePaymentTransfer :one
UPDATE payments
SET transfer_reference = $2,
    transfer_status = $3,
    status = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: MarkPaymentSettled :one
UPDATE payments
SET status = 'settled',
    settled_at = NOW(),
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: MarkPaymentFailed :one
UPDATE payments
SET status = 'failed',
    failure_reason = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: ListUserPayments :many
SELECT * FROM payments
WHERE sender_user_id = $1 OR receiver_user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListStuckPayments :many
SELECT * FROM payments
WHERE status IN ('debiting','transferring','settling','initiated')
  AND updated_at < $1
ORDER BY updated_at ASC
LIMIT $2;
