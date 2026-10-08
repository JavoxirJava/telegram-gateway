# Unofficial Telegram Gateway

[![CI](https://github.com/JavoxirJava/telegram-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/JavoxirJava/telegram-gateway/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/JavoxirJava/telegram-gateway)](https://github.com/JavoxirJava/telegram-gateway/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Current release: [v2.0.2](https://github.com/JavoxirJava/telegram-gateway/releases/tag/v2.0.2)** — OAuth security fixes, chat permissions, image/video-frame inspection and controlled sending.
Read the [changelog and upgrade notes](CHANGELOG.md) before updating: existing chats start with no AI access.

A self-hosted Telegram gateway with a **permission-controlled REST API and MCP server**.
Search your synchronized messages, list chats and contacts, and retrieve message
attachments from AI clients or your own scripts.

TDLib connects to Telegram. PostgreSQL stores the mirror, MinIO stores media,
Redis handles rate limits. Legacy NATS jobs are retained but background sync is disabled.
This is an independent project, not affiliated with Telegram or an AI provider.

## Try the hosted MCP

You can try the running gateway without installing the project:

| Entry point | URL |
|---|---|
| MCP server | **https://tg-geteway.javohir-dev.uz/mcp** |
| Telegram sign-in | https://tg-geteway.javohir-dev.uz/login |
| Your account, tokens and connected AI clients | https://tg-geteway.javohir-dev.uz/account |

1. Add the MCP server URL to your AI client's custom MCP/connector settings.
   See [client examples below](#connect-an-ai-client).
2. Start the client's OAuth connection. In the browser page it opens, sign in
   with **your own Telegram account** and approve the displayed scopes.
   No personal Telegram API ID/hash is needed for this hosted trial.
3. Open `/account`, click **Telegramdan yuklash**, select a chat, and enable
   **Chatni o‘qish** and/or **Xabar yuborish**. Every chat is denied initially,
   including chats cached before this upgrade. Loading this list fetches metadata
   only; it does not read history.
4. Return to the AI client and enable the connection. Try asking:
   **"Use Telegram Gateway to show my profile and list my first 5 chats."**
   This exercises `get_profile` and `list_chats`. Reads refresh requested data;
   Telegram rate limits or an unready session produce a retryable error.
5. Manage personal tokens or revoke an AI client's grant from your account page.
   You can connect the same Telegram account from another AI client; each client
   has its own grant and sees only the account it was authorized to access.

Opening `/mcp` directly in a browser without authentication returns
`401 unauthorized`; that is expected. Use an MCP client's OAuth flow, or a
personal bearer token created on your account page.

This is a hosted test instance; availability depends on the maintainer's server.
AI/API reads store requested data on that server, whose operator controls the
stored data. Read [Before connecting an account](#before-connecting-an-account)
before signing in. For your own installation, follow the Linux quick start below.

## What works

- Phone/code, two-step verification, email verification and QR login through TDLib.
- Multi-user sign-in: verified Telegram identities map to separate accounts;
  returning users recover their existing mirror.
- On-demand chat metadata, history, contacts, members, search and requested media.
- REST pagination/search and 12 account-scoped MCP tools, including image/video-frame inspection and permission-controlled sending.
- OAuth discovery, dynamic client registration, PKCE S256, rotating refresh
  tokens and per-client revocation; personal API tokens are also supported.
- Private media download links, account isolation tests, audit chains,
  checksummed database migrations and restart-safe session storage.
- Linux deployment with rootless Podman Quadlet and an optional HTTPS tunnel.

## Before connecting an account

**Automatic synchronization is disabled (`TELEGRAM_AUTO_SYNC=false`).**
Starting with `TELEGRAM_AUTO_SYNC=true` now fails configuration validation.
No gateway background history, live-event mirroring, or media downloads run.

Only browser-authenticated account owners can change chat permissions at `/account`.
Read and send permissions are independent and apply to all clients of that account;
clients also need the appropriate OAuth/token scope. Bearer tokens cannot edit
permissions. Revocation blocks subsequent REST/MCP reads and existing media links.
A request already dispatched to Telegram cannot be recalled, and content already
returned to an AI cannot be erased by revoking access.

AI chat listing/search returns only approved cached chat metadata and does not
query Telegram. Explicit browser **Telegramdan yuklash** fetches one metadata page;
**Yana yuklash** fetches the next, and browser search finds chats by name. Message
reads and searches query only an approved chat on demand, with a 45-second budget.
`search_messages` now requires `chat_id`; account-wide message search is disabled.
Attachments are fetched only when requested. Contacts remain separately controlled
by `contacts:read`. MCP instructions prohibit automatic polling; the gateway cannot
independently verify whether a client's individual tool call followed a human prompt.

Local Telegram limits are 60 account operations/minute (burst 15), 20 history/list
operations/minute per method (burst 6), and 10 searches/minute per method (burst 3).
Attachment limits stay unchanged. Telegram FLOOD_WAIT cooldowns still apply.

Existing cache and legacy queued jobs are retained. Background workers and update
journal processing are paused and cannot be enabled through configuration.
Telegram connections remain online and TDLib maintains its own session/cache;
gateway live events are not mirrored into PostgreSQL in on-demand mode.
Storage quotas, automatic retention and self-service account deletion are not implemented.

The operator controls the host and its stored data. Keep `.env`, session keys,
TDLib directories, backups and media private. AI clients receive the tool results
you authorize; self-hosting does not make an external AI's processing local.
See [SECURITY.md](SECURITY.md) for the access boundaries and reporting process.

Using the source does not grant rights to Telegram content. Review the
[Telegram API terms](https://core.telegram.org/api/terms) and
[content/AI terms](https://telegram.org/tos/content-licensing), including the
content-specific consent conditions. A gateway login does not establish consent
from everyone whose messages appear in a chat. This repository is not a claim of
approval by Telegram, OpenAI, Anthropic or any directory.

## Quick start: your own Linux machine

Requirements: rootless Podman with Quadlet/cgroup v2, user systemd and Python 3.
The image builds Go and native TDLib; a host Go installation is only needed for
local development/tests. TDLib's first build can take several minutes.

```bash
git clone https://github.com/JavoxirJava/telegram-gateway.git
cd telegram-gateway
cp .env.example .env
```

Create your own application at <https://my.telegram.org/apps>. Set
`TELEGRAM_API_ID` and `TELEGRAM_API_HASH` in the local `.env`, then run:

```bash
python3 deploy/configure.py
# Build the pinned MinIO dependency from source; the old registry image is unavailable.
podman build --layers -f Dockerfile.minio \
  -t quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z .
podman build --layers -f Dockerfile.tdlib -t localhost/telegram-gateway-tdlib:d1085f9 .
podman build --layers --build-arg VCS_REF="$(git rev-parse HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t localhost/telegram-gateway:local .
python3 deploy/install.py
```

The service listens on **http://127.0.0.1:8086**. PostgreSQL, Redis, NATS and MinIO
have no published host ports. These scripts install one `tgw-*` stack per Linux
user and preserve existing volumes.

- `/version`: running release version, Git commit and build timestamp.
- `/health/ready`: dependency checks.
- `/login`: sign in to your Telegram account; background sync stays off by default.
- `/account`: per-chat read/send permissions, personal tokens and AI grants.
- `/manage`: operator panel; use `GATEWAY_ADMIN_TOKEN` from your local `.env`.
- `/mcp`: Streamable HTTP MCP endpoint. A tokenless browser request returns 401
  with OAuth discovery information; add this URL to an MCP client.

`configure.py` generates secrets locally and preserves existing credentials and
`PUBLIC_URL`. No application credentials, admin tokens or Telegram sessions are
provided by this repository. Do not share the admin token with AI clients.

### Optional: a public HTTPS endpoint

Hosted AI clients need to reach your own server over HTTPS. Configure an origin
you control before installing/restarting the service:

```bash
python3 deploy/configure.py --public-url https://gateway.example.com
python3 deploy/install.py
```

Route that hostname to `http://127.0.0.1:8086` with your HTTPS reverse proxy.
Alternatively, after installing and authenticating `cloudflared` for your domain:

```bash
python3 deploy/tunnel.py
```

The tunnel helper uses the hostname from `.env` and installs `tgw-tunnel.service`.
It does not replace an existing DNS record unless `--overwrite-dns` is supplied.
The machine must remain powered on and connected for remote clients to work.

### Optional: development Compose stack

After building both images and generating `.env`, run `podman compose up -d` with
a Compose provider installed. `docker-compose.yml` uses separate volumes and
`http://127.0.0.1:18086`. Its migration container initializes the schema before
starting the API. The Quadlet installation above is the primary deployment path.

## Connect an AI client

The examples below use the hosted test endpoint:
**`https://tg-geteway.javohir-dev.uz/mcp`**.
For your own installation, replace it with your configured HTTPS origin followed
by `/mcp`, or `http://127.0.0.1:8086/mcp` for a local client on the same machine.

Choose OAuth where supported. The client opens the gateway's sign-in page; sign
in to your own Telegram account and approve the displayed scopes. Enter
Telegram codes/passwords only in that page. Each authorization gets a separate
account-bound grant, so revoking one does not revoke the others.

### Codex CLI

```bash
codex mcp add telegram-gateway --url https://tg-geteway.javohir-dev.uz/mcp
codex mcp login telegram-gateway
```

### Cursor

Example `.cursor/mcp.json` for the hosted trial:

```json
{
  "mcpServers": {
    "telegram-gateway": {
      "url": "https://tg-geteway.javohir-dev.uz/mcp"
    }
  }
}
```

### Gemini CLI

Add to `~/.gemini/settings.json`, then run `/mcp auth telegram-gateway`:

```json
{
  "mcpServers": {
    "telegram-gateway": {
      "httpUrl": "https://tg-geteway.javohir-dev.uz/mcp"
    }
  }
}
```

This describes Gemini CLI, not a promise of custom MCP support in the consumer
Gemini web app.

### Claude and ChatGPT

Where your client/account permits custom remote MCP connections, add
`https://tg-geteway.javohir-dev.uz/mcp` and authenticate with OAuth.
Enable the connection in the conversation's
tool menu if required. Product features and workspace policies vary; see the
[current Claude guide](https://claude.com/docs/connectors/custom/remote-mcp) and
[OpenAI connection guide](https://developers.openai.com/plugins/deploy/connect-chatgpt).
Private/custom use and public directory approval are separate processes. This
project has not been approved or listed in those directories.

### API tokens and stdio

Create a personal token in `/account` for a client that accepts bearer tokens.
Personal tokens last 30 days. OAuth access tokens last one hour and refresh
tokens rotate with a 30-day lifetime. Revoke a grant in `/account` to stop that
client's gateway access and refresh tokens. Browser sign-out alone does not do so.

Reusing a consumed refresh token closes its entire authorization grant, including
successor refresh tokens and access tokens. Clients must serialize refreshes;
after an ambiguous refresh failure, reconnect instead of replaying the old token.
`/oauth/revoke` also closes the entire grant, whether given its access token or
refresh token. Separate consent grants are unaffected.

Unused dynamic client registrations expire after 24 hours and may be evicted
earlier when the 1,000-entry unused pool fills. Register again if an unused client
ID becomes invalid. Registrations with pending consent or token records are
protected from eviction; consented clients remain registered. Anonymous
registrations cannot fill a permanent global quota and block new connections.

Build the stdio bridge with `go build -o bin/gateway-mcp ./cmd/mcp-stdio`, or use
`/usr/local/bin/gateway-mcp` inside the image. It reads `GATEWAY_URL` and
`GATEWAY_TOKEN`. Keep token-bearing client configurations out of Git.

### Available tools

`get_profile`, `list_chats`, `search_chats`, `get_messages`, `search_messages`,
`list_contacts`, `search_contacts`, `list_chat_members`, `list_message_media`,
`get_media_url`, `inspect_media`, `send_message`.

Scopes: `profile:read`, `chats:list`, `chat:read`, `messages:read`,
`messages:search`, `messages:send`, `contacts:read`, `members:read`, `media:read`.
The tool list is filtered by granted scopes. Existing grants must reconnect and
consent to `messages:send` before sending; personal token creation offers an explicit
send checkbox. There are no join or Telegram-delete tools.

`inspect_media(media_id, second=0)` returns native MCP image content: a photo or
one video frame at the requested timestamp. Request further timestamps to inspect
other parts of a video. It does not transcribe audio or analyze an entire video.
Image input is limited to 16 MiB / 40 megapixels; video input to 100 MiB, frame time
to 0–86400 seconds, conversion to 20 seconds, and output to a 1280-pixel edge.
The container includes FFmpeg; native installations need `ffmpeg` on PATH.

`send_message(chat_id, text, request_id)` sends plain text (1–4096 characters).
Use a fresh UUID `request_id` for a new message and the same UUID/text on retries.
A durable reservation prevents automatic duplicate dispatch after timeouts or
crashes. `accepted` means TDLib accepted it, not that final delivery was confirmed.
An unknown/pending result must be checked in Telegram before starting a new send.
Sending requires both `messages:send` scope and the chat's send permission.
The account limit is a burst of 5 sends, refilling at 6 per minute.

## REST

All `/v1/` endpoints require `Authorization: Bearer <token>`.

| Endpoint | Purpose |
|---|---|
| `GET /v1/profile` | Connected profile |
| `GET /v1/chats` | Paginated approved chats and read/send flags |
| `GET /v1/chats/search?q=...` | Search chat titles/usernames |
| `GET /v1/chats/{chatID}/messages` | Paginated chat messages |
| `GET /v1/messages/search?q=...&chat_id=...` | Search one approved chat |
| `GET /v1/contacts` | Paginated contacts |
| `GET /v1/contacts/search?q=...` | Search contacts |
| `GET /v1/chats/{chatID}/members` | Paginated visible members |
| `GET /v1/chats/{chatID}/media` | Paginated attachment metadata |
| `GET /v1/media/{mediaID}/url` | Five-minute download link |
| `GET /v1/media/{mediaID}/inspect?second=0` | Photo or video frame as base64 JPEG |
| `POST /v1/chats/{chatID}/messages` | Send `{ "text": "...", "request_id": "UUID" }` |

IDs in these paths are gateway UUIDs returned by the API. Use `limit=1..100`
and `cursor=<next_cursor>` for paginated lists. Search returns at most 100
results. Downloads recheck token validity and the active media projection,
so expiry, revocation and Telegram deletion invalidate previously issued links.

## Operate and stop

```bash
systemctl --user status tgw-api tgw-postgres tgw-redis tgw-nats tgw-minio
journalctl --user -u tgw-api -n 100 --no-pager

# Stop the API and all Telegram synchronization; retain stored data.
systemctl --user stop tgw-api.service

# Start it again; default mode only refreshes data when requested.
systemctl --user start tgw-api.service
```

Quadlets live in `~/.config/containers/systemd/tgw-*`. The installer enables
linger so user services can start at boot. Persistent volumes are `tgw-postgres`,
`tgw-redis`, `tgw-nats`, `tgw-minio` and `tgw-tdlib`. Never remove these volumes
as an application update step.

For this permission upgrade, rebuild the application image (includes FFmpeg),
keep `TELEGRAM_AUTO_SYNC=false`, and run migration `000016` before starting the
updated API. No existing chat is automatically approved; use `/account` to grant
access. Reconnect clients that need the new send scope.

Rebuild the application image and rerun `deploy/install.py` to update. Migrations
run under an owner role; the runtime database role has data permissions but
cannot alter the schema or update/delete audit entries.

`python3 deploy/backup.py` creates a private PostgreSQL/configuration backup under
`data/backups/`. Back up MinIO and TDLib volumes separately. Preserve
`TELEGRAM_SESSION_KEY` with session backups: TDLib databases use keys derived
from it and their storage-directory identifiers.

To stop the whole stack without deleting data:

```bash
# If the optional tunnel is installed, stop it first.
systemctl --user stop tgw-tunnel.service
systemctl --user stop tgw-api tgw-minio tgw-nats tgw-redis tgw-postgres
```

## Development and tests

Use the Go version in `go.mod`.

```bash
go vet ./...
go test -race -count=1 ./...
python3 deploy/test-infra.py
python3 deploy/test.py
podman stop tgw-test-postgres tgw-test-redis tgw-test-nats tgw-test-minio
```

Without test configuration, the integration packages skip. `deploy/test.py`
provides a dedicated `_test` database and MinIO credentials from a private
`/tmp/telegram-gateway-test/` directory. Test services bind only to loopback ports
15486/16386/14286/19086. They are separate from the permanent stack. Set
`GO=/path/to/go` if necessary; set `TDLIB_LIBRARY=/path/to/libtdjson.so` to also
exercise native encrypted TDLib session initialization. Tests do not authenticate
to a real Telegram account.

Integration coverage includes returning-user identity, browser CSRF, cross-user
REST/MCP/media isolation, OAuth consent binding, PKCE, token rotation/revocation
races, durable updates, tombstones and media file identity across restarts.

`python3 deploy/verify-public.py` is an optional live smoke test for a configured
Quadlet installation with an active operator account. It checks the deployed
version/commit, OAuth, MCP tool discovery and denial for a cached closed chat.
It does not grant chat permissions or fetch histories, media contents or send messages.
It creates temporary grants and revokes them afterward. It requires no credentials
in command arguments and prints only statuses/counts. Do not run it against a
paused account; the native-ready checks intentionally require an active session.

## Current limits

- One combined API/session/worker instance with local TDLib storage; horizontal
  sharding and a managed hosted service are not provided.
- Telegram controls available history and member lists. Secret chats are disabled.
- Synchronization is eventually consistent and respects persisted FLOOD_WAIT
  cooldowns. API/client revocation does not erase stored mirror data.
- Permanent message deletions are hidden by tombstones; deleted contents can
  remain in private storage and backups. No automatic retention policy is supplied.
- Account isolation has automated tests; a complete independent security audit
  and end-to-end tests in every named AI product have not been performed.

## Contributing and license

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).
Project-authored code is licensed under [MIT](LICENSE). Third-party dependencies,
container components and Telegram content retain their own licenses and terms;
the project license does not relicense those materials.
