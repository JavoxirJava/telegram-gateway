BEGIN;

CREATE TABLE app_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE telegram_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES app_users(id) ON DELETE RESTRICT,
    telegram_user_id BIGINT,
    phone_hash BYTEA,
    display_name TEXT,
    username TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    connected_at TIMESTAMPTZ,
    disconnected_at TIMESTAMPTZ,
    last_update_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT telegram_accounts_status_check CHECK (status IN ('pending', 'active', 'disconnected', 'blocked')),
    CONSTRAINT telegram_accounts_user_telegram_unique UNIQUE (user_id, telegram_user_id)
);

CREATE INDEX idx_telegram_accounts_user_id ON telegram_accounts(user_id);
CREATE INDEX idx_telegram_accounts_status ON telegram_accounts(status);

CREATE TABLE chats (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    telegram_chat_id BIGINT NOT NULL,
    chat_type TEXT NOT NULL,
    title TEXT,
    username TEXT,
    photo_object_key TEXT,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    last_message_id BIGINT,
    last_message_at TIMESTAMPTZ,
    deleted BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chats_account_telegram_unique UNIQUE (account_id, telegram_chat_id),
    CONSTRAINT chats_deleted_state_check CHECK ((deleted = FALSE AND deleted_at IS NULL) OR deleted = TRUE)
);

CREATE INDEX idx_chats_account_id ON chats(account_id);
CREATE INDEX idx_chats_account_last_message ON chats(account_id, last_message_at DESC);
CREATE INDEX idx_chats_active ON chats(account_id, telegram_chat_id) WHERE deleted = FALSE;

CREATE TABLE messages (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    chat_id UUID NOT NULL REFERENCES chats(id) ON DELETE RESTRICT,
    telegram_message_id BIGINT NOT NULL,
    sender_telegram_id BIGINT,
    sender_chat_id BIGINT,
    message_type TEXT NOT NULL,
    content TEXT,
    content_entities JSONB NOT NULL DEFAULT '[]'::jsonb,
    reply_to_message_id BIGINT,
    forward_info JSONB,
    raw_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    sent_at TIMESTAMPTZ NOT NULL,
    edited_at TIMESTAMPTZ,
    deleted BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT messages_account_chat_telegram_unique UNIQUE (account_id, chat_id, telegram_message_id),
    CONSTRAINT messages_deleted_state_check CHECK ((deleted = FALSE AND deleted_at IS NULL) OR deleted = TRUE)
);

CREATE INDEX idx_messages_chat_time ON messages(chat_id, sent_at DESC, telegram_message_id DESC);
CREATE INDEX idx_messages_account_time ON messages(account_id, sent_at DESC);
CREATE INDEX idx_messages_sender ON messages(account_id, sender_telegram_id, sent_at DESC);
CREATE INDEX idx_messages_active_chat_time ON messages(chat_id, sent_at DESC, telegram_message_id DESC) WHERE deleted = FALSE;

CREATE TABLE message_media (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id UUID NOT NULL REFERENCES messages(id) ON DELETE RESTRICT,
    media_type TEXT NOT NULL,
    telegram_file_id BIGINT,
    unique_file_key TEXT,
    object_key TEXT,
    mime_type TEXT,
    file_name TEXT,
    file_size BIGINT,
    width INTEGER,
    height INTEGER,
    duration_seconds INTEGER,
    sha256 BYTEA,
    download_status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT message_media_status_check CHECK (download_status IN ('pending', 'downloading', 'ready', 'failed', 'skipped')),
    CONSTRAINT message_media_file_size_check CHECK (file_size IS NULL OR file_size >= 0)
);

CREATE INDEX idx_message_media_message_id ON message_media(message_id);
CREATE INDEX idx_message_media_object_key ON message_media(object_key) WHERE object_key IS NOT NULL;
CREATE UNIQUE INDEX idx_message_media_unique_file_key ON message_media(unique_file_key) WHERE unique_file_key IS NOT NULL;

CREATE TABLE sync_states (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    chat_id UUID REFERENCES chats(id) ON DELETE RESTRICT,
    sync_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    cursor JSONB NOT NULL DEFAULT '{}'::jsonb,
    oldest_message_id BIGINT,
    newest_message_id BIGINT,
    last_synced_at TIMESTAMPTZ,
    next_sync_at TIMESTAMPTZ,
    retry_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT sync_states_status_check CHECK (status IN ('pending', 'running', 'paused', 'completed', 'failed')),
    CONSTRAINT sync_states_retry_count_check CHECK (retry_count >= 0)
);

CREATE UNIQUE INDEX idx_sync_states_account_scope
    ON sync_states(account_id, COALESCE(chat_id, '00000000-0000-0000-0000-000000000000'::uuid), sync_type);
CREATE INDEX idx_sync_states_work_queue ON sync_states(status, next_sync_at) WHERE status IN ('pending', 'failed');

CREATE TABLE audit_logs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id UUID REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    actor_type TEXT NOT NULL,
    actor_id TEXT,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT,
    ip_address INET,
    user_agent TEXT,
    request_id UUID,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    previous_hash BYTEA,
    hash BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT audit_logs_actor_type_check CHECK (actor_type IN ('USER', 'MCP', 'SYSTEM', 'TELEGRAM', 'ADMIN'))
);

CREATE INDEX idx_audit_logs_account_time ON audit_logs(account_id, created_at DESC);
CREATE INDEX idx_audit_logs_action_time ON audit_logs(action, created_at DESC);
CREATE INDEX idx_audit_logs_resource ON audit_logs(resource_type, resource_id, created_at DESC);
CREATE INDEX idx_audit_logs_request_id ON audit_logs(request_id) WHERE request_id IS NOT NULL;

CREATE OR REPLACE FUNCTION prevent_audit_log_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'audit_logs is append-only';
END;
$$;

CREATE TRIGGER audit_logs_no_update
BEFORE UPDATE ON audit_logs
FOR EACH ROW EXECUTE FUNCTION prevent_audit_log_mutation();

CREATE TRIGGER audit_logs_no_delete
BEFORE DELETE ON audit_logs
FOR EACH ROW EXECUTE FUNCTION prevent_audit_log_mutation();

CREATE VIEW active_chats AS
SELECT *
FROM chats
WHERE deleted = FALSE;

CREATE VIEW active_messages AS
SELECT *
FROM messages
WHERE deleted = FALSE;

COMMENT ON TABLE messages IS 'Persistent Telegram message mirror. Telegram deletions are represented with deleted=true; rows are retained.';
COMMENT ON VIEW active_messages IS 'Read-only projection intended for normal API/MCP reads. Soft-deleted Telegram messages are excluded.';
COMMENT ON TABLE audit_logs IS 'Append-only security and access audit trail. Hash-chain values are generated by the application layer.';

COMMIT;
