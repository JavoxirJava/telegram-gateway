BEGIN;

DROP VIEW IF EXISTS active_messages;
DROP VIEW IF EXISTS active_chats;

DROP TRIGGER IF EXISTS audit_logs_no_delete ON audit_logs;
DROP TRIGGER IF EXISTS audit_logs_no_update ON audit_logs;
DROP FUNCTION IF EXISTS prevent_audit_log_mutation();

DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS sync_states;
DROP TABLE IF EXISTS message_media;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS chats;
DROP TABLE IF EXISTS telegram_accounts;
DROP TABLE IF EXISTS app_users;

COMMIT;
