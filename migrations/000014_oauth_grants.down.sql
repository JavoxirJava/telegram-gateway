BEGIN;
DROP TABLE gateway_oauth_grants;
DROP FUNCTION guard_oauth_binding();
DROP TABLE gateway_oauth_subjects;
COMMIT;
