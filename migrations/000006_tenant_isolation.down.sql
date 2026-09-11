BEGIN;

CREATE OR REPLACE VIEW active_messages AS
SELECT *
FROM messages
WHERE deleted = FALSE;

ALTER TABLE access_tokens
    DROP CONSTRAINT IF EXISTS access_tokens_account_user_fk,
    DROP CONSTRAINT IF EXISTS access_tokens_client_user_fk,
    DROP COLUMN IF EXISTS user_id;

ALTER TABLE sync_states
    DROP CONSTRAINT IF EXISTS sync_states_chat_account_fk;

ALTER TABLE messages
    DROP CONSTRAINT IF EXISTS messages_chat_account_fk;

ALTER TABLE gateway_clients
    DROP CONSTRAINT IF EXISTS gateway_clients_id_user_unique;

ALTER TABLE telegram_accounts
    DROP CONSTRAINT IF EXISTS telegram_accounts_id_user_unique;

ALTER TABLE chats
    DROP CONSTRAINT IF EXISTS chats_id_account_unique;

COMMIT;
