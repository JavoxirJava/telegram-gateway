BEGIN;

CREATE TABLE telegram_message_tombstones (
    account_id UUID NOT NULL REFERENCES telegram_accounts(id) ON DELETE RESTRICT,
    telegram_chat_id BIGINT NOT NULL,
    telegram_message_id BIGINT NOT NULL,
    deleted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, telegram_chat_id, telegram_message_id)
);

ALTER TABLE messages ADD COLUMN live_content_version BIGINT NOT NULL DEFAULT 0
    CHECK (live_content_version >= 0);

CREATE FUNCTION guard_message_mirror() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE tombstone_time TIMESTAMPTZ;
BEGIN
    SELECT t.deleted_at INTO tombstone_time
    FROM telegram_message_tombstones t
    JOIN chats c ON c.account_id=t.account_id AND c.telegram_chat_id=t.telegram_chat_id
    WHERE c.id=NEW.chat_id AND t.account_id=NEW.account_id
      AND t.telegram_message_id=NEW.telegram_message_id;
    IF tombstone_time IS NOT NULL THEN
        NEW.deleted := TRUE;
        NEW.deleted_at := COALESCE(NEW.deleted_at, tombstone_time);
    END IF;
    IF TG_OP='UPDATE' THEN
        IF OLD.deleted THEN
            NEW.deleted := TRUE;
            NEW.deleted_at := OLD.deleted_at;
        END IF;
        -- A history snapshot must not overwrite a live edit. Live writers
        -- increment the version; history writers never do.
        IF OLD.deleted OR (OLD.live_content_version > 0 AND NEW.live_content_version <= OLD.live_content_version)
           OR (OLD.edited_at IS NOT NULL AND (NEW.edited_at IS NULL OR NEW.edited_at < OLD.edited_at)) THEN
            NEW.content := OLD.content;
            NEW.content_entities := OLD.content_entities;
            NEW.message_type := OLD.message_type;
            NEW.forward_info := OLD.forward_info;
            NEW.raw_metadata := OLD.raw_metadata;
            NEW.edited_at := OLD.edited_at;
            NEW.live_content_version := OLD.live_content_version;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER messages_mirror_guard BEFORE INSERT OR UPDATE ON messages
    FOR EACH ROW EXECUTE FUNCTION guard_message_mirror();

COMMENT ON TABLE telegram_message_tombstones IS 'Deletion identities prevent a delayed history response from resurrecting a deleted message.';
COMMIT;
