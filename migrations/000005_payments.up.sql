CREATE TABLE payments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    token_id UUID NOT NULL REFERENCES tokens(id),
    sender_user_id UUID NOT NULL REFERENCES users(id),
    receiver_user_id UUID NOT NULL REFERENCES users(id),
    sender_mandate_id UUID NOT NULL REFERENCES mandates(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    idempotency_key TEXT NOT NULL UNIQUE,
    charge_reference TEXT,
    charge_status TEXT,
    transfer_reference TEXT,
    transfer_status TEXT,
    status TEXT NOT NULL DEFAULT 'initiated',
    failure_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    settled_at TIMESTAMPTZ
);

CREATE INDEX payments_sender_created_idx ON payments(sender_user_id, created_at DESC);
CREATE INDEX payments_receiver_created_idx ON payments(receiver_user_id, created_at DESC);
CREATE INDEX payments_status_idx ON payments(status);
CREATE INDEX payments_charge_ref_idx ON payments(charge_reference) WHERE charge_reference IS NOT NULL;
