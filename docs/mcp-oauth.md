# Read-only MCP and external OAuth

The API exposes `/mcp` through the official MCP Go SDK's stateless Streamable HTTP
handler. The gateway is an **OAuth resource server**. A separately operated
OAuth/OIDC authorization server owns user login, S256 PKCE, consent, authorization
codes and refresh tokens. No home-grown password/token issuer was added.

## Authorization-server contract

Set `MCP_ENABLED=true`, `MCP_PUBLIC_URL=https://<gateway>/mcp`, `OAUTH_ISSUER`,
`OAUTH_INTROSPECTION_CLIENT_ID` and a 0600 `OAUTH_INTROSPECTION_SECRET_FILE`.
The issuer's OpenID configuration must have an exact matching issuer and same-host
HTTPS authorization, token and introspection endpoints, authorization-code support
and `code_challenge_methods_supported` containing `S256`. Register real MCP client
redirect URIs explicitly. Use pre-registration supported by the chosen client;
there is no anonymous dynamic-registration service in this gateway.

Introspection must accept client_secret_basic and return `active`, `iss`, `sub`,
`client_id`, `aud`, integer `exp`, `token_type=Bearer`, optional `nbf`, and the
space-separated granted `scope`. `aud` must include the exact MCP resource URL.
Configure this audience and introspection claim mapping in the issuer, not by
weakening the gateway's checks. Access and refresh token policies, redirect
validation, PKCE verification and consent storage must be tested on the issuer.

No positive introspection cache is used. Each request validates the live token
and a database grant. Audiences, expiry, issuer, account/client mapping and scope
intersection are enforced. Authority never comes from an account_id tool argument
or a forwarded Telegram credential. The API never receives the TDLib database key.
An unavailable issuer or grant database fails closed.

## Explicit account/client approval

After the native agent verifies the account identity, an authorized operator binds
an already verified immutable issuer/subject (not an email/name) to the account:

```sh
gatewayctl grant --operator=deployment-admin \
  --account=<gateway-account-UUID> \
  --issuer=https://identity.example.com/realms/gateway \
  --subject=<verified-issuer-subject> --client=<registered-MCP-client-ID> \
  --consent=<approval-record-reference> \
  --scopes=profile:read,chats:list,messages:read,messages:search
```

The operator must verify the subject/account owner and retain informed approval;
a string in `--consent` is a reference, NOT proof that consent was obtained.
Scope changes must be approved again. The `(issuer, subject, OAuth client)` account
binding is immutable. Use a distinct registered client for another account;
there is no model-selected cross-account switch. Different AI clients may have
different scopes but read the same already-stored account mirror.

To revoke, use `gatewayctl revoke --operator=... --account=... --issuer=...
--subject=... --client=...`. Revocation and provisioning are audited. Existing
API keys work only on the REST interface, not as OAuth tokens on `/mcp`.

## Discovery, tools and boundaries

Unauthenticated requests return 401 with `WWW-Authenticate` pointing at
`/.well-known/oauth-protected-resource/mcp`. That metadata advertises the issuer,
resource audience and read scopes. Tool scope failures return 403 with the required
scope, enabling the client to seek new consent at the issuer. The initial
challenge requests only `profile:read chats:list`; grant nothing automatically.

Tools: get_profile, get_sync_status, list_chats, search_chats, list_messages,
search_messages, list_contacts, search_contacts, list_members, list_media.
All carry read-only annotations. All actual reads still pass through the existing
REST authorization, account filtering, active views, limiter and audit writer.
There is no send/edit/delete/join/auth/arbitrary-HTTP/SQL tool. Telegram content is
untrusted data, not model instructions. Deleted/inaccessible content and native
file credentials are never offered as tools/resources.

Requests are bounded to 64 KiB and results to 1 MiB; page size is 1..100. MCP
JSON-RPC batches are rejected to avoid bypassing per-request budgets. Long chat
history uses next_cursor. Metadata endpoints without a cursor intentionally
return a bounded selection, not an assertion that all contacts/members were
returned. Binary media stays behind authenticated REST `/content`, not a public
capability URL embedded in a model response.

The HTTPS Host must match MCP_PUBLIC_URL. Browser origins require exact entries
in space-separated MCP_ALLOWED_ORIGINS; do not use '*'. Server-to-server clients
without Origin are supported. A peer-address admission limiter runs before token
introspection; spoofable X-Forwarded-For is ignored. A proxy should enforce its own
per-user/IP admission policy, since all upstream peers otherwise share that cap.

Tests use the actual Go MCP SDK plus controlled introspection/REST/native doubles
and real PostgreSQL/Redis/NATS CI services where configured. No claim is made that
a deployed third-party AI account, live Telegram login or real identity provider
has already been connected. This release includes no signup/consent web frontend;
operator provisioning and an external identity server are required.

Primary protocol reference: https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization
