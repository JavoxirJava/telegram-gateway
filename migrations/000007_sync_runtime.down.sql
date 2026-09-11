BEGIN;

DROP VIEW IF EXISTS active_message_media;

ALTER TABLE message_media
    DROP CONSTRAINT IF EXISTS message_media_attempt_count_check,
    DROP COLUMN IF EXISTS last_error,
    DROP COLUMN IF EXISTS downloaded_at,
    DROP COLUMN IF EXISTS last_attempt_at,
    DROP COLUMN IF EXISTS attempt_count;

DROP INDEX IF EXISTS idx_sync_states_lease;
ALTER TABLE sync_states
    DROP CONSTRAINT IF EXISTS sync_states_lease_check,
    DROP CONSTRAINT IF EXISTS sync_states_generation_check,
    DROP COLUMN IF EXISTS generation,
    DROP COLUMN IF EXISTS lease_expires_at,
    DROP COLUMN IF EXISTS lease_token,
    DROP COLUMN IF EXISTS lease_owner;

DROP TABLE IF EXISTS telegram_session_runtime;

COMMIT;
