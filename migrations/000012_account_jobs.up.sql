BEGIN;
-- The database is the durable job/checkpoint authority. NATS wakes one account;
-- losing or duplicating a wake notification cannot lose or repeat a commit.
CREATE TABLE gateway_sync_jobs (
 id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
 account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
 generation BIGINT NOT NULL CHECK(generation>0),
 kind TEXT NOT NULL CHECK(kind IN ('chats','history','contacts','members','media','refresh','reconcile')),
 dedup_key TEXT NOT NULL CHECK(length(dedup_key)<=512),
 payload JSONB NOT NULL DEFAULT '{}',
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','running','completed','dead','superseded')),
 reschedule BOOLEAN NOT NULL DEFAULT FALSE,
 attempts INT NOT NULL DEFAULT 0 CHECK(attempts>=0),
 due_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 claim_token UUID,
 claim_until TIMESTAMPTZ,
 last_error_code TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE(account_id,generation,kind,dedup_key)
);
CREATE INDEX gateway_sync_jobs_due ON gateway_sync_jobs(account_id,generation,due_at,created_at) WHERE status IN ('pending','running');
CREATE TABLE gateway_history_progress (
 account_id UUID NOT NULL,
 chat_id UUID NOT NULL,
 before_message_id BIGINT NOT NULL DEFAULT 0,
 exhausted BOOLEAN NOT NULL DEFAULT FALSE,
 checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(account_id,chat_id),
 FOREIGN KEY(chat_id,account_id) REFERENCES chats(id,account_id) ON DELETE RESTRICT
);
ALTER TABLE chats ADD COLUMN access_blocked BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE messages ADD COLUMN access_blocked BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE message_media ADD COLUMN retired BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE message_media ADD COLUMN source_generation BIGINT NOT NULL DEFAULT 0;
-- Keep existing view column lists unchanged: dependent API views need no change.
CREATE OR REPLACE VIEW active_chats AS SELECT id,account_id,telegram_chat_id,chat_type,title,username,photo_object_key,metadata,last_message_id,last_message_at,deleted,deleted_at,created_at,updated_at FROM chats WHERE NOT deleted AND NOT access_blocked;
CREATE OR REPLACE VIEW active_messages AS SELECT m.id,m.account_id,m.chat_id,m.telegram_message_id,m.sender_telegram_id,m.sender_chat_id,m.message_type,m.content,m.content_entities,m.reply_to_message_id,m.forward_info,m.raw_metadata,m.sent_at,m.edited_at,m.deleted,m.deleted_at,m.created_at,m.updated_at FROM messages m JOIN chats c ON c.id=m.chat_id AND c.account_id=m.account_id WHERE NOT m.deleted AND NOT c.deleted AND NOT m.access_blocked AND NOT c.access_blocked;
CREATE OR REPLACE VIEW active_message_media AS SELECT mm.id,mm.message_id,mm.media_type,mm.telegram_file_id,mm.unique_file_key,mm.object_key,mm.mime_type,mm.file_name,mm.file_size,mm.width,mm.height,mm.duration_seconds,mm.sha256,mm.download_status,mm.created_at,mm.updated_at,mm.attempt_count,mm.last_attempt_at,mm.downloaded_at,mm.last_error,m.account_id,m.chat_id FROM message_media mm JOIN active_messages m ON m.id=mm.message_id WHERE NOT mm.retired;
CREATE OR REPLACE VIEW active_chat_members AS SELECT cm.* FROM chat_members cm JOIN active_chats c ON c.id=cm.chat_id AND c.account_id=cm.account_id WHERE NOT cm.deleted;
COMMENT ON TABLE gateway_sync_jobs IS 'Fenced account-local sync work. Payloads contain only progress IDs; no Telegram credentials.';
COMMIT;
