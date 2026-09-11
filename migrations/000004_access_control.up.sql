BEGIN;

CREATE TABLE gateway_clients (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES app_users(id) ON DELETE RESTRICT,
    name TEXT NOT NULL,
    client_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT gateway_clients_type_check CHECK (client_type IN ('MCP', 'API')),
    CONSTRAINT gateway_clients_status_check CHECK (status IN ('active', 'disabled')),
    CONSTRAINT gateway_clients_name_check CHECK (length(trim(name)) BETWEEN 1 AND 120)
);

CREATE INDEX idx_gateway_clients_user ON gateway_clients(user_id, created_at DESC);

CREATE TABLE access_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id UUID NOT NULL REFERENCES gateway_clients(id) ON DELETE RESTRICT,
    account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    token_prefix TEXT NOT NULL,
    token_hash BYTEA NOT NULL,
    scopes TEXT[] NOT NULL,
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT access_tokens_hash_unique UNIQUE (token_hash),
    CONSTRAINT access_tokens_prefix_check CHECK (length(token_prefix) BETWEEN 8 AND 24),
    CONSTRAINT access_tokens_scopes_nonempty CHECK (cardinality(scopes) > 0),
    CONSTRAINT access_tokens_read_scopes_only CHECK (
        scopes <@ ARRAY[
            'profile:read',
            'chats:list',
            'chat:read',
            'messages:read',
            'messages:search',
            'contacts:read',
            'media:read',
            'members:read'
        ]::TEXT[]
    )
);

CREATE INDEX idx_access_tokens_client ON access_tokens(client_id, created_at DESC);
CREATE INDEX idx_access_tokens_account ON access_tokens(account_id, created_at DESC);
CREATE INDEX idx_access_tokens_active_prefix
    ON access_tokens(token_prefix)
    WHERE revoked_at IS NULL;

COMMENT ON TABLE access_tokens IS 'Bearer credentials are never stored in plaintext. token_hash is SHA-256 of the random bearer token.';

COMMIT;
