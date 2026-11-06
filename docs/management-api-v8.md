# v8 Management API

The base path is `/v8/management`. Configuration endpoints follow the structure in
[config.example.toml](../config.example.toml); operational endpoints are grouped
by function. The existing `/v0/management` API remains supported. Its reference is
available in the [management API guide](https://help.router-for.me/management/api).

## Access

The v8 endpoints use the same management key and remote-access policy as v0.
Send `Authorization: Bearer <management-key>` or `X-Management-Key: <management-key>`.
OAuth callbacks validate a pending login state and do not require a management-key
header. Home mode disables local management endpoints for both versions.

## Configuration endpoints

All paths below are relative to `/v8/management`.

| Path | Methods | Description |
| --- | --- | --- |
| `/config` | GET, PUT, PATCH | Read, replace, or merge the complete configuration as JSON. |
| `/config.toml` | GET, PUT | Read or replace the complete configuration as TOML. |
| `/config/<section>/<field>` | GET, PUT, PATCH, DELETE | Read or modify a section or field using its path in the v8 tree. |

GET renders the persisted configuration in the v8 layout without adding omitted
runtime defaults. Successful v8 configuration writes save the latest layout;
reads and rejected writes leave the file unchanged after any one-time legacy
file import performed at startup. Historical v8 paths and JSON request bodies
remain accepted as aliases.

The running configuration is native TOML. `X-Config-Format: toml` names its
format. `/config.toml` GET returns TOML and PUT accepts TOML, saving it to the
resolved running TOML path. There is no `/v8/management/config.yaml` endpoint
and no YAML export. JSON and TOML writes share ETag/If-Match handling. An
unparsable body is `400 {"error": "invalid_body", "message": ...}` and a parsable
but rejected configuration is `{"error": "invalid_config", "message": ...}`
(422 when it fails to decode as configuration or cannot be represented in TOML,
400 when it breaks a v8 layout rule). TOML has no null: omit a setting to inherit
its default. A write that would lose configuration meaning is rejected.

An explicitly selected legacy YAML file, or the default `config.yaml`, is
imported once to a sibling `.toml` file. An existing TOML sibling wins; otherwise
the sibling is written atomically with the source permissions. The YAML source
is untouched. API writes and hot reload use the resolved TOML path thereafter.

JSON writes accept the value directly, without a `{ "value": ... }` envelope.
PUT replaces its target. PATCH merges objects and replaces lists and scalars.
Field updates retain key/table comments and document order where the TOML layout
permits; normalization or moved legacy settings may move comments. Complete
document replacements use the submitted TOML document.
DELETE removes a field. Paths identify mapping keys, not array indexes; replace
an entire list to change its entries. Omit optional per-key overrides to inherit
the corresponding group value. Legacy field names are rejected by v8 writes.

| Example path | Value or purpose |
| --- | --- |
| `/config/access/api-keys` | Client authentication keys, for example `["client-key"]`. |
| `/config/api-keys` | All upstream provider groups. |
| `/config/api-keys/codex` | Codex upstream groups. |
| `/config/client/codex/optimize-multi-agent-v2` | Boolean, default `false`; applies to Codex clients across OAuth and API-key routes. |
| `/config/observability/logs/debug` | A boolean, for example `true`. |
| `/config/routing/retry/request-retry` | A number, for example `0`. |
| `/config/plugins/configs/<id>` | A plugin configuration object. |

For example, `PATCH /v8/management/config` with this body disables additional
request retries while preserving other settings:

```json
{
  "routing": {
    "retry": {
      "request-retry": 0
    }
  }
}
```

JSON reads omit TURN usernames and credentials under
`oauth.providers.codex.live-media-relay.ice-servers`; TOML reads include them.
The Home-owned revision fields
`credentials.concurrency.lifecycle-config-revision`,
`credentials.concurrency.observation-barrier-revision`, and `plugins.auth-revision`
cannot be changed through these endpoints.

### Codex multi-agent configuration migration

`client.codex.optimize-multi-agent-v2` is the sole runtime setting. Loading older
YAML accepts `providers.codex.optimize-multi-agent-v2`,
`oauth.providers.codex.optimize-multi-agent-v2`, and the flat
`codex.optimize-multi-agent-v2` path. The client path always wins conflicts,
including explicit `false` or `null`. If it is absent, the historical OAuth path
wins over `providers`, which wins over the flat path.

These aliases now have client-wide semantics, not OAuth-only scope. Loading does
not rewrite legacy-only settings; normalization removes aliases conflicting with
the client path. A successful v8 write migrates the aliases while preserving their
comments. Historical API paths remain usable. Home configuration publishers
need matching client schema support before emitting the new path; older files
remain readable through the one-time legacy import.

## Operational endpoints

All paths below are relative to `/v8/management`. Request and response bodies
retain the corresponding business operation's fields.

| Path | Methods | Description |
| --- | --- | --- |
| `/config/upstream/<provider>` | GET, PUT, PATCH, DELETE | Manage shared provider settings. |
| `/server/latest-version` | GET | Get latest release information. |
| `/requests/api-call` | POST | Make an authenticated upstream call. |
| `/routing/cooldown/reset` | POST | Clear credential cooldown. |
| `/routing/model-definitions/<channel>` | GET | Get model definitions. |
| `/observability/logs` | GET, DELETE | Read or clear application logs. |
| `/observability/logs/errors` | GET | List error-log files. |
| `/observability/logs/errors/<name>` | GET | Download an error-log file. |
| `/observability/logs/requests/<id>` | GET | Get a request log. |
| `/observability/usage/api-keys` | GET | Get API-key usage. |
| `/observability/usage/queue` | GET | Get queued usage events. |
| `/credentials` | GET, POST, DELETE | List, upload, or delete credential files. |
| `/credentials/models` | GET | Get credential models. |
| `/credentials/download` | GET | Download a credential file. |
| `/credentials/status` | PATCH | Change credential status. |
| `/credentials/fields` | PATCH | Change credential fields. |
| `/credentials/refresh` | POST | Refresh credentials. |
| `/oauth/import?provider=vertex` | POST | Import a Vertex service account using a multipart `file` upload. |
| `/oauth/auth-url?provider=<provider>` | GET | Start built-in or plugin OAuth. |
| `/oauth/status?state=<state>` | GET | Get login status. |
| `/oauth/session?state=<state>` | DELETE | Cancel a login session. |
| `/oauth/callback` | GET, POST | Submit an OAuth callback. |
| `/plugins` | GET | List installed plugins. |
| `/plugins/<id>` | DELETE | Delete a plugin. |
| `/plugins/store` | GET | List the plugin store. |
| `/plugins/store/<id>/install` | POST | Install or update a plugin. |
| `/plugins/<id>/quota` | GET, POST, DELETE | Read, fetch, or reset plugin quota. |

## OAuth

The login URL is shared by all providers. Set the required `provider` query
parameter to `claude`, `codex`, `antigravity`, `kimi`, `xai`, `devin`, `meta`,
or a registered plugin provider ID. For example:

```http
GET /v8/management/oauth/auth-url?provider=codex
Authorization: Bearer <management-key>
```

Keep any provider-specific login parameters in the query string. The login
response includes the authorization URL and a session `state`. Use that state
for status queries and cancellation.

Callbacks accept `provider`, `state`, `code`, and `error` as GET query parameters
or POST JSON fields. POST also accepts `redirect_url` containing the complete
callback URL. If `provider` is omitted, it is inferred from the pending state;
an explicit provider must match that state. Poll until the status is `ok` or
`error`; `wait` means the login is still pending. Callback acceptance alone does
not mean credential exchange and persistence have completed.

Import uses `POST /v8/management/oauth/import?provider=vertex`. The `provider`
query parameter is required; Vertex is currently the supported import provider.

Provider redirects land on `/codex/callback`, `/anthropic/callback`,
`/antigravity/callback`, `/callback` and `/devin/callback`; each answers with
one shared result page (inline CSS, no external assets) that says whether the
sign-in finished and links back to the pool at `/neo/#/`. Status codes and state
handling are unchanged.

The dashboard's unified provider-access API (`/v8/neo/access`, session
authenticated) is documented in [neo-access-api.md](neo-access-api.md).

