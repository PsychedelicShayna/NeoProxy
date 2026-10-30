# Backend contracts the NeoProxy dashboard assumes

The dashboard at `/neo/` was designed ahead of the backend. Its product pages initially used an in-memory fixture (`internal/neoui/static/lib/fixture.js`) behind one client (`lib/client.js`). This document specifies the real backend in the existing Go CLIProxyAPI-derived engine. NeoProxy owns provider accounts, credentials and refresh. NeoPi is an inference client, not the credential source. The authorized implementation and delivery status are tracked in `backend-plan.md`. Where the dashboard makes a decision the backend must honour, it is marked **must**. Where it only displays what it is given, the field is marked *display*.

Production product pages must use the real engine adapter; an unavailable backend is an error, not a fallback to demo accounts or generated requests. `fixture.js` remains importable only as the routing reference oracle. A source chip is display metadata, not proof of a live implementation or an authorization boundary. The delivery plan records whether the actual endpoint and browser checks have passed.

The existing management API (`/v8/management/...`) remains the engine administration interface. Organization-admin sessions may use its dashboard operations; member and account-owner sessions must not acquire management-secret authority. Existing operator management keys remain maintenance credentials, not ordinary dashboard logins or per-user inference keys. The first supported inference protocols are OpenAI Chat Completions, OpenAI Responses and Anthropic Messages.

## Vocabulary

| Term | Meaning |
|---|---|
| **Core** | The running NeoProxy Go engine. It owns provider login, credential storage, refresh and inference execution. |
| **Provider** | A credential family supported by the proxy: `codex`, `anthropic`, `xai`, `zai`, `cerebras`, `github`, `gemini`, … |
| **Account** | One proxy-managed signed-in identity or API credential for a provider, with an owning user and its own quota windows. A provider may have several. |
| **Window** | A provider quota period for one account: `5h`, `day`, `week`, `month`. Usage is a fraction `0..1` of that window. |
| **Model** | A provider's model, served as `neoproxy/<provider>/<model>`. |
| **Virtual model** | A name NeoProxy serves that is not a provider's model: `neoproxy/<name>` (`default`, `small`, `heavy`, …). Defined by the admin as a chain. |
| **Stage** | One step of a chain: a model, a pool of accounts, a draw policy, a budget, a window. |
| **Terminal** | What happens when every stage is spent: limit error, queue, or fall through. |
| **User** | A human who signs into NeoProxy with username/password, or a service principal using per-user inference keys. Roles are organization admin (`admin`), account owner (`owner`), `member`, or `service`. |
| **Grant** | A user's access to a virtual model, with an allotment that scales every budget in that chain. |
| **Direct access** | A user's permission to ask for `neoproxy/<provider>/<model>` by name, bypassing virtual models. Off by default for members. |
| **Allotment** | A per-grant multiplier `0..1` applied to every stage budget in a chain. `0.5` means half the sips and half the cap. |

## Naming rules

- Consumer inference keys use model ids beginning with `neoproxy/` on the three day-one inference endpoints. The prefix means "served by NeoProxy"; downstream (NeoPi, NeoDeck, NeoCode) treats `neoproxy` as one provider. Existing configured engine/operator keys retain their existing selectors and protocols; new consumer keys cannot use unsupported legacy surfaces.
- `neoproxy/<provider>/<model>` is a provider model. The provider segment is the proxy engine's provider id.
- `neoproxy/<name>` with exactly one segment after the prefix is a virtual model. Names are `^[a-z][a-z0-9-]{0,31}$` and **must** not collide with a provider id (the dashboard refuses a name that equals a served model id; the backend should refuse one that equals a provider id too).
- `GET /v1/models` **must** list, for the calling key's user: every granted virtual model first, then every provider model the user has direct access to and that is served (provider exposed and model exposed). Members with no direct access see only `neoproxy/<name>` entries.

## Authentication and sessions

The dashboard uses username/password login with server-side sessions. Provider account credentials, dashboard sessions, user inference keys and operator management secrets are separate authorities.

