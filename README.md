# Telegram Gateway

Private, user-authorized Telegram gateway intended to expose controlled,
read-only data to AI clients through MCP and REST.

**Status: development checkpoint, not a production-ready hosted service.**
The TDLib native runtime and an operator-only live mirror are implemented.
The complete historical adapter, public account-linking UI and MCP/OAuth
integration are still pending. See [TDLib runtime setup and release gates](docs/tdlib-runtime.md).

## Implemented components

| Component | Current status |
| --- | --- |
| PostgreSQL, Redis, NATS JetStream, MinIO clients | Foundation implemented |
| REST reads, scopes and hashed API tokens | Implemented; production privilege review remains |
| Persistent message mirror and soft-deleted projections | Implemented |
| Hash-chain audit and verifier | Implemented, including transactional live writes |
| Generic history, contacts, members and media workers | Implemented against an abstract Telegram session |
| Native TDLib JSON engine and authentication lifecycle | Implemented; fake-transport and C ABI tests |
| Account-bound TDLib key wrapping and workspace locks | Implemented |
| Ownership leases, identity binding and live-write fencing | Implemented with PostgreSQL tests |
| Operator-started native live-mirror agent | Implemented; real-account acceptance tests pending |
| Full typed native adapter and worker integration | Pending |
| Public MCP transport, OAuth and account-linking control plane | Pending |

```text
Native TDLib agent -> verified identity gate -> live PostgreSQL mirror + audit
                                                      |
                                                      v
                                                Read-only REST API

Generic JetStream workers -> typed TDLib adapter (pending) -> Telegram
MCP/OAuth (pending) -> scoped gateway reads
```

## Product invariants

External Telegram access starts read-only. Ordinary synced messages are kept
in PostgreSQL; ordinary Telegram deletions set `deleted=true` rather than
removing stored content. Normal reads use active projections. Deletion identities
prevent delayed history from making deleted messages visible again.

The native ingestion mapper excludes secret chats, protected chats and detected
expiring content. Login material must never enter source control, operational
logs or MCP output. Database-key wrapping does not replace PostgreSQL/disk/MinIO
encryption and does not constitute a complete secrets-management deployment.

**Not yet a complete deletion-access guarantee:** the pre-existing media service
issues expiring MinIO links. Links issued before a deletion can remain usable
until expiry. A deletion-aware media proxy/revocation path is a release blocker.
The native adapter also still needs shared request budgets and durable reconnect
reconciliation. Do not expose the development service to public users yet.

## Local API development

Use the Go version pinned in `go.mod`, Docker Compose and a disposable database.

```sh
cp .env.example .env
# Set private local development credentials; never commit .env.
make infra-up
make migrate-up
make run
```

Health endpoints are `GET /health/live` and `GET /health/ready`. Readiness checks
infrastructure reachability, not completeness of the Telegram/MCP integrations.

## Native agent and validation

Build the Linux native agent with `CGO_ENABLED=1` and `-tags tdlib`. Supply a
trusted, reviewed `libtdjson.so`, existing gateway account, owner-only session
workspace and private master-key file as described in the [runtime guide](docs/tdlib-runtime.md).
Authentication binds only to a `0600` Unix socket inside a `0700` directory, not
to a public TCP listener.

The TDLib validation workflow runs module/format checks, race tests, PostgreSQL
integration tests, migration up/down, `go vet`, all command builds and a tagged
C ABI test. The C fixture is not actual TDLib: real-account login, reconnect,
update delivery and flood-limit behavior remain acceptance-test requirements.

The full next-stage checklist, including MCP/OAuth, historical/media integration,
crash recovery, least-privilege storage and production hardening, is in
[docs/tdlib-runtime.md](docs/tdlib-runtime.md).
