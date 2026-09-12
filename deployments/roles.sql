-- Run once as a database administrator AFTER gateway-migrate.
-- These group roles have no login; create separate login users and GRANT the
-- corresponding group. NEVER run the API as the database owner/superuser.
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='gateway_api') THEN CREATE ROLE gateway_api NOLOGIN; END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='gateway_agent') THEN CREATE ROLE gateway_agent NOLOGIN; END IF;
END $$;
GRANT USAGE ON SCHEMA public TO gateway_api,gateway_agent;
GRANT SELECT ON active_chats,active_messages,active_message_media,active_chat_members,active_contacts,
 telegram_accounts,gateway_clients,access_tokens,gateway_oauth_grants,gateway_oauth_subjects,
 telegram_session_runtime,gateway_sync_jobs,gateway_history_progress TO gateway_api;
-- Status reads only journal timestamps/IDs, never its retained content payload.
GRANT SELECT(account_id,received_at,applied_at) ON gateway_live_inbox TO gateway_api;
GRANT SELECT,INSERT ON audit_logs TO gateway_api;
GRANT USAGE,SELECT ON SEQUENCE audit_logs_id_seq TO gateway_api;
GRANT SELECT,INSERT,UPDATE ON telegram_accounts,chats,messages,message_media,
 telegram_contacts,chat_members,telegram_session_runtime,sync_states,
 telegram_message_tombstones,gateway_sync_jobs,gateway_history_progress,gateway_live_inbox TO gateway_agent;
GRANT SELECT ON active_chats,active_messages,active_message_media,active_chat_members,active_contacts TO gateway_agent;
GRANT SELECT,INSERT ON audit_logs TO gateway_agent;
GRANT USAGE,SELECT ON SEQUENCE audit_logs_id_seq,gateway_live_inbox_id_seq TO gateway_agent;
-- No DELETE, TRUNCATE, DDL, access to wrapped key files, or base messages/chats
-- grants are given to the API role. Migration/operator credentials stay offline.
ALTER VIEW active_chats SET (security_barrier=true);
ALTER VIEW active_messages SET (security_barrier=true);
ALTER VIEW active_message_media SET (security_barrier=true);
ALTER VIEW active_chat_members SET (security_barrier=true);
ALTER VIEW active_contacts SET (security_barrier=true);