```
POST /v8/session
  { handle: string, password: string }
→ 200 { role: "admin" | "owner" | "user", userId, handle, expiresAt }
  Set-Cookie: neoproxy_session=<opaque session>; HttpOnly; SameSite=Strict; Path=/
→ 401 { error: { message } }   // invalid login; do not disclose which field failed
→ 403 { error: { message } }   // user suspended or not yet active

GET /v8/session
→ 200 { role, userId, handle, expiresAt }
→ 401 { error: { message } }

DELETE /v8/session
→ 204                         // revoke the server-side session and clear its cookie

POST /v8/session/bootstrap
  Authorization: Bearer <operator management secret>
  { handle: string, name?: string, password: string }
→ 201 { role: "admin", userId, handle, expiresAt } + session cookie
→ 409 { error: { message } }   // bootstrap already completed
```

Password verifiers and session-token hashes are stored in SQLite; plaintext passwords and session tokens are never returned in user records or logs. Sessions expire and are revoked on logout. Password changes and suspension invalidate existing access. The cookie is `Secure` when HTTPS is used. Initial local HTTP operation must not pretend to provide transport confidentiality; HTTPS deployment is a later task. State-changing cookie-authenticated requests must enforce same-origin/CSRF protection.

Session origin, cookie security and the core endpoint scheme use the actual
accepted transport. The shared HTTP/RESP multiplexer exposes TLS connection state
only for encrypted connections; plaintext wrappers must not advertise an empty
TLS state to Go's HTTP server. Same-origin checks remain strict for both schemes.

Sessions last 12 hours without sliding renewal. A `service` principal cannot sign
into the dashboard. Session responses map `member` to the dashboard role `user`;
user records retain `member`. Passwords are 12–72 UTF-8 bytes. Organization-admin
user creation/update accepts a write-only `password` field. Self-service password
changes use `POST /v8/neo/users/:id/password { currentPassword, password }`;
administrators may reset another user's password without its current value.
Changing a password revokes all existing sessions and inference keys. Suspension
revokes sessions but retains keys, which are rejected while the user is suspended.

Every dashboard mutation, including login/bootstrap/logout, requires an exact
same-origin `Origin` header or, for clients omitting `Origin`,
`X-Neoproxy-CSRF: 1`. Cross-site fetch metadata is rejected. The header is not an
authentication credential; cookie authority and current server-side roles are
still checked. An inference key cannot register a `/v1/ws` backend worker, mint a
realtime client secret, or enter management/plugin administration.

The first organization admin is provisioned through an explicit bootstrap operation guarded by the existing operator management authority, not open registration or a default password. Bootstrap must not permit creation of another organization admin once setup has completed. Username/password sessions are the only ordinary dashboard login; an inference key does not become a dashboard session.

A user inference key has format `nk_<48 hex>`. The dashboard displays `nk_xxxx…xxxx`; the server stores only its hash and rejects revocation immediately. Every request and `/v1/models` listing is restricted to that user's current grants and any narrower account scope on the key. Owners may mint keys over their own account pools, never another owner's pool.

Responses also accepts authenticated WebSocket upgrades at `GET /v1/responses`
and `GET /backend-api/codex/responses`. Consumer sockets retain the scoped
principal and normalize each `response.create` or `response.append` continuation
through the same virtual-model resolver as HTTP inference. Current key validity,
grants and narrower account scope are checked for every generating turn; opening
a socket does not freeze authority or expose the engine's global model catalogue.
The bridge retains conversation history so each turn may select a currently
authorized account without depending on a previous account's response ID.
Selected Codex accounts still use their configured upstream WebSocket transport;
consumer frames do not enter direct Codex duplex passthrough. Existing
engine/operator-key native and duplex socket paths are unchanged.

An owner key must name only accounts currently owned by that user. A foreign,
missing, mixed-ownership or unrestricted scope is rejected with HTTP 403
`neoproxy_forbidden`, without creating a key. Unexpected database errors remain
server errors rather than being mislabeled as authorization denials.

