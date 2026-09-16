BEGIN;
ALTER TABLE telegram_accounts ADD COLUMN session_directory_id UUID;
UPDATE telegram_accounts SET session_directory_id=id;
ALTER TABLE telegram_accounts ALTER COLUMN session_directory_id SET NOT NULL;
ALTER TABLE telegram_accounts ALTER COLUMN session_directory_id SET DEFAULT gen_random_uuid();
ALTER TABLE telegram_accounts ADD CONSTRAINT telegram_accounts_identity_unique UNIQUE(id,telegram_user_id);
CREATE TABLE telegram_identities (
 telegram_user_id BIGINT PRIMARY KEY CHECK(telegram_user_id>0),
 account_id UUID NOT NULL UNIQUE,
 FOREIGN KEY(account_id,telegram_user_id) REFERENCES telegram_accounts(id,telegram_user_id)
);
INSERT INTO telegram_identities(telegram_user_id,account_id)
 SELECT DISTINCT ON(telegram_user_id) telegram_user_id,id FROM telegram_accounts
 WHERE telegram_user_id IS NOT NULL ORDER BY telegram_user_id,created_at,id;
CREATE TABLE browser_sessions (
 token_hash BYTEA PRIMARY KEY,
 user_id UUID NOT NULL,
 account_id UUID NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 expires_at TIMESTAMPTZ NOT NULL,
 FOREIGN KEY(account_id,user_id) REFERENCES telegram_accounts(id,user_id)
);
CREATE INDEX idx_browser_sessions_expiry ON browser_sessions(expires_at);
ALTER TABLE oauth_requests ADD COLUMN user_id UUID;
ALTER TABLE oauth_requests ADD COLUMN account_id UUID;
ALTER TABLE oauth_requests ADD CONSTRAINT oauth_requests_account_user_fk
 FOREIGN KEY(account_id,user_id) REFERENCES telegram_accounts(id,user_id);
COMMIT;
