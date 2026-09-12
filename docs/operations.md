# Integrated account worker and recovery

The native agent now constructs the ordered TDLib index, typed adapter, durable
inbox and account-specific worker after verified identity activation. It is no
longer a live-only host. No public Telegram write/authentication passthrough was
introduced. The one-account-per-process model is intentional: a supervisor runs
an agent instance for each provisioned account.

## Persistence and ownership

`gateway_sync_jobs` in PostgreSQL is the authoritative work queue. NATS
`gateway.wake.<account UUID>` wakes only that account; the JetStream wake stream
is a bounded notification history, not the work record. The worker polls due
rows as well, so a lost/duplicate wake cannot lose a committed task. This replaces
using the old global `TELEGRAM_SYNC` consumer for native agents; **do not run the
legacy generic worker alongside these agents**. It remains source-compatible
for earlier unit tests but is not a second execution path.

Every history/contact/member/media commit holds a current account-owner fence
and a current job-claim token. Native RPCs and file transfers run outside these
transactions. Jobs, child jobs, audit and progress are committed together. Old
session generations are superseded. A claim expires after three minutes; calls
have a shorter timeout. Native Redis governance is the single charging boundary,
including getMessage/getMessages used for reconciliation. Application budgets do
not count TDLib's internal transport packets/retries.

At startup, the worker scans available chats, resumes persisted history progress,
refreshes recent history, reconciles previously mirrored message IDs and schedules
contacts/members. Reconnect signals trigger another scan/reconciliation. A
five-minute scan checks for newly available chats/recent history. Flood waits and
quota denials delay work without consuming the failure budget. Other failures
become visible `dead` jobs after ten attempts, or immediately for permanent input
errors. The status tool reports pending/dead work; it does not claim completeness.

The normalized live inbox commits each eligible event before applying it. On
restart pending records are replayed in order. Applying a record, scheduling
attachment refreshes, its audit entry and the applied checkpoint share one
transaction. It is idempotent for an accepted event's key. This is **not** a claim
of a lossless Telegram-to-database distributed transaction: a process can crash
between native receipt and inbox acceptance. Reconciliation repairs retrievable
gaps. Messages already deleted before first retrieval cannot be recovered.

## Data availability and files

Permanent ordinary Telegram deletions remain retained, hidden by sticky
`deleted=true` and tombstones. Cache eviction is not deletion. Unavailable,
protected or expired content is hidden using a reversible access block; no raw
protected/expiring/secret message body is newly archived. Known chat protection
changes also hide its messages and members. Restoring access requires fresh
eligible Telegram data; there is no public restore/read-deleted tool.

Live edits schedule a fresh message read. Compare-and-set content versions prevent
an in-flight history/reconciliation result from replacing a newer live edit.
Attachment rows are retired when content changes. Native file IDs are re-resolved
from the current message using its stable remote unique ID; media without a
stable identity is skipped instead of guessing. The default native transfer cap
is 1 GiB. Large/unsupported files may remain unavailable with a visible failed job.

A download claim writes a new private MinIO object version. Only a fenced database
transaction publishes that version. A stale process therefore cannot overwrite a
new owner's published file. Failed private versions are not exposed and are not
automatically purged; storage capacity/retention must be monitored. Public reads
use the authenticated deletion-aware proxy, never a signed/public storage URL.
Already delivered bytes cannot be recalled.

## Startup

1. Build with the Go version in go.mod. Run `go build ./cmd/...` and
   `CGO_ENABLED=1 go build -tags tdlib -o tdlib-agent ./cmd/tdlib-agent`.
   Install a verified real `libtdjson.so` separately; the CI ABI fixture is NOT it.
2. Start PostgreSQL, Redis with persistence, NATS JetStream and a private MinIO
   bucket. `docker-compose.yml` is **development infrastructure only**, with
   loopback-bound ports and example credentials, not a production deployment.
3. Export the environment using host-reachable addresses (compose service names
   resolve only inside its network). Run `go run ./cmd/gateway-migrate` with the
   migration owner. It records immutable checksums and can be run repeatedly.
   On the previous untracked database, first verify backups/applied versions,
   then explicitly use `--adopt-through=11`; do not adopt unverified schema.
4. Run `deployments/roles.sql` as an administrator, then create separate login
   roles for API, agent and privileged offline operator. The API role has no base
   messages/chats or inbox-payload access. Grant group membership to the respective
   login role; do not make application logins table owners or superusers.
5. `go run ./cmd/gatewayctl provision --operator=<operator>` returns a pending
   user/account UUID. For a second account of the same user use `--user=<UUID>`.
6. Create owner-only persistent session directories and a 0600 master-key file
   (base64-encoded 32 random bytes; never overwrite an existing key). Supply your
   Telegram API_ID/API_HASH in the private agent environment. Start the agent
   with `--account`, `--sessions-root`, `--library`, `--master-key-file` and
   explicit `--enable-live-mirror`. Follow the existing private Unix-socket login
   instructions in `tdlib-runtime.md`. Never send OTPs/passwords to an AI/chat.
7. Configure the external OAuth issuer and per-client approval described in
   `mcp-oauth.md`, then enable MCP and start the API behind HTTPS. Set its public
   Host header correctly; do not rewrite it to an internal address.

The systemd instance template restarts a failed agent after 30 seconds and kills
an unresponsive process before releasing its session directory. An expired lease
cannot authorize writes. Redis cooldown state and encrypted session files must
survive restart; do not clear them to bypass a flood wait. Preserve the master key
outside repository/backups readable by the API. Losing it can make saved TDLib
sessions unusable.

## Operations and explicit deployment gates

`gatewayctl status --operator=<name> --account=<UUID>` reports current work.
`gatewayctl retry --operator=<name> --account=<UUID> --job=<dead-job-UUID>` performs
an audited retry only in the current generation. Investigate the error first.
`gatewayctl revoke` removes one issuer/subject/client grant immediately on the
next authorization check. `audit-verify` checks the append-only audit hash chain;
an independently retained signed checkpoint is still required to detect a
privileged attacker replacing a whole chain. Audit hashes alone do not establish
legal admissibility or Telegram authorship.

Before public use, configure and test actual OAuth consent/PKCE/refresh/revocation,
real TDLib/account login, storage encryption (encrypted PostgreSQL volumes/backups
and MinIO KMS or encrypted storage), TLS, service credentials, resource limits,
backup restore, retention/legal requirements and monitoring. These require actual
infrastructure/credentials and are NOT proven by fake-native unit tests. The code
supplies an operator-managed backend, not a self-service signup/billing web UI or
a fleet scheduler. Deploy more per-account agent instances deliberately rather
than attaching one native session to a global consumer.

PostgreSQL 18 development volumes now mount `/var/lib/postgresql`. Existing
containers using the former `/var/lib/postgresql/data` mount need a verified
backup/data-directory migration before recreating them. No deployed volume was
modified by this repository change. Never run `migrate-down` against retained
production data; it is destructive and exists for isolated migration tests.