Organization admins manage users and organization-wide policy. Account owners manage their own provider accounts and pool-scoped keys, and see only statistics for their owned accounts. Ownership is not organization-admin authority. Member/service access is scoped to their grants, own user and requests regardless of supplied query parameters.

All routes below live under `/v8/neo/` unless stated. Errors are `{ error: { message: string, code?: string } }` with the appropriate status. Timestamps are epoch milliseconds. Fractions are `0..1` floats.

## Core

```
GET /v8/neo/core
→ {
    linked: boolean,
    endpoint: string,        // this proxy's inference endpoint, e.g. "http://127.0.0.1:8317"
    identity: string,        // this proxy's instance identity, e.g. "neoproxy@localhost"
    version: string,         // running NeoProxy engine version
    host: string,            // short host name shown in the topbar chip
    heartbeatAt: number,     // last successful heartbeat
    scopes: string[]         // authenticated session capabilities, e.g. "models:read", "usage:read"
  }
```

`linked` reports that the dashboard is connected to its authoritative NeoProxy engine. It does not mean a connection to a NeoPi credential broker. Core status must be real process/status data, not the old fixture's claimed NeoPi identity.

## Vault

```
GET /v8/neo/vault
→ { providers: Provider[], accounts: Account[] }

Provider {
  id: string,              // "codex"
  name: string,            // "Codex"
  vendor: string,          // "OpenAI"       display
  kind: "oauth" | "api-key",
  exposed: boolean         // served to users at all
}

Account {
  id: string,              // stable, opaque: "codex-a"
  providerId: string,
  ownerId: string,          // owning NeoProxy user; only org admins may reassign another user's account
  label: string,           // masked identity: "shayna@…", "key …7f3a". NEVER a full email or key.
  plan: string,            // "Pro", "Max 20×"   display
  state: "healthy" | "cooldown" | "expired" | "disabled",
  cooldownUntil?: number,  // when state is cooldown
  windows: Window[]
}

Window { kind: "5h" | "day" | "week" | "month", used: number /*0..1*/, resetsAt: number }
```

```
PATCH /v8/neo/vault/providers/:id   { exposed: boolean }
```

Hiding a provider **must** remove every one of its models from every user, including direct access, and make every stage that targets it unservable (the chain skips it as `unhealthy`).

Account health, credentials, refresh and quota-window observations come from the proxy's existing provider managers and login/API-key flows. Each account has a persisted `ownerId`. Provider tokens stay in the engine's existing credential store; ownership, exposure, users, sessions, keys, grants, routing and usage state live in SQLite under the engine's data directory. Missing quota measurements must remain unknown or explicitly configured; they must not be invented as zero use.

## Catalogue

```
GET /v8/neo/models
→ Model[]

Model {
  id: string,              // "<provider>/<model>", e.g. "codex/gpt-6-astra"
  providerId: string,
  name: string,            // "gpt-6-astra"
  display: string,         // "GPT-6 Astra"
  served: string,          // "neoproxy/codex/gpt-6-astra"
  tags: string[],          // free-form: "reasoning", "fast", "code", "vision", "voice", … display
  context: number,         // context window in tokens; 0 if not applicable
  exposed: boolean
}

PATCH /v8/neo/models/:id   { exposed: boolean }    // :id is the url-encoded "<provider>/<model>"
```

The catalogue is derived from the proxy engine's registered provider models and accounts. `exposed` is the admin's switch and is persisted by the proxy. NeoPi discovers the authorized served catalogue through `/v1/models` using a user inference key.

## Virtual models

