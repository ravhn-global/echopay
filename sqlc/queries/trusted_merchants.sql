-- name: UpsertTrustedMerchant :one
INSERT INTO trusted_merchants (user_id, merchant_user_id, per_tx_cap_kobo, label)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, merchant_user_id)
DO UPDATE SET per_tx_cap_kobo = EXCLUDED.per_tx_cap_kobo,
              label = EXCLUDED.label,
              updated_at = NOW()
RETURNING *;

-- name: GetTrustedMerchantCap :one
SELECT per_tx_cap_kobo FROM trusted_merchants
WHERE user_id = $1 AND merchant_user_id = $2;

-- name: ListTrustedMerchants :many
SELECT tm.*, u.full_name AS merchant_name, u.phone AS merchant_phone
FROM trusted_merchants tm
JOIN users u ON u.id = tm.merchant_user_id
WHERE tm.user_id = $1
ORDER BY tm.created_at DESC;

-- name: DeleteTrustedMerchant :exec
DELETE FROM trusted_merchants
WHERE id = $1 AND user_id = $2;
