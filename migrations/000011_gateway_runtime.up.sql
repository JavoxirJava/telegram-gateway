BEGIN;
CREATE TABLE gateway_settings (
 key TEXT PRIMARY KEY,
 value TEXT NOT NULL
);
CREATE TABLE telegram_updates (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account_id UUID NOT NULL REFERENCES telegram_accounts(id),
 payload JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_telegram_updates_account ON telegram_updates(account_id,id);
CREATE TABLE message_tombstones (
 account_id UUID NOT NULL REFERENCES telegram_accounts(id),
 telegram_chat_id BIGINT NOT NULL,
 telegram_message_id BIGINT NOT NULL,
 deleted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(account_id,telegram_chat_id,telegram_message_id)
);
CREATE TABLE oauth_clients (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 redirect_uris TEXT[] NOT NULL,
 secret_hash BYTEA,
 auth_method TEXT NOT NULL DEFAULT 'none',
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE oauth_requests (
 id TEXT PRIMARY KEY,
 client_id TEXT NOT NULL REFERENCES oauth_clients(id),
 redirect_uri TEXT NOT NULL,
 state TEXT NOT NULL,
 challenge TEXT NOT NULL,
 scopes TEXT[] NOT NULL,
 csrf_hash BYTEA NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE oauth_codes (
 code_hash BYTEA PRIMARY KEY,
 client_id TEXT NOT NULL REFERENCES oauth_clients(id),
 gateway_client_id UUID NOT NULL REFERENCES gateway_clients(id),
 account_id UUID NOT NULL REFERENCES telegram_accounts(id),
 redirect_uri TEXT NOT NULL,
 challenge TEXT NOT NULL,
 scopes TEXT[] NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE oauth_refresh_tokens (
 token_hash BYTEA PRIMARY KEY,
 client_id TEXT NOT NULL REFERENCES oauth_clients(id),
 gateway_client_id UUID NOT NULL REFERENCES gateway_clients(id),
 account_id UUID NOT NULL REFERENCES telegram_accounts(id),
 scopes TEXT[] NOT NULL,
 expires_at TIMESTAMPTZ NOT NULL,
 revoked_at TIMESTAMPTZ
);
CREATE INDEX idx_oauth_requests_expiry ON oauth_requests(expires_at);
CREATE INDEX idx_oauth_codes_expiry ON oauth_codes(expires_at);
CREATE INDEX idx_oauth_refresh_expiry ON oauth_refresh_tokens(expires_at);
COMMIT;
