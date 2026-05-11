ALTER TABLE payments ADD COLUMN auto_refund_reference TEXT;

CREATE INDEX payments_auto_refund_idx ON payments(auto_refund_reference)
    WHERE auto_refund_reference IS NOT NULL;
