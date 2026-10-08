BEGIN;
ALTER TABLE oauth_clients ADD COLUMN activated_at TIMESTAMPTZ;
-- Preserve every existing client with a grant, including revoked grants.
UPDATE oauth_clients c SET activated_at=c.created_at
WHERE EXISTS (SELECT 1 FROM oauth_codes r WHERE r.client_id=c.id)
   OR EXISTS (SELECT 1 FROM oauth_refresh_tokens r WHERE r.client_id=c.id);
CREATE INDEX idx_oauth_clients_unused ON oauth_clients(created_at,id) WHERE activated_at IS NULL;
CREATE INDEX idx_oauth_requests_client ON oauth_requests(client_id);
CREATE INDEX idx_oauth_codes_client ON oauth_codes(client_id);
CREATE INDEX idx_oauth_refresh_client ON oauth_refresh_tokens(client_id);
COMMIT;
