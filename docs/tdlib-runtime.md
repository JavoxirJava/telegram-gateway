# TDLib native runtime: implementation checkpoint

This branch adds the native runtime and an **operator-started, one-account live
mirror agent**. It does not yet turn the repository into a complete hosted MCP
product. Public deployment must remain disabled until the release gates below
are completed.

## What is implemented

* A modern `td_create_client_id` / `td_send` / `td_receive` JSON bridge with exactly
  one receiver per process, bounded queues, per-client ordered delivery, request
  correlation, cancellation, and orderly native shutdown. The bridge dynamically
  loads a trusted Linux shared library; ordinary builds do not need TDLib.
* A strict native method allowlist. Telegram send/edit/delete/join operations
  are not exposed. Login is a separate, operator-only control surface.
* Phone, code, two-factor password, email-code and QR authentication states.
  Credentials are forwarded in memory, not persisted to operational logs. QR
  links are credentials too and appear only in the private control response.
* A random per-account TDLib database key, wrapped with AES-256-GCM and bound to
  the account UUID and wrapping-key ID. Private directories, owner-only key
  files, and an exclusive filesystem lock protect a session workspace.
* PostgreSQL ownership leases, periodic renewal, generation/token fencing for
  live writes, and identity activation inside an ownership-checked transaction.
  Reconnecting a different Telegram user to an already bound account is denied.
* An identity gate: received updates are buffered with hard limits until `getMe`
  has been verified against the registered account. No chat data is mirrored
  before this check succeeds.
* Normalization of ordinary cloud-chat events, message text/captions, edits and
  deletions. Cache eviction is not treated as Telegram deletion. Secret chats,
  protected chats and detected expiring content are excluded at ingestion.
* Atomic live mutation plus hash-chain audit in the same PostgreSQL transaction.
  Ordinary deleted rows remain with `deleted=true`. Persistent deletion IDs
  prevent a delayed history response from making a deleted message visible.
* History writers preserve newer live edits. A short history page is no longer
  incorrectly treated as the end of history.

The engine callback must **never synchronously call that same client**: its
response follows the callback in the same ordered queue. Slow persistence
backpressures ingestion instead of silently discarding updates. An unsuccessful
native close requires process termination before a session directory is reused.

## Local prerequisites

Use Linux, the Go version in `go.mod`, a C compiler, and a reviewed TDLib build
exporting the modern JSON symbols. Build TDLib from a **pinned, reviewed commit**
using Telegram's official build instructions; supply the resulting absolute
library path. This repository does not download an unverified binary for you.

Set the existing PostgreSQL environment variables and `TELEGRAM_API_ID` /
`TELEGRAM_API_HASH` through your deployment secret mechanism. Do not commit them
or paste login codes, passwords, encryption keys or QR links into an AI chat.

Apply all migrations to a disposable development database first. The account
must already exist in `telegram_accounts`, belong to its intended `app_users`
record and have status `pending` or `active`. Creation of this record is not
proof of Telegram ownership; the agent performs that check after login.

Create a wrapping key once, outside the repository:

```sh
install -d -m 700 "$HOME/.config/telegram-gateway"
(umask 077; openssl rand -base64 32 > "$HOME/.config/telegram-gateway/master.key")
install -d -m 700 "$HOME/.local/share/telegram-gateway/sessions"
CGO_ENABLED=1 go build -tags tdlib -o bin/tdlib-agent ./cmd/tdlib-agent

./bin/tdlib-agent \
  --account 'CANONICAL-LOWERCASE-ACCOUNT-UUID' \
  --sessions-root "$HOME/.local/share/telegram-gateway/sessions" \
  --library /absolute/path/to/libtdjson.so \
  --master-key-file "$HOME/.config/telegram-gateway/master.key" \
  --enable-live-mirror
```

Do not run the key-generation command again over an existing key. Preserve
secure backups of the wrapping key and workspace. Losing the key prevents
opening the saved TDLib database. Use only a locally owned, non-shared session
filesystem; distributed filesystem lock semantics have not been verified.

The agent listens only at `SESSIONS_ROOT/ACCOUNT_UUID/control.sock` with mode
`0600`, inside a `0700` directory. It has no TCP authentication endpoint.

