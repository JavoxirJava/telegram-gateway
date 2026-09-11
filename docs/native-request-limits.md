# Native TDLib request governor

The operator agent now requires Redis and injects a mandatory request policy
into the authenticated TDLib session. All application-level `Session.Read` and
authentication submissions pass a serialized per-session gate, a shared account
budget and a category budget. Local TDLib parameter initialization, auth-state
inspection and shutdown are exempt; Redis failure must not prevent shutdown.

The Redis operation checks the existing account cooldown and both budgets in
one Lua invocation. It debits both or neither. Defaults are provisional gateway
policies, NOT documented Telegram quotas: 30 requests/minute (burst 10) per
account, 10/minute (burst 3) for history, 5/minute (burst 2) for file calls, and
5/minute (burst 1) shared across login steps. The private control socket returns
429 with Retry-After for throttled attempts and never echoes credentials.

An observed TDLib 429 saves a shared cooldown with a one-second margin, without
shortening an existing cooldown. Saving this response is attempted even if the
caller context was cancelled. Failure to persist it seals the current policy
instance; restart only after Redis/cooldown state is recovered. A budget outage
blocks requests rather than falling back to unrestricted calls. A private
control socket is still mandatory; this does not expose public Telegram login.

## Explicit boundaries

- This controls application calls, not TDLib's internal transport, push updates,
  reconnect packets, file-transfer chunks or its own internal retries.
- The ordered native mailbox observes sanitized errors even after the original
  caller timed out. A late 429 still stores its cooldown, but requests already
  accepted before the response arrived cannot be recalled.
- Redis persistence/recovery and account-owner routing must be designed before
  horizontal scaling. The current multi-key script targets standalone Redis,
  not Redis Cluster's cross-slot execution.
- Existing generic workers have a pre-request limiter too. When the typed native
  adapter is connected, make the native policy the single charging boundary;
  do not inadvertently charge the same call twice.

Tests include concurrent atomic budgets, a rejected category preserving account
credit, cooldown non-shortening, category selection, fail-closed Redis behavior,
private-auth Retry-After responses and a cancelled caller's observed cooldown.
The Redis integration tests run only with TEST_REDIS_ADDR set (CI supplies an
isolated Redis service); they are skipped otherwise.
