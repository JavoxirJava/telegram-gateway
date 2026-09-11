BEGIN;

DROP INDEX IF EXISTS idx_message_media_telegram_file;
DROP INDEX IF EXISTS idx_message_media_unique_file_key;

CREATE UNIQUE INDEX idx_message_media_unique_file_key
    ON message_media(unique_file_key)
    WHERE unique_file_key IS NOT NULL;

COMMIT;