```
GET    /v8/neo/virtual-models            → VirtualModel[]
GET    /v8/neo/virtual-models/:id        → VirtualModel
POST   /v8/neo/virtual-models            VirtualModel (without id) → 201 VirtualModel
PUT    /v8/neo/virtual-models/:id        VirtualModel → VirtualModel
DELETE /v8/neo/virtual-models/:id        → 204   (also removes every grant that references it)

VirtualModel {
  id: string,              // opaque, "vm-heavy"
  name: string,            // "heavy"
  served: string,          // "neoproxy/heavy" (derived; server fills it)
  description: string,     // one sentence shown to users
  stages: Stage[],         // ordered; at least one
  terminal: Terminal,
  script?: string,         // optional admin-authored routing source; absent = standard chain
  updatedAt: number,
  updatedBy: string        // handle
}

Stage {
  id: string,              // stable within the virtual model; the dashboard generates "s<rand>" for new ones
  model: string,           // "<provider>/<model>"
  accounts: "all" | string[],   // account ids within that provider; "all" follows the pool as it changes
  draw: "alternate" | "least-used" | "fill",
  perAccount: number,      // 0..1 of each account's window this stage may draw
  cap: number,             // 0..1 total across the stage's accounts
  window: "5h" | "day" | "week" | "month",
  per: "user" | "all",     // whether the budget is per granted user or shared by all
  effort: null | "low" | "medium" | "high" | "max"   // force the reasoning effort; null = caller's choice
  lua?: {                 // named functions defined in VirtualModel.script
    eligible?: string,    // function(ctx) -> boolean, per authorized account
    perAccount?: string,  // function(ctx) -> finite 0..1 window fraction
    cap?: string          // function(ctx) -> finite 0..1 stage fraction
  }
}

Terminal {
  kind: "limit" | "queue" | "passthrough",
  message: string,         // returned to the caller on limit (and while queued)
  model?: string           // for passthrough: "<provider>/<model>" served with no budget
}
```

Validation the backend **must** enforce: name pattern; unique name; not equal to any provider id or served model id; at least one stage; every stage's model exists in the proxy catalogue and belongs to a configured provider; `accounts` ids belong to that provider and satisfy the caller/key's ownership scope; `perAccount ≤ cap` is *not* required. A cap lower than one sip is allowed and simply ends the stage early.

### Usage

```
GET /v8/neo/usage
→ { [userId]: { [virtualModelId]: { [stageId]: { [accountId]: number /*0..1*/ } } } }
```

The fraction of an account's **current** window (the stage's `window` kind) that this user has drawn through this stage. The backend keeps this per `(user, virtualModel, stage, account, window)` and resets it when the account's window resets. For stages with `per: "all"` the backend may still report per-user rows; the dashboard sums them.

This is the one data structure the simulator, the rings, the flow-board ribbons and the member view all read. Get it right and every page is right.

### Resolution (the algorithm)

`internal/neoui/static/lib/fixture.js › resolve()` is the reference implementation. Given a request for `asked` from `user` at time `at`:

