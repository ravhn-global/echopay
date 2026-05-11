CREATE TABLE ledger_entries (
    id BIGSERIAL PRIMARY KEY,
    payment_id UUID NOT NULL REFERENCES payments(id),
    user_id UUID NOT NULL REFERENCES users(id),
    side TEXT NOT NULL CHECK (side IN ('debit','credit')),
    amount_kobo BIGINT NOT NULL CHECK (amount_kobo > 0),
    reference TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX ledger_user_created_idx ON ledger_entries(user_id, created_at DESC);
CREATE INDEX ledger_payment_idx ON ledger_entries(payment_id);
