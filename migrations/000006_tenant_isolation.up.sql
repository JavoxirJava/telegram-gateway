BEGIN;

ALTER TABLE chats
    ADD CONSTRAINT chats_id_account_unique UNIQUE (id, account_id);

ALTER TABLE telegram_accounts
    ADD CONSTRAINT telegram_accounts_id_user_unique UNIQUE (id, user_id);

ALTER TABLE gateway_clients
    ADD CONSTRAINT gateway_clients_id_user_unique UNIQUE (id, user_id);

ALTER TABLE messages
    ADD CONSTRAINT messages_chat_account_fk
    FOREIGN KEY (chat_id, account_id)
    REFERENCES chats(id, account_id)
    ON DELETE RESTRICT;

ALTER TABLE sync_states
    ADD CONSTRAINT sync_states_chat_account_fk
    FOREIGN KEY (chat_id, account_id)
    REFERENCES chats(id, account_id)
    ON DELETE RESTRICT;

ALTER TABLE access_tokens ADD COLUMN user_id UUID;

UPDATE access_tokens at
SET user_id = gc.user_id
FROM gateway_clients gc
WHERE gc.id = at.client_id;

ALTER TABLE access_tokens
    ALTER COLUMN user_id SET NOT NULL,
    ADD CONSTRAINT access_tokens_client_user_fk
        FOREIGN KEY (client_id, user_id)
        REFERENCES gateway_clients(id, user_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT access_tokens_account_user_fk
        FOREIGN KEY (account_id, user_id)
        REFERENCES telegram_accounts(id, user_id)
        ON DELETE RESTRICT;

CREATE OR REPLACE VIEW active_messages AS
SELECT m.*
FROM messages m
JOIN chats c
  ON c.id = m.chat_id
 AND c.account_id = m.account_id
WHERE m.deleted = FALSE
  AND c.deleted = FALSE;

COMMENT ON VIEW active_messages IS 'Normal API/MCP projection. Hides soft-deleted messages and every message belonging to a soft-deleted chat.';

COMMIT;
