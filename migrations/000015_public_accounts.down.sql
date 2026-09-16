BEGIN;
ALTER TABLE oauth_requests DROP CONSTRAINT oauth_requests_account_user_fk;
ALTER TABLE oauth_requests DROP COLUMN user_id, DROP COLUMN account_id;
DROP TABLE browser_sessions;
DROP TABLE telegram_identities;
ALTER TABLE telegram_accounts DROP CONSTRAINT telegram_accounts_identity_unique;
ALTER TABLE telegram_accounts DROP COLUMN session_directory_id;
COMMIT;
