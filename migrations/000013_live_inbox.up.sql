BEGIN;
CREATE TABLE gateway_live_inbox (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
 event_key UUID NOT NULL,
 payload JSONB NOT NULL,
 received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 applied_at TIMESTAMPTZ,
 UNIQUE(account_id,event_key)
);
CREATE INDEX gateway_live_inbox_pending ON gateway_live_inbox(account_id,id) WHERE applied_at IS NULL;
COMMENT ON TABLE gateway_live_inbox IS 'Private normalized-event journal. No raw Telegram credentials, no public read grants.';
COMMIT;