1. Strip `neoproxy/`. If the remainder has one segment it is a virtual model name; otherwise it is `<provider>/<model>`.
2. **Provider model**: the user **must** have `providerId` in `providerAccess`, the provider and model **must** be exposed, and a healthy account **must** exist. Serve from the first healthy account (the engine's own routing strategy applies here). No budget is applied. Outcome `ok` with `direct: true`.
3. **Virtual model**: the user **must** hold a grant; `allot = grant.allotment`.
4. For each stage in order:
   - `pool` = accounts of the stage's provider, filtered by `stage.accounts`.
   - `used(a)` = the usage fraction for this stage and account, per user (`per: "user"`) or summed over all users (`per: "all"`).
   - If `Σ used(a) ≥ stage.cap × allot` → skip (`cap`).
   - `candidates` = pool members that are `healthy` and have `used(a) < stage.perAccount × allot`. None → skip (`unhealthy` if any pool member is not healthy, else `per-account`).
   - Choose: `fill` → first candidate in pool order; `least-used` → the candidate with the lowest `used`; `alternate` → rotate through candidates on successive requests (the fixture rotates by wall-clock second; the backend should keep a per-stage cursor).
   - Serve. Record usage against `(user, vm, stage, account)`. Apply `effort` if set. Outcome `ok` with `stageIndex`, `stageId`, `model`, `accountId`.
5. No stage served → the terminal:
   - `limit` → HTTP 429, body `{ error: { message: terminal.message, code: "neoproxy_limit", virtualModel, resetsAt } }`. Outcome `limited`.
   - `queue` → hold the request until some stage can serve (bounded by the client's timeout; the backend should send keep-alive on streaming responses). Outcome `queued`.
   - `passthrough` → serve `terminal.model` as in step 2 but without the direct-access check. Outcome `ok` retains `virtualModel`, `model` and `accountId`, but has no `stageId`, `stageIndex` or `direct` flag. It does not draw a stage budget. A missing or hidden target returns `error` with `reason: "unknown-model"`; a target without a healthy account returns `error` with `reason: "no-healthy-account"`. The simulator highlights the terminal and explains the selected model/account or the failure instead of reporting a limit.
6. User-level `limits.rpm` and `limits.tokensPerDay` are hard ceilings checked before step 4; exceeding them is a 429 with `code: "neoproxy_user_limit"`.

The decision **must** be recorded on the request (see Requests › `decision`), including the `trail`, so the Activity page can explain it.

```
POST /v8/neo/simulate   { asked: string, userId: string, protocol?: "openai" | "openai-response" | "claude", request?: JSON }
→ Decision
```

Pure: the server runs the same resolver against current grants and usage without recording activity, admitting a request, reserving usage or advancing a cursor. The dashboard simulates only saved models and invalidates the result as soon as a draft changes; save before running it again. Optional protocol and credential-free business-request JSON let a configured script inspect representative input. Simulation never grants a hypothetical 100% allotment or runs a local production fixture fallback.

Execution reserves a conservative token upper bound against explicitly configured
metered windows before dispatch, then replaces that reservation with the existing
engine reporter's actual account-attributed input/output usage. Text requests use
an explicit protocol output maximum plus input bytes; requests without a usable
bound require a known catalogue context ceiling. Unknown provider quota remains
unknown. An executed attempt with no trustworthy usage report retains an explicitly
estimated upper-bound debit rather than inventing zero usage. Cached tokens are a
subset of input tokens and are not counted twice.

The alternate cursor and ledger reservation commit in one SQLite transaction.
Simulation reads the cursor, current grants and current-window usage, but never
advances the cursor, admits a request, reserves usage or records an activity row.
Queue waits wake on current authority, inventory or ledger changes and reset
boundaries; cancellation is inherited from the client. Queued streams emit SSE
comments while waiting, without replaying any delivered answer.

Actual upstream failure records `skipped: "upstream-error"` and `failureStatus`
on the failed stage and demotes to a later eligible stage before output. Once an
answer chunk is delivered, subsequent errors terminate that stream; they do not
restart the chain. `X-NeoProxy-Request-ID` identifies the persisted request record.

A configured `script` is limited to 16 KiB, requires an installed evaluator, and
cannot silently fall back to the ordinary chain on an evaluator error. A stage's
optional `lua` object names boolean eligibility and numeric allocation/cap
functions in that source. `eligible(ctx)` runs per currently authorized candidate;
false removes that account. `perAccount(ctx)` runs per candidate and `cap(ctx)`
once per stage. Numeric results must be finite fractions in 0..1 (0.05 = 5%),
before grant scaling. Effective limits are `min(static ceiling, Lua result) ×
allotment`; the real ledger reserves these effective limits atomically, including
in-flight usage. Lua cannot restore an excluded account or raise a hard ceiling.

Callback `ctx` is deeply read-only and contains `asked`, `protocol`, `userId`,
`request`, `stage`, `allotment`; account functions also receive `account`,
`window` (the configured local budget window), `used` and `budget` (grant-scaled
stage usage and hard per-account budget). `account.providerWindows` are separate
honest native reports, never local token denominators. Unknown/missing/expired
reports are not unused quota or proof that a vendor has no five-hour window.
There are no credentials, files, network or ambient provider-plan assumptions.

Policy-only chunks define functions without a return. Existing selector chunks
may still return exactly `{stageId, accountId}`; when combined with callbacks,
that selection must survive the reduced eligible set. Static stages retain their
draw order. Wrong types, nonfinite/out-of-range limits, missing functions and
runtime errors fail closed with stage/function diagnostics, rather than trying a
different route. The resolver reloads current authority after evaluation and
uses the same callbacks in simulation, execution and reservation rechecks.
Saving requires an organization-admin dashboard session and validates syntax;
Try it validates behavior against the saved model and selected user's real
grants. Simulation makes no token reservation or promise about a later request's
capacity. Ordinary member views do not expose source.


## Users

```
GET    /v8/neo/users                → User[]
GET    /v8/neo/users/:id            → User            (member: own id only)
POST   /v8/neo/users                User (without id, keys) → 201 User
PUT    /v8/neo/users/:id            User → User     (keys are not editable here)
DELETE /v8/neo/users/:id            → 204            // admin; last active admin is protected
POST   /v8/neo/users/:id/keys       { label, accountIds?: string[], expiresAt?: number } → 201 { key: Key, secret: string }   // secret shown once
DELETE /v8/neo/users/:id/keys/:keyId → 204

User {
  id: string,              // "u-santiago"
  handle: string,          // "santiago"  ^[a-z][a-z0-9-]{1,31}$  unique
  name: string,
  role: "admin" | "owner" | "member" | "service",
  state: "active" | "invited" | "suspended",
  keys: Key[],
  providerAccess: string[],    // provider ids with direct access
  grants: Grant[],
  limits: { rpm: number | null, tokensPerDay: number | null },
  createdAt: number
}
Key   { id: string, prefix: string /*"nk_xxxx…xxxx"*/, label: string, accountIds?: string[] /*narrower scope*/, createdAt: number, lastUsedAt: number | null, expiresAt?: number }
Grant { virtualModel: string /*VirtualModel.id*/, allotment: number /*0..1*/ }
```

- `invited` users have no keys; issuing a key **should** move them to `active`.
- `suspended` users keep their keys; every request with them fails 403 `code: "neoproxy_suspended"`.
- An `admin` user's username/password session opens the organization administration dashboard. An `owner` session manages only its own accounts, scoped keys and account statistics. A `service` user is a machine such as NeoDeck or CI and uses inference keys. No user's inference key is an organization-admin dashboard session.

## Requests

```
GET /v8/neo/requests?limit=&userId=&outcome=&asked=&before=   → Request[]   (newest first; member: own only)
GET /v8/neo/requests/:id                                       → Request

Request {
  id: string,              // "req_3f02"
  at: number,
  userId: string,
  keyId: string | null,
  client: string,          // best-effort from User-Agent / X-Client: "npi", "NeoDeck", "NeoCode", "curl"
  asked: string,           // the model id as sent
  decision: Decision,
  tokens: { in: number, out: number, cached: number },
  latencyMs: number,
  outcome: "ok" | "limited" | "queued" | "error",
  stream: boolean
}

Decision {
  outcome: "ok" | "limited" | "queued" | "error",
  direct?: true,
  virtualModel?: string, stageIndex?: number, stageId?: string,
  model?: string, accountId?: string, effort?: string | null,
  reason?: "unknown-model" | "no-provider-access" | "no-healthy-account" | "not-granted" | "limit" | "queue",
  message?: string,
  trail: TrailStep[]
}
TrailStep =
  | { kind: "stage", index, stageId, model, total, cap, candidates: [{accountId, used, budget}], chosen?: accountId, skipped?: "cap" | "per-account" | "unhealthy" | "upstream-error" | "script" | "lua-policy" | "lua-cap", failureStatus?: number, policy?: {stageId, cap?: number, accounts: [{accountId, eligible: boolean, limit?: number}]} }
  | { kind: "terminal", terminal: Terminal }
  | { kind: "direct", model, accountId }
  | { kind: "denied", providerId? , virtualModel? }
  | { kind: "no-model", asked }
```

A live stream for the Overview ticker is wanted but not assumed: `GET /v8/neo/requests/stream` as SSE emitting `Request` objects. Until it exists the dashboard polls `GET /v8/neo/requests?limit=14`.

## What the dashboard never asks for

- Full emails, full keys, raw tokens, auth-file contents. `Account.label` and `Key.prefix` are already masked server-side.
- Provider pricing or guessed cost. Quota fractions, token counts and monetary cost are distinct. Proxy-owned usage attribution and per-owner statistics come from its own ledger; a NeoPi client's local `/stats` is not the authoritative account ledger.
- Anything from a member session beyond their own user, their own requests, the catalogue, the virtual models they are granted, and the core's `linked` flag.

## How this maps onto NeoPi

NeoProxy is the account, credential and refresh authority. Users log provider accounts into the existing Go engine directly through its provider login flows or API-key configuration. NeoPi connects as an inference client with a scoped user API key and discovers `neoproxy/<name>` entries from `/v1/models`. A NeoPi model role may select one of those virtual models; that client-side role is separate from the proxy's admin/owner/member roles and from the server-side virtual-model chain.

The Go engine is retained for this delivery; there is no rewrite into NeoPi and no dependency on a NeoPi core socket, vault export or credential broker. Proxy-owned state is persisted in SQLite under the engine's data directory. The local end-to-end proof uses a scratch `PI_CODING_AGENT_DIR`, assigns a scratch NeoPi role to a discovered virtual model and proves a real role-selected turn with first-stage failure and fallback success over all three day-one protocols. It must never alter the owner's NeoPi profile.

## Identity persistence and build requirements

`NEOPROXY_DATA_DIR` selects the engine-owned application data directory. Otherwise
an engine with a config file uses `data/neoproxy.sqlite` beside the absolute
config-file path. Provider credential/auth directories are not application state
directories. Empty-config SDK construction does not create a database unless the
environment explicitly selects one or the caller injects `api.WithNeoStore`.
Injected stores remain the caller's responsibility to close. Normal servers
close their own store on shutdown and report initialization errors from `Start`.

Server attachment authenticates reserved `nk_` credentials before dynamic routes.
Only the three day-one inference endpoints and `/v1/models` accept these consumer
principals. Until the scoped catalogue and routing implementation is attached,
they return `503 neoproxy_unavailable`; they never fall through to legacy raw-model
execution or an unfiltered catalogue. Integration calls
`Server.EnableNeoInference()` only after both real scoped paths are installed.
Existing configured engine keys keep their existing inference authority, separate
from dashboard sessions. `Store.Changes()` broadcasts committed identity-policy
changes so queued resolvers can wake and recheck `RefreshPrincipal`; extensions
may call `NotifyChanges()` after committing ownership changes.

Identity uses `github.com/mattn/go-sqlite3 v1.14.52`, requiring `CGO_ENABLED=1`
and a C compiler. The module is pinned to upstream commit
`b0be46fa28d17ee0b65c79774ac0dad84b6db068` (2026-09-05), with Go sum
`h1:wVbm2Qnf4OXkqhBTSPuCRZDRnxfbVrrmiCEroVdog8U=`. Its bundled SQLite is
3.53.4, matching the 2026-07-24 source ID published in SQLite's
[release history](https://www.sqlite.org/changes.html). This includes the
WAL-reset corruption fix absent from the initially inspected 3.50.4 bundle.
The [upstream advisory page](https://github.com/mattn/go-sqlite3/security/advisories)
listed no published advisories at intake.

Before compiling the dependency, inspection covered its module manifest
(no dependency graph or install hooks), CGO build directives, driver registration,
connection initialization/DSN handling, statement preparation/execution, and
extension loading. No process/network execution was found in the inspected Go
surfaces. This is targeted source vetting, not a line-by-line audit of the bundled
SQLite amalgamation or a claim that it has no defects. Application SQL is fixed
and parameterized; callers cannot supply DSNs, SQL, extensions or database files.
The driver is registered without extension loaders or connection hooks. The
database is mode `0600`, newly created data directories are `0700`, foreign keys
are enabled on every connection, and migrations run in immediate transactions.
Account inventory and routing extend this same `neo.Store.DB()` database.
