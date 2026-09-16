# Unofficial Telegram Gateway

[![CI](https://github.com/JavoxirJava/telegram-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/JavoxirJava/telegram-gateway/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A self-hosted Telegram gateway with a **read-only REST API and MCP server**.
Search your synchronized messages, list chats and contacts, and retrieve message
attachments from AI clients or your own scripts.

TDLib connects to Telegram. PostgreSQL stores the mirror, MinIO stores media,
Redis handles rate limits, and NATS JetStream schedules durable sync jobs.
This is an independent project, not affiliated with Telegram or an AI provider.

## What works

- Phone/code, two-step verification, email verification and QR login through TDLib.
- Multi-user sign-in: verified Telegram identities map to separate accounts;
  returning users recover their existing mirror.
- Background history, contacts, visible members, media and live update sync.
- REST pagination/search and 10 account-scoped MCP tools.
- OAuth discovery, dynamic client registration, PKCE S256, rotating refresh
  tokens and per-client revocation; personal API tokens are also supported.
- Private media download links, account isolation tests, audit chains,
  checksummed database migrations and restart-safe session storage.
- Linux deployment with rootless Podman Quadlet and an optional HTTPS tunnel.

## Before connecting an account

**This version starts background synchronization when an account is connected.**
It can copy accessible cloud-chat history, contacts, visible members and media
onto the host. Large accounts can use significant disk space. Per-chat selection,
storage quotas, automatic retention and a self-service delete-account workflow
are not implemented yet. Stop the API service to stop all synchronization.

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
podman build --layers -f Dockerfile.tdlib -t localhost/telegram-gateway-tdlib:d1085f9 .
podman build --layers -t localhost/telegram-gateway:local .
python3 deploy/install.py
```

The service listens on **http://127.0.0.1:8086**. PostgreSQL, Redis, NATS and MinIO
have no published host ports. These scripts install one `tgw-*` stack per Linux
user and preserve existing volumes.

- `/health/ready`: dependency checks.
- `/login`: sign in to your Telegram account; this begins synchronization.
- `/account`: sync counts, personal tokens and AI grants for that account.
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

For a local client on the same machine, use `http://127.0.0.1:8086/mcp`.
For a hosted client, use your configured HTTPS origin followed by `/mcp`.

Choose OAuth where supported. The client opens the gateway's sign-in page; sign
in to your own Telegram account and approve the displayed read scopes. Enter
Telegram codes/passwords only in that page. Each authorization gets a separate
account-bound grant, so revoking one does not revoke the others.

### Codex CLI

```bash
codex mcp add telegram-gateway --url http://127.0.0.1:8086/mcp
codex mcp login telegram-gateway
```

### Cursor

Example `.cursor/mcp.json` for a local installation:

```json
{
  "mcpServers": {
    "telegram-gateway": {
      "url": "http://127.0.0.1:8086/mcp"
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
      "httpUrl": "http://127.0.0.1:8086/mcp"
    }
  }
}
```

This describes Gemini CLI, not a promise of custom MCP support in the consumer
Gemini web app.

### Claude and ChatGPT

Where your client/account permits custom remote MCP connections, add your HTTPS
MCP URL and authenticate with OAuth. Enable the connection in the conversation's
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

Build the stdio bridge with `go build -o bin/gateway-mcp ./cmd/mcp-stdio`, or use
`/usr/local/bin/gateway-mcp` inside the image. It reads `GATEWAY_URL` and
`GATEWAY_TOKEN`. Keep token-bearing client configurations out of Git.

### Available tools

`get_profile`, `list_chats`, `search_chats`, `get_messages`, `search_messages`,
`list_contacts`, `search_contacts`, `list_chat_members`, `list_message_media`,
`get_media_url`.

Scopes: `profile:read`, `chats:list`, `chat:read`, `messages:read`,
`messages:search`, `contacts:read`, `members:read`, `media:read`.
The tool list is filtered by granted scopes. There are no MCP send, join or
Telegram-delete tools. Results come from the stored mirror and may lag sync.

## REST

All `/v1/` endpoints require `Authorization: Bearer <token>`.

| Endpoint | Purpose |
|---|---|
| `GET /v1/profile` | Connected profile |
| `GET /v1/chats` | Paginated active chats |
| `GET /v1/chats/search?q=...` | Search chat titles/usernames |
| `GET /v1/chats/{chatID}/messages` | Paginated chat messages |
| `GET /v1/messages/search?q=...` | Search stored message text |
| `GET /v1/contacts` | Paginated contacts |
| `GET /v1/contacts/search?q=...` | Search contacts |
| `GET /v1/chats/{chatID}/members` | Paginated visible members |
| `GET /v1/chats/{chatID}/media` | Paginated attachment metadata |
| `GET /v1/media/{mediaID}/url` | Five-minute download link |

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

# Start it again; connected accounts configured online resume synchronization.
systemctl --user start tgw-api.service
```

Quadlets live in `~/.config/containers/systemd/tgw-*`. The installer enables
linger so user services can start at boot. Persistent volumes are `tgw-postgres`,
`tgw-redis`, `tgw-nats`, `tgw-minio` and `tgw-tdlib`. Never remove these volumes
as an application update step.

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
Quadlet installation with an active operator account and mirrored messages/media.
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
