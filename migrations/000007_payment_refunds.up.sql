-- Allow payments without a token (refunds are receiver-issued and don't go
-- through the ultrasonic channel).
ALTER TABLE payments ALTER COLUMN token_id DROP NOT NULL;

-- A refund is itself a payment, with a link back to the payment it reverses.
ALTER TABLE payments ADD COLUMN refunds_payment_id UUID REFERENCES payments(id);

CREATE INDEX payments_refunds_idx ON payments(refunds_payment_id) WHERE refunds_payment_id IS NOT NULL;
