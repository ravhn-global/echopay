CREATE TABLE device_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    jti UUID NOT NULL UNIQUE,
    fingerprint TEXT,
    model TEXT,
    os_name TEXT,
    os_version TEXT,
    app_version TEXT,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);

CREATE INDEX device_sessions_user_active_idx
    ON device_sessions(user_id, last_seen_at DESC)
    WHERE revoked_at IS NULL;

CREATE INDEX device_sessions_jti_active_idx
    ON device_sessions(jti)
    WHERE revoked_at IS NULL;
