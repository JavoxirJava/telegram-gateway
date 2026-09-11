BEGIN;

CREATE INDEX IF NOT EXISTS idx_chats_active_title_trgm
    ON chats USING GIN ((COALESCE(title, '')) gin_trgm_ops)
    WHERE deleted = FALSE;

CREATE INDEX IF NOT EXISTS idx_chats_active_username_trgm
    ON chats USING GIN ((COALESCE(username, '')) gin_trgm_ops)
    WHERE deleted = FALSE;

COMMIT;
