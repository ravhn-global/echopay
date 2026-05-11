DROP INDEX IF EXISTS payments_auto_refund_idx;
ALTER TABLE payments DROP COLUMN IF EXISTS auto_refund_reference;
