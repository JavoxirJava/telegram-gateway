BEGIN;
DROP TABLE message_send_requests;
DROP TABLE chat_permissions;
ALTER TABLE access_tokens DROP CONSTRAINT access_tokens_allowed_scopes;
UPDATE access_tokens SET revoked_at=COALESCE(revoked_at,NOW()),scopes=array_remove(scopes,'messages:send') WHERE 'messages:send'=ANY(scopes) AND cardinality(scopes)>1;
UPDATE access_tokens SET revoked_at=COALESCE(revoked_at,NOW()),scopes=ARRAY['profile:read']::TEXT[] WHERE scopes=ARRAY['messages:send']::TEXT[];
ALTER TABLE access_tokens ADD CONSTRAINT access_tokens_read_scopes_only CHECK (scopes <@ ARRAY['profile:read','chats:list','chat:read','messages:read','messages:search','contacts:read','media:read','members:read']::TEXT[]);
COMMIT;
