# Deletion-aware media delivery

`GET /v1/media/{mediaID}/content` (also HEAD) replaces signed MinIO URLs.
A valid account-bound bearer token with `media:read` is required on every request.
The legacy `/url` endpoint returns 410; it does not redirect or issue a capability.

The service resolves the media through `active_message_media`, checks that the
object key belongs to the account and media UUID, and checks the stored size.
An audit event must commit before any response bytes are sent. Downloads use
attachment/octet-stream, nosniff, no-store and sandbox headers. Ranges and 304
responses are intentionally not supported in this initial proxy implementation.

Before each read of at most 64 KiB, both current token/grant validity and active
media state are checked again. Deletion of the message/chat, token revocation,
permission loss or a database outage stops the stream. An interrupted body is
aborted instead of being presented as a complete successful download.

This is not retroactive revocation: already delivered bytes cannot be recalled,
and an already authorized chunk may finish if deletion races that chunk.
There is no claim of an atomic transaction spanning a database and the network.
The repeated checks favor conservative access control over peak throughput;
measure the database cost before changing the recheck interval.

## Deployment transition

Keep the bucket private and MinIO reachable only by backend services. Do not
publish port 9000, the console, object paths, public bucket policies or a CDN
bypass. Existing development signed links are NOT retroactively invalidated by
a code change: before deployment, revoke/rotate the credential that signed old
URLs or keep object storage unreachable from clients. Do not delete retained
objects to implement revocation. Production data encryption remains a separate
release gate; this proxy does not configure KMS or change bucket encryption.

Tests cover missing/deleted media, token revocation, per-read limits, cross-account
object keys, truncated/mismatched storage, audit outages, scope checks, headers,
HEAD/range handling, and retirement of the signed-link endpoint. Real MinIO,
concurrent database deletion and deployed reverse-proxy behavior also require
end-to-end validation before public release.
