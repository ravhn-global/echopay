DROP INDEX IF EXISTS payments_held_due_idx;
ALTER TABLE payments DROP COLUMN IF EXISTS hold_expires_at;
