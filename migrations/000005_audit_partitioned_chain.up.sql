BEGIN;

ALTER TABLE audit_logs
    ADD COLUMN chain_key TEXT NOT NULL DEFAULT 'global';

CREATE INDEX idx_audit_logs_chain
    ON audit_logs(chain_key, id DESC);

COMMENT ON COLUMN audit_logs.chain_key IS 'Independent tamper-evident chain. Account-scoped events use the Telegram account UUID; global events use global.';

COMMIT;
