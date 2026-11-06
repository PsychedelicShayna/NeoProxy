# NeoProxy

NeoProxy is a fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).
It keeps CLIProxyAPI as the engine and replaces the management interface with a
new, unified web dashboard.

## What it is

- **The engine is CLIProxyAPI.** NeoProxy exposes OpenAI-, Anthropic- and
  Gemini-compatible endpoints and routes them to the providers the engine
  supports. The request handling, provider translators, SDK and management API
  are upstream's work; see [docs/upstream](docs/upstream/README.upstream.md) for
  the original documentation.
- **The interface is new.** The dashboard is designed in the same visual
  language as the rest of the Neo ecosystem, so that every Neo surface reads as
  part of one system rather than a collection of unrelated tools.

## Part of one ecosystem

NeoProxy is one of three pieces that are built to work together:

| Project | Role |
|---|---|
| **NeoPi** | An inference client. Connects with a user key and discovers granted models. |
| **NeoProxy** | Owns provider accounts, users and routing, and serves standard API endpoints. This repository. |
| **NeoDeck** | The team-facing web deck: inbox, workspaces, automations. Uses NeoProxy as its provider. |

The dashboard lives at `/neo/`. Models served by this proxy are named
`neoproxy/<provider>/<model>`. The admin shapes **virtual models**
(`neoproxy/default`, `neoproxy/heavy`, …) as chains of stages drawing from
proxy-owned pooled accounts. Users receive grants, allotments and scoped inference
keys; provider credentials stay in the proxy. Engine administration remains
available to organization-admin sessions, not ordinary member keys.

### How budgets work

Every provider account has, per usage window (`5h`, `day`, `week`, `month`), one
**meter**: the usage the provider reports (Claude and ChatGPT/Codex sign-ins are
polled every five minutes), or a local token meter an admin configured, or none.
An account with no meter is always available. A stage in a virtual model says
"draw from each account while its window is below N%" and "skip this stage once
the pool averages M%"; a person's grant carries an **allotment** that multiplies
every threshold in that chain for them (30% means a stage set to 50% stops at
15% for that person). Virtual models decide routing for `neoproxy/<name>`
requests; the engine's routing strategy only applies to direct provider models.
The Pool page can send one real test request through any account.

### Working on the dashboard

The dashboard is plain ES modules in `internal/neoui/static/` (no build step),
embedded into the binary and served at `/neo/`. To work on it against a running
proxy, run `bun run hack/neoui-dev.ts --upstream http://127.0.0.1:8318` and open
http://127.0.0.1:8411/neo/; it serves the working tree and forwards the API with
the `Origin` header the cookie sign-in expects. Reload the page to see changes. `lib/client.js` is the API client,
`lib/components.js` the shared widgets, `styles.css` the tokens, and each page
is one file under `pages/`.

## Connect from a released NeoPi client

Use the published native client
[`v18.10.3-nightly.fd5698a0772a.41001835518.1`](https://github.com/PsychedelicShayna/neopi/releases/tag/v18.10.3-nightly.fd5698a0772a.41001835518.1)
or a later release retaining NeoProxy login. This is the minimum office client
release containing [native connection support](https://github.com/PsychedelicShayna/neopi/pull/602);
the base version `18.10.3` alone is insufficient. Download its raw executable
(`npi-linux-x64`, `npi-windows-x64.exe`, or the matching platform asset), verify
`SHA256SUMS.txt`, and name it `npi` / `npi.exe` on your current terminal's PATH.
The [installation guide](docs/downloads.md#connect-using-a-released-neopi-client)
has exact Windows/Linux commands. No clone, Bun or manual provider file is needed:

```sh
npi login neoproxy --url https://YOUR-PROXY-HOST
npi models neoproxy --json
npi
```

Use the administrator's actual HTTPS URL and answer the username/hidden-password
prompts. NeoPi mints and stores a scoped inference key, configures discovery and
logs out its temporary dashboard session. Owners explicitly narrow the key to
their owned accounts. Open `/model` to choose a granted NeoProxy model, or
`/models` to assign one through the normal role picker, then send a short prompt.
Use the exact discovered selector, not an invented alias. The full
[released-client instructions](docs/downloads.md#connect-using-a-released-neopi-client)
explain version requirements and the difference between model discovery and a
real reply.

## Downloads and setup

Choose an explicit version from [NeoProxy releases](https://github.com/PsychedelicShayna/NeoProxy/releases);
nightlies are pre-releases. Linux, Windows and macOS archives include their source
identity and must pass real SQLite bootstrap/session/restart checks before publication.
See [download, checksum, local setup and office connection instructions](docs/downloads.md).
An office member connecting to an existing service needs its HTTPS URL and user
credentials, not another locally running proxy.

The users/sessions, owned-account pools, virtual-model routing, dashboard and optional
Lua hook are implemented in the existing Go engine. The product contract and
delivery receipts live in [docs/design/backend-contracts.md](docs/design/backend-contracts.md)
and [docs/design/backend-plan.md](docs/design/backend-plan.md).

## Building

NeoProxy requires Go 1.26+ and a C compiler because its SQLite backend uses CGO:

```sh
CGO_ENABLED=1 go build -o neoproxy ./cmd/server
```

`CGO_ENABLED=0` can compile a binary with a nonfunctional SQLite stub; do not ship
or use it as an office backend. The portable Linux release keeps CGO enabled and
links musl statically instead. Configuration follows `config.example.toml`;
[the setup instructions](docs/downloads.md) create a minimal private local instance
without shipping placeholder client keys.

Configuration is native TOML, with the same kebab-case setting names. Without
`--config`, the engine uses `config.toml` in the working directory. On first use,
an explicitly selected `.yaml`/`.yml` file, or the default `config.yaml`, is
imported to a sibling `.toml` file only if that sibling does not already exist.
The TOML sibling always wins. Import writes the sibling atomically with the
source file's permissions and leaves the YAML source untouched; subsequent loads,
dashboard/API saves, and hot reload use the resolved TOML path.

Dashboard saves write native TOML, not YAML wrapped in another format. Existing
key/table comments and document order are retained where the TOML layout permits;
normalizing or moving legacy settings may move their comments. Legacy import
retains representable comments, but the untouched YAML remains the original
reference. TOML has no `null`: omit a setting to use its default or inherit a group
value. An import that cannot represent the configuration without changing its
meaning fails rather than silently changing a setting.

For an explicit conversion, use `neoproxy config convert --to toml config.yaml`.
YAML export is not supported. The deprecated `/v0/management` interface retains
its legacy compatibility contract; new integrations should use
[`/v8/management`](docs/management-api-v8.md).

The dashboard's identity state uses SQLite in the engine data directory
(`NEOPROXY_DATA_DIR` overrides it). Native Windows drive paths and directory names
containing spaces, `#`, `%`, or Unicode are supported: filesystem paths are escaped
as file URIs, not accepted as caller-supplied SQLite connection strings. Builds
with this backend require CGO and a supported C compiler.

## Credit and licence

NeoProxy is derived from
[router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI),
originally written by Luis Pater and maintained by Router-For.ME, released under
the MIT License. The upstream README is preserved verbatim at
[docs/upstream/README.upstream.md](docs/upstream/README.upstream.md), with its
Chinese and Japanese translations beside it.

This fork is also released under the [MIT License](LICENSE). The original
copyright notices are retained.