### Claude provider

`claude` uses the NeoPi Anthropic implementation for both subscription OAuth
and API keys; there is no separate experimental provider. The dashboard starts
the same authorization-code flow at `/v8/neo/vault/login/claude`. The provider
catalog reports `subscription.flow = "browser"` because NeoProxy catches its
`http://localhost:54545/callback` redirect. The dialog retains a pasted URL/code
fallback when the browser cannot reach that listener. Existing Claude credential
files keep their filenames, ownership and prefixes. Imported NeoPi credentials
remain refresh-locked; model and usage discovery never refreshes them.

Without an explicit configured model list, Claude merges NeoPi's bundled
catalog with account-scoped `/v1/models` discovery. Successful discovery is
cached for two hours; failures retain the last successful roster and are retried
after five minutes. Changing the credential or endpoint invalidates that roster.
Explicit model lists, aliases and exclusions remain operator-controlled.

Provider-reported shared five-hour and weekly utilization feeds the dashboard
and routing budgets. Missing or null readings are unknown, not zero. Model-scoped
weekly caps and monetary extra usage are not promoted to account-wide percent
budgets. Usage polling does not retry a 429 or move a rejected credential to a
different host; canonical-host fallback is limited to absent usage endpoints.

Source-backed acceptance compares request method, URL, headers and exact body
bytes, including the billing signature. It does not establish identical native
TLS fingerprints or HTTP framing between Bun and Go. Plain Messages requests
also cannot reconstruct NeoPi-only history provenance, request controls or
provider-session state; explicit wire controls are preserved instead. Runtime
identity versions follow the captured NeoPi source.

Model discovery is asynchronous and lifecycle-cancellable, but deliberately
does not copy NeoPi's ten-second discovery deadline. The repository's explicit
network rule requires: “after an upstream connection is established, do not set
timeouts for any subsequent network behavior.” Optional account-profile,
credit-reset and monetary-usage UI enrichment is not exposed through NeoProxy's
shared-percentage budget projection.

## v0 compatibility

The `/v0/management` endpoints keep their original paths and payloads, including
provider-specific OAuth login URLs, callbacks, status queries, and cancellation.
For example, `GET /v0/management/codex-auth-url` and
`GET /v8/management/oauth/auth-url?provider=codex` start the same login flow.

Legacy flat configuration endpoints such as `/debug`, `/request-retry`, and
`/codex-api-key` exist under `/v0/management` only.
`/v0/management/api-keys` continues to manage client authentication keys; the v8
equivalent is `/v8/management/config/access/api-keys`.

Legacy-only configuration files keep their layout until a successful v8
configuration write. When both layouts specify a field, the new field takes
precedence and its legacy equivalent is removed. V0 setters keep legacy-only
files in their original layout; existing v8 files are saved in the latest v8
layout. `PUT /v0/management/config.yaml` still replaces the complete file and
accepts legacy, new, or mixed layouts, normalizing v8 documents on save.

Plugin OAuth uses the shared v8 login endpoint. Other plugin-defined HTTP
extensions retain their declared `/v0/management` routes.
