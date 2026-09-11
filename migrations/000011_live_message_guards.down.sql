BEGIN;
DROP TRIGGER IF EXISTS messages_mirror_guard ON messages;
DROP FUNCTION IF EXISTS guard_message_mirror();
ALTER TABLE messages DROP COLUMN IF EXISTS live_content_version;
DROP TABLE IF EXISTS telegram_message_tombstones;
COMMIT;
