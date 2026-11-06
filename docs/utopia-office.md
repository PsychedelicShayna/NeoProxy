# Private office services

The units in [`deploy/systemd`](../deploy/systemd/) run two actual products as a
non-root user:

- `neoproxy-office.service` runs the NeoProxy HTTPS server and owns its account,
  user, routing and usage state.
- `npi-office.socket` starts a real `npi --mode rpc --no-ui` agent for each private
  Unix-socket connection. This is not an authentication-broker-only service.

Use a persistent user manager (`loginctl enable-linger USER`, once, as root).
There is no NixOS rebuild or change to another service's configuration.

## Files and identity

Install official release binaries in versioned directories, with `current`
symlinks pointing to the selected versions:

```text
~/.local/lib/neoproxy-office/VERSION/neoproxy
~/.local/lib/neoproxy-office/current
~/.local/lib/npi-office/VERSION/npi
~/.local/lib/npi-office/current
~/.config/neoproxy-office/config.toml
~/.config/neoproxy-office/npi.env
~/.local/share/neoproxy-office/pki/ca.crt
~/.local/share/neoproxy-office/pki/server.crt
~/.local/share/neoproxy-office/pki/server.key
~/.local/share/neoproxy-office/npi-profile/
~/.local/share/neoproxy-office/workspace/
~/.local/share/neoproxy-office/tmp/
~/.local/state/neoproxy-office/auths/
```

Keep configuration, profile, state and private-key directories mode `0700`, and
secret-bearing files mode `0600`. Do not copy an existing NeoPi profile or an
authentication broker's refresh tokens. The service profile is deliberately
separate from the user's ordinary `~/.omp` profile.

NeoPi's NeoProxy login uses the server URL and an administrator-assigned inference
key. That flow arrived in [NeoPi #647](https://github.com/PsychedelicShayna/neopi/pull/647),
merged into NeoPi `nightly` as `07fca9db63934ecd518c95fe7ccc3b2f80261c70`; use a
NeoPi build from that commit or later. Earlier builds, including
`v18.10.3-nightly.fd5698a0772a.41001835518.1`, use the retired username/password
flow. Verify downloaded assets against their release's `SHA256SUMS.txt` before
running them. No source checkout or Bun installation is needed to use the native
binary.

## NeoProxy and TLS

The server configuration uses canonical v8 fields:

- `server.host`: the specific private VPN address, not an all-interface bind.
- `server.port`: the chosen unoccupied port.
- `server.tls.enable`: `true`.
- `server.tls.cert` and `server.tls.key`: absolute certificate and key paths.
- `oauth.auth-dir`: the absolute private engine credential directory above.
- `management.secret-key`: a separately generated operator credential.

`auth.dir` is not the credential-directory setting. An omitted effective
`oauth.auth-dir` falls back to `~/.cli-proxy-api`; that would defeat the intended
profile separation. Verify the effective path before importing credentials.
Systemd `%h` and `%S` specifiers belong in unit files, not server TOML.

If the certificate uses a private CA, transfer the CA certificate through an
already authenticated channel. Verify its identity and the certificate's IP or
DNS SAN. Trust it only for the intended client processes, for example with
`NODE_EXTRA_CA_CERTS`; do not disable certificate checking or alter the user's
global trust store. A verified health probe is:

```sh
curl --cacert /path/to/office-ca.crt https://VPN_HOST:PORT/healthz
```

Initial administrator bootstrap requires the operator credential and the
management remote-access policy. When bootstrapping through the VPN listener,
permit authenticated remote management on that private bind. Dashboard user
sessions, owner permissions and scoped inference keys remain separate from the
operator credential. Never give an inference client the operator secret.

There are two credential paths. A local provider login is managed and refreshed
by NeoProxy. A shared NeoPi source instead leaves provider credentials and refresh
authority with NeoPi's canonical broker. Independent Codex login is optional when
a working shared source is available; never copy the broker's refresh tokens.

### Share logged-in NeoPi accounts

1. Start NeoPi's authenticated auth gateway against its existing broker
   (`npi auth-gateway serve`, or `npi auth-gateway serve --bind=0.0.0.0:4000`
   when NeoProxy runs on another machine). Use its gateway bearer, not a
   provider API key or a NeoProxy inference key. Prefer HTTPS, or carry
   loopback HTTP over an authenticated private tunnel.
2. As an organization administrator, connect the gateway one of three ways.
   Source credentials cannot be supplied or read by ordinary members or
   account owners.
   - **Same machine:** when NeoPi runs as the same user as NeoProxy, NeoProxy
     finds its gateway itself and connects it with one click; the token is
     read from NeoPi's config directory and never reaches the browser.
   - **Connection link:** run `npi auth-gateway link` on the NeoPi machine and
     paste the single `neopi-gateway://host:port?token=…&name=…` link. Its
     name becomes the source name. The link contains the bearer: treat it as
     a password.
   - **By hand:** enter a source name, gateway URL (an optional `/v1` suffix is
     accepted), bearer and default owner for newly discovered accounts.
3. The proxy discovers `/v1/models` and `/v1/neoproxy/accounts`. Each broker
   credential becomes an independently selectable account. In **Account pools**,
   configure an explicit **local day token budget** on the intended accounts.
   Native provider percentages are shown separately: unknown or absent windows
   do not establish unlimited usage or absence of a five-hour limit.
4. The catalogue creates ordinary stored 1:1 virtual models named
   `neoproxy/np-<model-slug>-<stable-id>`. Descriptions identify the source and
   exact provider-qualified model. Edit these routes in **Virtual models**,
   grant the intended models in **Users**, then issue a key scoped to the selected
   accounts. No source model can bypass its virtual route through direct
   provider access. Without a configured local budget and grant, it does not run.

API for the first two paths (session-authenticated administrator; errors are
`{"error":{"message":…}}`):

- `POST /v8/neo/vault/sources` and `PUT /v8/neo/vault/sources/:id` accept
  `{"connectionLink": "neopi-gateway://…", "ownerId": "u_…", "label": "…"}` in
  place of `url` + `bearer`. `label` defaults to the link's `name`; `enabled`
  defaults to `true` for a link. A link with another scheme, no token, or sent
  together with `url`/`bearer` is a `400` naming the problem. `tls=1` in the
  link selects `https://`.
- `GET /v8/neo/access/neopi/local` →
  `{"found":true,"running":true,"url":"http://127.0.0.1:4000","name":"blackswan","accountCount":3,"alreadyConnected":"src_…","reason":"…","startCommand":"npi auth-gateway serve"}`.
  NeoProxy reads NeoPi's config directory (`$HOME/.omp`, honouring
  `PI_CONFIG_DIR` and `PI_PROFILE`) read-only: the listener from
  `auth-gateway.runtime.json` (written by `serve`; default `127.0.0.1:4000`
  without it) and the bearer from `auth-gateway.token`. It probes `/healthz`,
  then lists `/v1/neoproxy/accounts` with the bearer. `accountCount` appears
  only when the gateway accepted the token; `reason` explains anything that
  prevents connecting (not set up, not running, `--no-auth`, token rejected);
  `startCommand` appears when the gateway is not running; `alreadyConnected`
  is the existing source with the same URL or bearer.
