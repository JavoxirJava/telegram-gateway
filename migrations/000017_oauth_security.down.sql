BEGIN;
DROP INDEX idx_oauth_refresh_client, idx_oauth_codes_client, idx_oauth_requests_client, idx_oauth_clients_unused;
ALTER TABLE oauth_clients DROP COLUMN activated_at;
COMMIT;
