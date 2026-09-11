BEGIN;

CREATE TABLE telegram_contacts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    telegram_user_id BIGINT NOT NULL,
    first_name TEXT,
    last_name TEXT,
    username TEXT,
    phone_hash BYTEA,
    is_mutual BOOLEAN NOT NULL DEFAULT FALSE,
    raw_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    deleted BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT telegram_contacts_account_user_unique UNIQUE (account_id, telegram_user_id),
    CONSTRAINT telegram_contacts_deleted_state_check CHECK ((deleted = FALSE AND deleted_at IS NULL) OR deleted = TRUE)
);

CREATE INDEX idx_telegram_contacts_account_name
    ON telegram_contacts(account_id, first_name, last_name)
    WHERE deleted = FALSE;
CREATE INDEX idx_telegram_contacts_username
    ON telegram_contacts(account_id, username)
    WHERE deleted = FALSE AND username IS NOT NULL;

CREATE TABLE chat_members (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    chat_id UUID NOT NULL,
    peer_type TEXT NOT NULL DEFAULT 'user',
    telegram_peer_id BIGINT NOT NULL,
    first_name TEXT,
    last_name TEXT,
    username TEXT,
    role TEXT,
    raw_metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    deleted BOOLEAN NOT NULL DEFAULT FALSE,
    deleted_at TIMESTAMPTZ,
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chat_members_chat_account_fk
        FOREIGN KEY (chat_id, account_id)
        REFERENCES chats(id, account_id)
        ON DELETE RESTRICT,
    CONSTRAINT chat_members_peer_type_check CHECK (peer_type IN ('user', 'chat')),
    CONSTRAINT chat_members_scope_peer_unique UNIQUE (account_id, chat_id, peer_type, telegram_peer_id),
    CONSTRAINT chat_members_deleted_state_check CHECK ((deleted = FALSE AND deleted_at IS NULL) OR deleted = TRUE)
);

CREATE INDEX idx_chat_members_active_chat
    ON chat_members(account_id, chat_id, last_seen_at DESC)
    WHERE deleted = FALSE;
CREATE INDEX idx_chat_members_username
    ON chat_members(account_id, chat_id, username)
    WHERE deleted = FALSE AND username IS NOT NULL;

CREATE VIEW active_contacts AS
SELECT *
FROM telegram_contacts
WHERE deleted = FALSE;

CREATE VIEW active_chat_members AS
SELECT cm.*
FROM chat_members cm
JOIN chats c
  ON c.id = cm.chat_id
 AND c.account_id = cm.account_id
WHERE cm.deleted = FALSE
  AND c.deleted = FALSE;

COMMENT ON COLUMN telegram_contacts.phone_hash IS 'One-way normalized phone hash only. Raw contact phone numbers are not persisted until an encrypted field policy is implemented.';
COMMENT ON VIEW active_chat_members IS 'MCP/API member projection that hides deleted members and all members of deleted chats.';

COMMIT;
