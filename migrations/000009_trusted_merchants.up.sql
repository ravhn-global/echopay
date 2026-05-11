CREATE TABLE trusted_merchants (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    merchant_user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    per_tx_cap_kobo BIGINT NOT NULL CHECK (per_tx_cap_kobo > 0),
    label TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (user_id <> merchant_user_id)
);

CREATE UNIQUE INDEX trusted_merchants_pair_idx
    ON trusted_merchants(user_id, merchant_user_id);
