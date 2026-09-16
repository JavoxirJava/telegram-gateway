BEGIN;
CREATE TABLE media_read_tickets (
 ticket_hash BYTEA PRIMARY KEY,
 token_hash BYTEA NOT NULL REFERENCES access_tokens(token_hash),
 media_id UUID NOT NULL REFERENCES message_media(id),
 expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_media_read_tickets_expiry ON media_read_tickets(expires_at);
COMMIT;
