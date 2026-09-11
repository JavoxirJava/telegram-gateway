BEGIN;

DROP INDEX IF EXISTS idx_message_media_unique_file_key;

CREATE INDEX idx_message_media_unique_file_key
    ON message_media(unique_file_key)
    WHERE unique_file_key IS NOT NULL;

CREATE INDEX idx_message_media_telegram_file
    ON message_media(telegram_file_id)
    WHERE telegram_file_id IS NOT NULL;

COMMIT;
