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

-- name: ListPaymentsForAutoRefund :many
SELECT * FROM payments
WHERE status IN ('debiting','transferring','settling')
  AND charge_status = 'success'
  AND auto_refund_reference IS NULL
  AND created_at < $1
ORDER BY created_at ASC
LIMIT $2;

-- name: MarkPaymentAutoRefunded :one
UPDATE payments
SET auto_refund_reference = $2,
    status = 'failed',
    failure_reason = $3,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: HoldPayment :one
-- Conditional on status=transferring so the held path never trips a payment
-- that's already moved through the normal flow.
UPDATE payments
SET status = 'held',
    hold_expires_at = $2,
    updated_at = NOW()
WHERE id = $1 AND status = 'transferring'
RETURNING *;

-- name: ReleaseHeldPayment :one
-- Conditional on status=held so the undo path and the release path don't
-- both try to advance the payment. Whoever wins the UPDATE proceeds.
UPDATE payments
SET status = 'transferring',
    updated_at = NOW()
WHERE id = $1 AND status = 'held'
RETURNING *;

-- name: MarkPaymentRefunded :one
-- Conditional on status=held so undo can't fire after the hold has been
-- released into transfer.
UPDATE payments
SET status = 'refunded',
    auto_refund_reference = $2,
    failure_reason = $3,
    updated_at = NOW()
WHERE id = $1 AND status = 'held'
RETURNING *;

-- name: ListExpiredHolds :many
-- For the reconciler safety net — picks up holds where the in-process
-- timer was lost across a restart.
SELECT * FROM payments
WHERE status = 'held'
  AND hold_expires_at IS NOT NULL
  AND hold_expires_at <= $1
ORDER BY hold_expires_at ASC
LIMIT $2;
