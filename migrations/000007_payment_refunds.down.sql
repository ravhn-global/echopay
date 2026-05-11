DROP INDEX IF EXISTS payments_refunds_idx;
ALTER TABLE payments DROP COLUMN IF EXISTS refunds_payment_id;
ALTER TABLE payments ALTER COLUMN token_id SET NOT NULL;
