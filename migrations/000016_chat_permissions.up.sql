BEGIN;
CREATE TABLE chat_permissions (
 account_id UUID NOT NULL,
 chat_id UUID NOT NULL,
 can_read BOOLEAN NOT NULL DEFAULT FALSE,
 can_send BOOLEAN NOT NULL DEFAULT FALSE,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(account_id,chat_id),
 FOREIGN KEY(chat_id,account_id) REFERENCES chats(id,account_id) ON DELETE CASCADE
);
-- Deliberately no backfill: existing AI grants do not authorize any chat.
ALTER TABLE access_tokens DROP CONSTRAINT access_tokens_read_scopes_only;
ALTER TABLE access_tokens ADD CONSTRAINT access_tokens_allowed_scopes CHECK (scopes <@ ARRAY['profile:read','chats:list','chat:read','messages:read','messages:search','messages:send','contacts:read','media:read','members:read']::TEXT[]);
CREATE TABLE message_send_requests (
 account_id UUID NOT NULL REFERENCES telegram_accounts(id),
 client_id UUID NOT NULL REFERENCES gateway_clients(id),
 request_id UUID NOT NULL,
 chat_id UUID NOT NULL,
 body_hash BYTEA NOT NULL,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','accepted','unknown')),
 telegram_message_id BIGINT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(account_id,client_id,request_id),
 FOREIGN KEY(chat_id,account_id) REFERENCES chats(id,account_id)
);
COMMIT;
