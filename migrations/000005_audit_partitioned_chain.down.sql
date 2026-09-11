BEGIN;

DROP INDEX IF EXISTS idx_audit_logs_chain;
ALTER TABLE audit_logs DROP COLUMN IF EXISTS chain_key;

COMMIT;
