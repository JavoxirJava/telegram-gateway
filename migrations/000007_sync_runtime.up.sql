BEGIN;

CREATE TABLE telegram_session_runtime (
    account_id UUID PRIMARY KEY REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    shard_key TEXT NOT NULL DEFAULT 'default',
    desired_state TEXT NOT NULL DEFAULT 'online',
    observed_state TEXT NOT NULL DEFAULT 'offline',
    worker_id TEXT,
    lease_token UUID,
    lease_expires_at TIMESTAMPTZ,
    generation BIGINT NOT NULL DEFAULT 0,
    last_error TEXT,
    last_started_at TIMESTAMPTZ,
    last_ready_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT telegram_session_runtime_desired_state_check
        CHECK (desired_state IN ('online', 'offline')),
    CONSTRAINT telegram_session_runtime_observed_state_check
        CHECK (observed_state IN ('offline', 'starting', 'authorizing', 'ready', 'backoff', 'error')),
    CONSTRAINT telegram_session_runtime_generation_check CHECK (generation >= 0),
    CONSTRAINT telegram_session_runtime_lease_check
        CHECK ((worker_id IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL)
            OR (worker_id IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))
);

CREATE INDEX idx_telegram_session_runtime_lease
    ON telegram_session_runtime(lease_expires_at)
    WHERE lease_expires_at IS NOT NULL;
CREATE INDEX idx_telegram_session_runtime_shard_state
    ON telegram_session_runtime(shard_key, desired_state, observed_state);

ALTER TABLE sync_states
    ADD COLUMN lease_owner TEXT,
    ADD COLUMN lease_token UUID,
    ADD COLUMN lease_expires_at TIMESTAMPTZ,
    ADD COLUMN generation BIGINT NOT NULL DEFAULT 0,
    ADD CONSTRAINT sync_states_generation_check CHECK (generation >= 0),
    ADD CONSTRAINT sync_states_lease_check
        CHECK ((lease_owner IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL)
            OR (lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL));

CREATE INDEX idx_sync_states_lease
    ON sync_states(lease_expires_at)
    WHERE lease_expires_at IS NOT NULL;

ALTER TABLE message_media
    ADD COLUMN attempt_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN last_attempt_at TIMESTAMPTZ,
    ADD COLUMN downloaded_at TIMESTAMPTZ,
    ADD COLUMN last_error TEXT,
    ADD CONSTRAINT message_media_attempt_count_check CHECK (attempt_count >= 0);

CREATE VIEW active_message_media AS
SELECT
    mm.*,
    m.account_id,
    m.chat_id
FROM message_media mm
JOIN active_messages m ON m.id = mm.message_id;

COMMENT ON TABLE telegram_session_runtime IS 'Worker ownership and lifecycle state for Telegram sessions. No raw session secrets are stored here.';
COMMENT ON VIEW active_message_media IS 'Media projection for normal API/MCP reads. Media from deleted messages or deleted chats is excluded.';

COMMIT;