- `POST /v8/neo/access/neopi/local/connect` `{"ownerId": "u_…"}` (optional,
  defaults to the caller) creates and syncs that source and returns it like
  `POST /v8/neo/vault/sources`. When it is already connected it returns the
  existing source; when it cannot connect it answers `409` with the reason and
  the detection under `local`.

On **Models**, the search narrows both provider models and virtual names; the
All / Served / Hidden switch applies only to provider models. Virtual results
start at 25, with **Show 25 more**. In a user's **Catalogue**, search before
granting: the first 20 ungranted matches appear with their upstream model, vendor
and source identity. Exact upstream model names remain searchable even when the
stable generated selector is shortened. Account scopes group human account
labels under the gateway's canonical vendor slug and source, with a filter when
there are more than eight accounts; filtering does not change selected scopes.

New source routes use identity-only descriptions (`<model> via <source>`) and
direct unavailable-key callers to their NeoProxy admin. Discovery does not
backfill older descriptions or terminal messages, replace administrator-edited
routes, or rename existing served selectors.

Connections persist with ownership and meters in the existing private
`data/neoproxy.sqlite` (or `NEOPROXY_DATA_DIR/neoproxy.sqlite`). The bearer is
write-only in the dashboard, never exported in account inventory or engine auth
files. Changing a source URL requires entering a bearer again; redirects are not
followed with the bearer. Restarting the proxy restores enabled connections.
Discovery reconciles accounts and catalogue changes every 15 seconds; **Refresh
accounts** requests an immediate check. Failed discovery removes that source's
selectable accounts. A logged-out or disabled broker credential also fails closed
at inference time, before the next inventory check. Every attempt forwards the
selected `X-Neopi-Credential-Id`; gateway retries cannot silently use a sibling.
Configuration reloads preserve the source's live credential resolver while
applying the updated payload rules. They do not replace a shared-source executor
with a generic provider client or bypass source and key revocation checks.

