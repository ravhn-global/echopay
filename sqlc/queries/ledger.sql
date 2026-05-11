-- name: InsertLedgerEntry :one
INSERT INTO ledger_entries (payment_id, user_id, side, amount_kobo, reference, metadata)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListUserLedger :many
SELECT * FROM ledger_entries
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListPaymentLedger :many
SELECT * FROM ledger_entries
WHERE payment_id = $1
ORDER BY created_at ASC;
