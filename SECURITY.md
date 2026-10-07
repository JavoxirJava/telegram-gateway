# Security

This is self-hosted software that handles Telegram account sessions and a local
message mirror. Run it only on infrastructure you trust. Automated isolation
tests are included; they are not a substitute for an independent security review.

## Reporting a vulnerability

Use GitHub's **Security → Report a vulnerability** on this repository to report
security issues privately. Include reproduction steps using synthetic accounts
and data. Do not put credentials or private messages into a public issue.

Never share `.env`, `deploy/runtime/`, database dumps, TDLib session directories,
media volumes, authentication cookies, access tokens, Telegram codes or passwords.
If credentials are exposed, revoke or rotate them; removing the latest file is
not sufficient because Git history and copies may retain it.

## Boundaries

- MCP and REST enforce account-bound scopes plus default-deny chat permissions.
  Only the owning browser session can edit read/send permissions, with CSRF checks.
  Sending additionally requires `messages:send`; read permission does not grant it.
  No permission-management tools or Telegram-delete tools are exposed to AI clients.
- Chat listing exposes only approved metadata. Message search requires a chat ID,
  and authorization runs before Telegram fetches, previews and media downloads.
  Revocation also invalidates existing media links on their next use.
- Sending uses durable idempotency keys, request auditing and account rate limits.
  Ambiguous sends are not automatically retried. A send already dispatched to
  Telegram cannot be recalled by revoking permissions.
- Preview processing uses bounded input/output and a conversion timeout. FFmpeg
  is restricted to file/pipe protocols and media demuxers, excluding playlists.
  Keep the packaged media decoder patched and run the container as its non-root user.
- Telegram authentication creates a native account session on the host. The
  gateway's scoped access does not make that session a Telegram read-only token.
- The host operator controls the database, media and encryption keys. Isolation
  between API users does not hide stored data from the host operator.
- Each AI receives the tool results you authorize it to read. Local hosting does
  not imply that an external AI processes those results locally.
- Revoking an AI grant stops its gateway access; it does not erase the mirror,
  remove previously delivered AI results, or terminate the Telegram device session.
- Reads refresh approved requested data only. `TELEGRAM_AUTO_SYNC=true` is rejected;
  TDLib's authorized connection and local cache remain active. The gateway does not
  mirror background updates. Tool instructions prohibit polling, but a server cannot
  independently prove a tool call was initiated by a human.
- Storage quotas, automatic retention and a
  self-service account/data deletion workflow are not implemented in this version.

Use loopback HTTP for a local installation or HTTPS for a remote one. Keep the
database, Redis, NATS and MinIO off the public network. Back up the session key
with your private volumes and apply dependency/security updates before exposing
an installation to other users.