**Disable** stops source inference without discarding connection settings.
**Disconnect** forgets the source connection while retaining account ownership,
meters, virtual routes and historical usage. Neither operation deletes a NeoPi
credential or changes another local provider account. Existing temporary leases,
native accounts and custom virtual models remain in service until their owner
explicitly replaces them.

Chat Completions, Responses and Messages clients use the existing translators,
streaming and measured-account ledger. The current gateway source contract is
HTTP/SSE: it does not advertise a downstream WebSocket endpoint. Native Codex
WebSocket accounts remain on their existing executor path.

For a local credential instead, complete the normal provider consent in this
proxy, assign the account to its owner, configure explicit metered windows and
grant a virtual model. An access-only lease may prove inference, but its expiry
does not establish durable refresh. A local token meter is never a vendor quota.

## Native client and agent profile

Use native NeoPi login with the dedicated profile and process-scoped CA trust:

```sh
PI_CODING_AGENT_DIR="$HOME/.local/share/neoproxy-office/npi-profile" \
NODE_EXTRA_CA_CERTS="$HOME/.local/share/neoproxy-office/pki/ca.crt" \
  "$HOME/.local/lib/npi-office/current/npi" login neoproxy --url https://VPN_HOST:PORT
```

First issue an inference key (`nk_...`) for the profile's NeoProxy user in the
dashboard: **Users** → that user → **Keys** → **Issue key**. On HTTPS the command
shows the server and CA certificate SHA-256 fingerprints before sending anything;
confirm them, then paste the key into the hidden prompt. NeoPi validates it with
`GET /v1/models` and stores the key with the confirmed CA pin in the profile's
credential storage. It never asks for the dashboard password and does not mint a
key. `/login neoproxy` inside a session is the interactive equivalent. Discover
models with `npi models neoproxy --json`; use the returned native selector in
`NPI_MODEL` below, or assign it to a role through the existing `modelRoles`
setting.

Create `~/.config/neoproxy-office/npi.env` with the selected model, for example:

```ini
NPI_MODEL=neoproxy/neoproxy/office-cheap
```

The repeated prefix is intentional: the provider is `neoproxy`, and the server's
model ID is `neoproxy/office-cheap`. Do not rewrite the server ID or hand-author a
model catalogue.

On NixOS, the official GNU/Linux binary can use the host's existing `nix-ld`.
Include the host's actual `NIX_LD` and `NIX_LD_LIBRARY_PATH` in this dedicated
`npi.env`, plus a `PATH` containing its system tools and a `TMPDIR` under the
private disk-backed directory above. Verify `npi --version` and
`npi --smoke-test` with that environment. Do not hand-assemble another binary or
change the global Nix daemon's limits.

## Start, use and stop

Copy the three unit files to `~/.config/systemd/user/`, then run:

```sh
systemctl --user daemon-reload
systemctl --user enable --now neoproxy-office.service npi-office.socket
systemctl --user status neoproxy-office.service npi-office.socket
```

NeoProxy retries a failed start, including a private VPN address not yet being
available during boot. The socket is mode `0600` inside a mode `0700` directory
at `$XDG_RUNTIME_DIR/neoproxy-office/npi.sock`; it is not an Internet listener.
Each accepted connection gets its own templated `npi-office@.service` process.
The workspace and profile remain persistent when the connection ends.

RPC uses newline-delimited JSON. Read the `ready` frame, send a `prompt` command
with an ID, and wait for its successful admission response. A response alone is
not a model answer. Keep the write half open until the matching `prompt_result`
reports `status: "completed"`; if `sessionSettled` is false, also wait for
`session_settled`. Then close the write half and continue reading through EOF to
drain pending output. Closing stdin is session termination, not a request to
finish an admitted generation. The public protocol is documented in
[NeoPi's RPC guide](https://github.com/PsychedelicShayna/neopi/blob/fd5698a0772a555cb554b860d8f3208a0f7026a5/docs/rpc.md).

Do not run this RPC process as an ordinary service with `/dev/null` stdin: EOF
correctly ends it. Socket activation supplies the real connection as stdin and
stdout; stderr goes to the journal without contaminating the protocol stream.

To stop only these services:

```sh
systemctl --user stop npi-office.socket neoproxy-office.service
```

For an upgrade, retain the prior version directory, atomically switch each
`current` symlink, and restart only the affected service. Keep the previous
binary until the new one launches and an authenticated request succeeds. Stop
existing `npi-office@` connections before replacing their selected version if a
fully drained cutover is required. Never reset unrelated configuration, account
state, keys or services to make a deployment check pass.
