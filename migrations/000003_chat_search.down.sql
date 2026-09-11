BEGIN;

DROP INDEX IF EXISTS idx_chats_active_username_trgm;
DROP INDEX IF EXISTS idx_chats_active_title_trgm;

COMMIT;
