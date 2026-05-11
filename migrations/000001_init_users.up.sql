CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone TEXT NOT NULL UNIQUE,
    full_name TEXT,
    bvn_hash TEXT,
    date_of_birth DATE,
    kyc_tier SMALLINT NOT NULL DEFAULT 0,
    kyc_status TEXT NOT NULL DEFAULT 'pending',
    nuban TEXT,
    bank_code TEXT,
    account_name TEXT,
    per_tx_limit_kobo BIGINT NOT NULL DEFAULT 2000000,
    per_day_limit_kobo BIGINT NOT NULL DEFAULT 10000000,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX users_phone_idx ON users(phone);
