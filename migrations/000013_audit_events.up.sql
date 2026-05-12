CREATE TABLE audit_events (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    actor_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    target_type TEXT,
    target_id TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    ip TEXT,
    user_agent TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Append-only — no DELETE policy, no updated_at. The user's list view is
-- the primary read path.
CREATE INDEX audit_events_user_created_idx ON audit_events(user_id, created_at DESC);
CREATE INDEX audit_events_action_idx ON audit_events(action);
