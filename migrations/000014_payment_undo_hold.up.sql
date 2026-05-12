-- Add a "held" intermediate state where the sender has been charged but the
-- transfer to the receiver is deferred for the 10s undo window. The
-- partial index lets the releaser (and the reconciler safety net) scan
-- only the live holds cheaply.
ALTER TABLE payments ADD COLUMN hold_expires_at TIMESTAMPTZ;

CREATE INDEX payments_held_due_idx
    ON payments(hold_expires_at)
    WHERE status = 'held';
