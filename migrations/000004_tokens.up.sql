CREATE TABLE tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT NOT NULL UNIQUE,
    issued_by_user_id UUID NOT NULL REFERENCES users(id),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    note TEXT,
    status TEXT NOT NULL DEFAULT 'created',
    claimed_by_user_id UUID REFERENCES users(id),
    claimed_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL,
    settled_payment_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX tokens_status_expires_idx ON tokens(status, expires_at);
CREATE INDEX tokens_issuer_created_idx ON tokens(issued_by_user_id, created_at DESC);
