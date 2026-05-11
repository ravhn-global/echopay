CREATE TABLE mandates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    authorization_code TEXT NOT NULL,
    bank_name TEXT NOT NULL,
    bank_code TEXT,
    last4 TEXT NOT NULL,
    channel TEXT NOT NULL,
    reusable BOOLEAN NOT NULL DEFAULT TRUE,
    status TEXT NOT NULL DEFAULT 'active',
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX mandates_user_authcode_idx ON mandates(user_id, authorization_code);
CREATE INDEX mandates_user_active_idx ON mandates(user_id) WHERE status = 'active';
CREATE UNIQUE INDEX mandates_user_default_idx ON mandates(user_id) WHERE is_default = TRUE AND status = 'active';
