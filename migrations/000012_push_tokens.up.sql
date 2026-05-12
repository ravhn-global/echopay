CREATE TABLE push_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_session_id UUID REFERENCES device_sessions(id) ON DELETE SET NULL,
    fcm_token TEXT NOT NULL,
    platform TEXT NOT NULL CHECK (platform IN ('ios','android')),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

-- An fcm_token is globally unique among active rows. When a device that used
-- to belong to user A now signs in as user B, we upsert and reassign.
CREATE UNIQUE INDEX push_tokens_token_active_idx
    ON push_tokens(fcm_token) WHERE revoked_at IS NULL;

CREATE INDEX push_tokens_user_active_idx
    ON push_tokens(user_id) WHERE revoked_at IS NULL;
