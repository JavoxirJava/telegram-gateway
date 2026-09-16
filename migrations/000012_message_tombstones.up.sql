BEGIN;
CREATE OR REPLACE VIEW active_messages AS
SELECT m.* FROM messages m
JOIN active_chats c ON c.id=m.chat_id AND c.account_id=m.account_id
WHERE m.deleted=FALSE AND NOT EXISTS (
 SELECT 1 FROM message_tombstones t
 WHERE t.account_id=m.account_id AND t.telegram_chat_id=c.telegram_chat_id
 AND t.telegram_message_id=m.telegram_message_id
);
CREATE OR REPLACE VIEW active_message_media AS
SELECT mm.*, m.account_id, m.chat_id FROM message_media mm
JOIN active_messages m ON m.id=mm.message_id;
COMMIT;
