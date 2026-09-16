BEGIN;

DROP INDEX idx_message_media_message_file_unique;
CREATE UNIQUE INDEX idx_message_media_stable_file_unique
    ON message_media(message_id, media_type, unique_file_key)
    WHERE unique_file_key IS NOT NULL;
CREATE UNIQUE INDEX idx_message_media_message_file_unique
    ON message_media(message_id, media_type, telegram_file_id)
    WHERE telegram_file_id IS NOT NULL AND unique_file_key IS NULL;

COMMIT;
