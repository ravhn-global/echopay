CREATE TABLE pending_limit_changes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    per_tx_limit_kobo BIGINT NOT NULL CHECK (per_tx_limit_kobo > 0),
    per_day_limit_kobo BIGINT NOT NULL CHECK (per_day_limit_kobo > 0),
    applies_at TIMESTAMPTZ NOT NULL,
    applied_at TIMESTAMPTZ,
    cancelled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- At most one pending (not-yet-applied, not-cancelled) change per user.
CREATE UNIQUE INDEX pending_limit_one_per_user
    ON pending_limit_changes(user_id)
    WHERE applied_at IS NULL AND cancelled_at IS NULL;

CREATE INDEX pending_limit_due_idx
    ON pending_limit_changes(applies_at)
    WHERE applied_at IS NULL AND cancelled_at IS NULL;
