# Telegram Gateway — integrated backend

Go/TDLib account mirror, PostgreSQL durable synchronization and audit, Redis native
budgets, private MinIO media, account-specific NATS wakeups and read-only MCP.

**Deployment status:** integrated operator-managed backend; not a deployed
self-service SaaS. Real Telegram/identity-provider end-to-end validation and
production infrastructure configuration are still required. Native CI fixtures
are test doubles, not Telegram. Nothing here enables reading retained deleted
messages through MCP/API.

Start with [operations](docs/operations.md), [MCP/OAuth](docs/mcp-oauth.md),
[media access](docs/media-access.md), [native budgets](docs/native-request-limits.md)
and [TDLib private authentication](docs/tdlib-runtime.md).

Build: `go build ./cmd/...`; native agent: `CGO_ENABLED=1 go build -tags tdlib
-o tdlib-agent ./cmd/tdlib-agent`. Provision an account with `gatewayctl`, authenticate
on its private Unix socket, approve an OAuth grant, and run the API behind HTTPS.
Set configuration from `.env.example` privately; never commit live credentials.

The native agent uses the new PostgreSQL account queue, not the legacy global
JetStream worker. Existing history/media/native protection components remain in
place. `deleted=true` is retained and hidden, while temporary inaccessibility and
protection changes use a separate reversible access block. No raw Telegram
credentials enter the MCP process.

## Verification

`go test -race -count=1 ./...`, `go vet ./...`, and `go build ./cmd/...`.
Set TEST_DATABASE_URL, TEST_REDIS_ADDR and TEST_NATS_URL for service integration
tests; otherwise those tests are explicitly skipped. Native ABI tests additionally
use `-tags tdlib`. GitHub TDLib validation supplies isolated services and checks
migrations and role setup. Check the workflow for the exact commit being deployed.

## Data/security boundary

Store session databases and master keys only in owner-only persistent storage.
Use separate API/agent/operator database identities (`deployments/roles.sql`).
Configure encrypted volumes/backups and MinIO encryption/KMS for production; an
application repository alone does not encrypt your host's disks. Keep all storage
and native-control endpoints private. Never deploy example development credentials.
The compose file binds infrastructure ports to loopback; it is not a hardened
production stack. See the operations guide before changing an existing PG volume.

Audit hash chaining detects certain modifications, not wholesale privileged
history replacement without an external trusted checkpoint; it is not a legal
proof service. Obtain required user/participant permissions and review platform
and retention obligations before opening the service to others.
