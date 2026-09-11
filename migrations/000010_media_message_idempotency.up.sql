BEGIN;

CREATE UNIQUE INDEX idx_message_media_message_file_unique
    ON message_media(message_id, media_type, telegram_file_id)
    WHERE telegram_file_id IS NOT NULL;

COMMIT;