```sh
curl --unix-socket "$SOCKET" http://localhost/state
curl --unix-socket "$SOCKET" -X POST -H 'Content-Type: application/json' -d '{}' http://localhost/auth/qr
```

Phone/code/password/email/email-code steps use `POST /auth/STEP` with
`{"value":"..."}` as the JSON body. For sensitive values supply the body through
standard input (`curl --data-binary @-`) or a private local UI, rather than shell
arguments/history. Only the Unix-socket owner should perform these operations.
This is **not** an OAuth authorization server or a public account-linking UI.

Stop with SIGTERM. The agent attempts a clean native close; an incomplete close
is reported as an error and its lease/workspace are not explicitly released
before process exit. Replicas still require a shared ownership strategy and
session-state placement; do not start multiple hosts against copied workspaces.

## Tests and what they establish

The development workflow runs module verification, formatting, `go vet`, race
tests, ordinary and native-tagged command builds, and PostgreSQL migration
up/down checks. Its PostgreSQL integration tests exercise retained deletions,
delete-before-history ordering, stale-history/live-edit conflicts, hash-chain
verification, identity rebinding rejection, and lease-loss fencing.

The tagged native test compiles a small **C ABI fixture**, not Telegram's actual
TDLib, to exercise dynamic symbol loading, memory copying and close behavior.
Fake-transport tests exercise parallel responses and authorization state
transitions without a Telegram account. Passing these tests does **not** prove
that a real account can log in or that Telegram flood limits are respected by a
fully integrated worker.

For PostgreSQL integration tests set `TEST_DATABASE_URL` to a disposable,
migrated test database only. These tests insert fixtures intentionally retained
under the mirror/audit rules.

## Release gates still open

1. Implement and test the complete typed `telegram.Session` adapter for history,
   chat-list pagination, contacts/members and media. Wire it to the existing
   JetStream worker through explicit per-account ownership/routing. The new
   native agent currently processes live updates only and does not run that
   historical worker. Media captions/type are mirrored, not attachment bytes.
2. Persist and replay update checkpoints across crashes, reconcile reconnect
   gaps, and handle live edits for messages not yet present in the mirror.
   An in-memory ordered queue is not a durable inbox. Test chat deletion,
   access-loss, protection changes, auto-delete policies and restoration rules
   against a real account before exposing any data.
3. Apply the shared Redis budgets and persistent FLOOD_WAIT rules to **every**
   native adapter request and authentication attempt. The pre-existing worker
   limiter is not automatically a limiter for arbitrary native `Read` calls.
4. Complete MCP transport, OAuth/PKCE, consent/grants, account-scoped tool access
   and the authenticated public control plane. There is no public raw-TDLib
   passthrough endpoint and one must not be added.
5. Replace the existing public MinIO presigned-link delivery with an authorized
   media proxy or equivalent deletion-aware revocation. A previously issued
   presigned link can remain usable until expiry after a soft deletion. Existing
   API views alone do not close that access path.
6. Configure least-privilege database roles, tenant row-level security as needed,
   TLS, secrets rotation/KMS integration, audit checkpoints outside the mutable
   database, encrypted PostgreSQL/media storage, backups and recovery drills.
   Wrapping a TDLib key does **not** encrypt PostgreSQL or MinIO objects. A local
   hash chain does not by itself establish court admissibility or protect against
   a privileged administrator rewriting the entire chain.
7. Run real-TDLib login, restart, identity mismatch, update/edit/delete, duplicate
   owner, lease-expiry and load tests. Obtain appropriate consent and a review of
   Telegram's API/data-use requirements and applicable retention obligations
   before processing other people's personal data in a hosted AI service.

## Primary protocol references

* https://core.telegram.org/tdlib/docs/td__json__client_8h.html
* https://core.telegram.org/tdlib/getting-started
* https://core.telegram.org/tdlib/docs/classtd_1_1td__api_1_1set_tdlib_parameters.html
* https://core.telegram.org/tdlib/docs/classtd_1_1td__api_1_1update_delete_messages.html
* https://core.telegram.org/tdlib/docs/classtd_1_1td__api_1_1get_chat_history.html
