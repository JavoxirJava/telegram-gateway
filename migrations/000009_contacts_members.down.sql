BEGIN;

DROP VIEW IF EXISTS active_chat_members;
DROP VIEW IF EXISTS active_contacts;
DROP TABLE IF EXISTS chat_members;
DROP TABLE IF EXISTS telegram_contacts;

COMMIT;
