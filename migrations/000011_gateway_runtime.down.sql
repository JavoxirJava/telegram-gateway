BEGIN;
DROP TABLE oauth_refresh_tokens, oauth_codes, oauth_requests, oauth_clients;
DROP TABLE message_tombstones, telegram_updates, gateway_settings;
COMMIT;
