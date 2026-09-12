BEGIN;
CREATE TABLE gateway_oauth_subjects (
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL,
 user_id UUID NOT NULL REFERENCES app_users(id) ON DELETE RESTRICT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(issuer,subject),
 UNIQUE(issuer,subject,user_id)
);
CREATE TABLE gateway_oauth_grants (
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL,
 oauth_client_id TEXT NOT NULL,
 user_id UUID NOT NULL,
 account_id UUID NOT NULL,
 gateway_client_id UUID NOT NULL,
 scopes TEXT[] NOT NULL CHECK(cardinality(scopes)>0 AND scopes <@ ARRAY['profile:read','chats:list','chat:read','messages:read','messages:search','contacts:read','media:read','members:read']::text[]),
 consent_reference TEXT NOT NULL CHECK(length(consent_reference)>0),
 revoked_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY(issuer,subject,oauth_client_id),
 FOREIGN KEY(issuer,subject,user_id) REFERENCES gateway_oauth_subjects(issuer,subject,user_id) ON DELETE RESTRICT,
 FOREIGN KEY(account_id,user_id) REFERENCES telegram_accounts(id,user_id) ON DELETE RESTRICT,
 FOREIGN KEY(gateway_client_id,user_id) REFERENCES gateway_clients(id,user_id) ON DELETE RESTRICT
);
CREATE FUNCTION guard_oauth_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.issuer,NEW.subject,NEW.oauth_client_id,NEW.user_id,NEW.account_id,NEW.gateway_client_id)
 IS DISTINCT FROM ROW(OLD.issuer,OLD.subject,OLD.oauth_client_id,OLD.user_id,OLD.account_id,OLD.gateway_client_id) THEN
 RAISE EXCEPTION 'OAuth subject/client/account binding is immutable'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER oauth_binding_immutable BEFORE UPDATE ON gateway_oauth_grants FOR EACH ROW EXECUTE FUNCTION guard_oauth_binding();
COMMIT;
