BEGIN;

DROP INDEX IF EXISTS idx_messages_active_content_trgm;
DROP EXTENSION IF EXISTS pg_trgm;

COMMIT;
