# Changelog

## [2.0.1](https://github.com/JavoxirJava/telegram-gateway/releases/tag/v2.0.1) — 2026-10-07

- Fix CI and fresh-install instructions after the former MinIO registries stopped serving the pinned image. `Dockerfile.minio` builds the same upstream source release from its verified immutable commit.
- Preserve the v2.0.0 feature set and upgrade requirements below. Existing running MinIO instances and storage volumes do not need to be replaced for this gateway update.
- Runtime release metadata now reports `2.0.1`. This is the recommended release for new installations and upgrades.

## [2.0.0](https://github.com/JavoxirJava/telegram-gateway/releases/tag/v2.0.0) — 2026-10-07

This release makes chat access opt-in and adds visual media inspection and controlled text sending.

### Added

- Per-chat **read** and **send** permissions managed only by the account owner in `/account`.
- `inspect_media`: native MCP image content for photos and individual video frames at requested timestamps. Audio transcription is not included.
- `send_message`: plain-text sending with a separate `messages:send` scope, chat permission, rate limits, audit records and durable request IDs to prevent duplicate dispatch on retries.
- Telegram-inspired account management, searchable chat list, permission switches, mobile layouts and dark mode.
- `/version`, version response headers, MCP server version and container release labels for identifying the running release and source commit.
- Regression tests for default-deny access, account isolation, permission revocation, duplicate sends, image previews and video decoding.

### Breaking changes

- **All chats are denied initially, including previously cached chats.** Existing tokens do not bypass this policy. Use `/account` to enable access explicitly.
- AI chat listing and chat search return only approved cached metadata. Chat discovery is an explicit browser action and does not fetch histories.
- `search_messages` requires `chat_id`; account-wide message searches are no longer available.
- `TELEGRAM_AUTO_SYNC=true` is rejected. Set it to `false`; gateway background mirroring and queued history processing remain disabled.
- Existing OAuth grants do not gain sending privileges automatically. Reconnect and consent to `messages:send`, or create a personal token with its send checkbox enabled.

### Upgrade

1. Back up the database and private configuration, then check out `v2.0.0`.
2. Keep `TELEGRAM_AUTO_SYNC=false`. Build the updated container; it includes FFmpeg. Native installations need FFmpeg on PATH.
3. Apply migration `000016_chat_permissions` before starting the new API. The provided installer applies migrations and runtime table grants.
4. Open `/account`, load chat names, and enable only the required read/send permissions. Both start disabled.
5. Reconnect AI clients that need sending. Use a new UUID `request_id` for each intended message, and reuse it on retries. `accepted` is not a final-delivery confirmation; ambiguous sends are never automatically dispatched again.
6. Check `/version` and `/health/ready`. The version must be `2.0.0`, and `revision` should match the checked-out Git commit when built with `VCS_REF`.

Chat revocation blocks subsequent reads and previously issued media download links. Already returned content and messages already dispatched to Telegram cannot be recalled by revocation. TDLib retains its own authorized connection and local cache.

### Validation

Go formatting, vet, race-enabled unit/integration tests, FFmpeg frame decoding, and browser checks at 390, 768 and 1440 pixels. Telegram sending is tested with a fake session; no real messages are sent by the test suite.

## Previous untagged builds

The original read-only gateway, account-scoped REST/MCP tools, OAuth, private media links and local Podman deployment were published without a release tag. Version 2.0.0 is the first tagged release; its major version reflects the access and API changes above.
