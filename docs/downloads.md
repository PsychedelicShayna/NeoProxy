# Download and run NeoProxy

NeoProxy is the server. It owns provider accounts, users, sessions, scoped inference
keys and pooled virtual-model routing. **An office user connecting to an existing
NeoProxy service does not need to run another proxy.** Get the HTTPS service URL
and a user account from your administrator; sign in at `<service-url>/neo/`.
Client applications use a user inference key, not the operator management secret
or your dashboard password. The administrator supplies the deployed service URL;
this document does not invent a public endpoint.

## Connect using a released NeoPi client

NeoPi connects with the service URL and an inference key (`nk_...`) that an
administrator issued for your user in the dashboard (**Users** → your user →
**Keys** → **Issue key**). NeoPi does not sign in to the dashboard, ask for your
dashboard password or mint keys itself. This key-based login arrived in
[NeoPi #647](https://github.com/PsychedelicShayna/neopi/pull/647), merged into
NeoPi `nightly` as `07fca9db63934ecd518c95fe7ccc3b2f80261c70`. Use a NeoPi release
built from that commit or later. Earlier releases, including
`v18.10.3-nightly.fd5698a0772a.41001835518.1` and
`v18.14.0-nightly.f0137340c065.41058410555.1`, predate it and still use the
retired username/password flow of [NeoPi #602](https://github.com/PsychedelicShayna/neopi/pull/602).
The base version number alone does not tell you which flow a build has.

NeoPi release assets are **standalone executables, not archives**:

| Client system | Asset from the chosen NeoPi release |
| --- | --- |
| Linux x86-64, glibc | `npi-linux-x64` |
| Linux ARM64, glibc | `npi-linux-arm64` |
| Linux musl x86-64 | `npi-linux-musl-x64` |
| Linux musl ARM64 | `npi-linux-musl-arm64` |
| Windows x86-64 | `npi-windows-x64.exe` |
| Windows ARM64 | `npi-windows-arm64.exe` |
| macOS Intel | `npi-darwin-x64` |
| macOS Apple Silicon | `npi-darwin-arm64` |

Download `SHA256SUMS.txt` from the **same NeoPi release**. These client names and
checksum filename differ from the NeoProxy server archives described below.
Keep the release's `LICENSE` and `THIRD-PARTY-NOTICES.txt` when redistributing the
client. No source clone, Bun installation or hand-written `models.yml` is needed.

### Install the released client on Windows

In PowerShell, choose a fresh private directory. Set `$tag` to the NeoPi release
tag you chose above. The example is x86-64; use `npi-windows-arm64.exe` on ARM64.

```powershell
if (Test-Path .\NeoPi-office) { throw "Choose a fresh client directory" }
New-Item -ItemType Directory .\NeoPi-office | Out-Null
Set-Location .\NeoPi-office
$tag = "NEOPI-RELEASE-TAG"
$asset = "npi-windows-x64.exe"
$base = "https://github.com/PsychedelicShayna/neopi/releases/download/$tag"
Invoke-WebRequest "$base/$asset" -OutFile $asset
Invoke-WebRequest "$base/SHA256SUMS.txt" -OutFile SHA256SUMS.txt
$line = Get-Content SHA256SUMS.txt | Where-Object { $_.EndsWith("  $asset") }
if (@($line).Count -ne 1) { throw "Client missing or duplicated in checksums" }
$expected = $line.Split(' ', [System.StringSplitOptions]::RemoveEmptyEntries)[0]
if ((Get-FileHash $asset -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected) { throw "Client checksum mismatch" }
Move-Item $asset npi.exe
$env:PATH = (Get-Location).Path + [IO.Path]::PathSeparator + $env:PATH
npi --version
npi login neoproxy --help
```

The PATH change above is for this terminal only; it does not replace another
NeoPi installation or edit a permanent profile.

### Install the released client on Linux

Use a fresh private directory, set `tag` to the chosen NeoPi release tag and select
the matching asset from the table.
NixOS is a **glibc** distribution; do not treat it as a musl platform or assume
that the official NeoPi musl client has been verified there.

The proven Utopia NixOS 25.11 path uses the official `npi-linux-x64` asset and
that host's **already installed** `nix-ld`. On that configured host, set these
variables in the current terminal before running the installation/login commands:

```sh
export NIX_LD=/run/current-system/sw/share/nix-ld/lib/ld.so
export NIX_LD_LIBRARY_PATH=/run/current-system/sw/share/nix-ld/lib
```

Those exact existing-loader paths were used for native `--version`, `--smoke-test`,
login, discovery and a real RPC reply. This does not install a loader,
hand-assemble a binary or change global NixOS configuration. Another NixOS host
needs its own appropriate loader setup from its administrator; do not blindly
assume the same paths exist. Change the asset assignment below for another
architecture or a genuinely musl-based Linux host.

```sh
mkdir NeoPi-office &&
cd NeoPi-office &&
tag=NEOPI-RELEASE-TAG &&
asset=npi-linux-x64 &&
base="https://github.com/PsychedelicShayna/neopi/releases/download/$tag" &&
curl -fL "$base/$asset" -o "$asset" &&
curl -fL "$base/SHA256SUMS.txt" -o SHA256SUMS.txt &&
sha256sum --ignore-missing -c SHA256SUMS.txt &&
chmod 755 "$asset" &&
mv "$asset" npi &&
export PATH="$PWD:$PATH" &&
npi --version &&
npi login neoproxy --help
```

For macOS, download the matching Darwin asset, compare `shasum -a 256 <asset>`
with its line in `SHA256SUMS.txt`, then make it executable and name it `npi`.

### Log in and select a granted model

The released client's `npi login neoproxy --help` must show `--url`. If it does
not, choose a newer release as described above instead of a source run or manual
provider configuration. Publication and native startup checks alone do not prove
your office service is reachable: the login and real reply below are separate steps.

```sh
npi login neoproxy --url https://YOUR-PROXY-HOST
npi models neoproxy --json
npi
```

Use the actual HTTPS URL provided by your administrator. On HTTPS, NeoPi first
shows the server and CA certificate SHA-256 fingerprints without sending a
credential; compare them with your administrator and confirm. It then asks for
the administrator-assigned inference key in a hidden prompt and validates it with
`GET /v1/models`. The key and the confirmed CA are stored in NeoPi's normal
credential storage, bound to that exact origin; endpoint/model discovery is
configured for you. There is no command-line key flag. Without `--url`, the server
URL is asked for first. The interactive equivalents are `npi login` → NeoProxy and
`/login neoproxy` inside a session; `/logout` → NeoProxy removes the local key
without revoking it on the server.

In NeoPi, open `/model` and choose a model that the authenticated NeoProxy
catalogue actually lists. For role assignments use `/models` and its normal
role picker. Do not invent or shorten a selector: a server model named
`neoproxy/office-cheap` is preserved under the client provider as
`neoproxy/neoproxy/office-cheap`. Send a short prompt such as
`Reply with the word connected.` A real reply proves more than model discovery.
An empty catalogue means the administrator must grant a model and make its pool
eligible; an operator key, TLS bypass or manual model file is not the fix.

## Choose a download

Use only [PsychedelicShayna/NeoProxy releases](https://github.com/PsychedelicShayna/NeoProxy/releases).
Nightly releases are marked **Pre-release** and may not appear behind GitHub's
`latest` link. Choose an explicit **published** tag. The coordinated first office
candidate is `neoproxy-v0.1.0-nightly.20261030.1`, with archive version
`0.1.0-nightly.20261030.1`. The examples below apply once that release is published;
the presence of this guide is not a claim that an unpublished candidate exists.

| System | Archive | Notes |
| --- | --- | --- |
| Linux x86-64 | `NeoProxy_<version>_linux_amd64_static.tar.gz` | Portable default, including NixOS and musl systems. SQLite is compiled in with CGO. Native dynamic-library plugins cannot load in this static build. |
| Linux ARM64 | `NeoProxy_<version>_linux_arm64_static.tar.gz` | Same portable build for ARM64. |
| Linux x86-64 with native plugins | `NeoProxy_<version>_linux_amd64.tar.gz` | Dynamically linked glibc build, built and tested on Ubuntu 22.04 (glibc 2.35). |
| Linux ARM64 with native plugins | `NeoProxy_<version>_linux_arm64.tar.gz` | Dynamically linked glibc build, built and tested on Ubuntu 24.04 (glibc 2.39). |
| Windows x86-64 | `NeoProxy_<version>_windows_amd64.zip` | Native Windows executable with working SQLite. No Go installation is needed to run it. |
| macOS Intel | `NeoProxy_<version>_darwin_amd64.tar.gz` | Native CGO build, tested on macOS 15 Intel. |
| macOS Apple Silicon | `NeoProxy_<version>_darwin_arm64.tar.gz` | Native CGO build, tested on macOS 15 ARM64. |

Every published archive passes real server startup, administrator bootstrap,
user/key creation, session login/logout, restart persistence and SQLite integrity
checks on its own native CI runner. A successful `go build` alone is not the
release check. The archive contains only `neoproxy` (or `neoproxy.exe`), `LICENSE`,
`README.md`, `DOWNLOADS.md`, `config.example.toml` and `BUILD.json`. The manifest
records the full source commit, Go version, platform, CGO setting, plugin variant
and binary SHA-256. No runtime configuration, database or credentials are shipped.

The former upstream `CGO_ENABLED=0` / `_no-plugin` artifacts are deliberately not
published: they compile but replace the required SQLite driver with a runtime
stub. Unverified FreeBSD and Windows ARM64 targets are also omitted rather than
presented as working office downloads. They can be restored after native SQLite
runtime verification exists. macOS native builds are retained. Optional Lua
routing scripts require Linux amd64 confinement; configured scripts fail closed
on other platforms, while ordinary unscripted routing remains available.

## Verify and extract

Download the archive **and `SHA256SUMS` from the same release**. Checksums detect
corruption or an asset mismatch; they are not an independent signature. Releases
are not Windows Authenticode-signed or Apple-notarized. Do not disable system
security globally or accept a similarly named download from another repository.

### Windows PowerShell

```powershell
$version = "0.1.0-nightly.20261030.1"
$tag = "neoproxy-v$version"
$asset = "NeoProxy_${version}_windows_amd64.zip"
$base = "https://github.com/PsychedelicShayna/NeoProxy/releases/download/$tag"
Invoke-WebRequest "$base/$asset" -OutFile $asset
Invoke-WebRequest "$base/SHA256SUMS" -OutFile SHA256SUMS
$line = Get-Content SHA256SUMS | Where-Object { $_.EndsWith("  $asset") }
if (@($line).Count -ne 1) { throw "Archive missing or duplicated in SHA256SUMS" }
$expected = $line.Split(' ', [System.StringSplitOptions]::RemoveEmptyEntries)[0]
if ((Get-FileHash $asset -Algorithm SHA256).Hash.ToLowerInvariant() -ne $expected) { throw "Checksum mismatch" }
Expand-Archive $asset -DestinationPath .\NeoProxy
Set-Location .\NeoProxy
```

Choose a private directory under your user profile, not a shared office drive.

### Linux

```sh
version=0.1.0-nightly.20261030.1
tag="neoproxy-v$version"
asset="NeoProxy_${version}_linux_amd64_static.tar.gz" # use arm64 for an ARM64 host
base="https://github.com/PsychedelicShayna/NeoProxy/releases/download/$tag"
curl -fL "$base/$asset" -o "$asset" &&
curl -fL "$base/SHA256SUMS" -o SHA256SUMS &&
sha256sum --ignore-missing -c SHA256SUMS &&
mkdir NeoProxy &&
tar -xzf "$asset" -C NeoProxy &&
cd NeoProxy
```

On NixOS choose `_static`, not the glibc archive: the static executable does not
require `/lib64/ld-linux-x86-64.so.2` to exist. On macOS choose the matching Darwin
archive; compare `shasum -a 256 <archive>` with its line in `SHA256SUMS`, then
extract it with `tar -xzf`. macOS may request approval for the unsigned executable.

## Start your own local server

These steps are for the administrator of a **new local instance**, not an office
member connecting to the shared service. There are no default users or passwords.
The large `config.example.toml` describes all engine options; do not expose its
placeholder client keys as real access credentials. Generate this minimal private
configuration instead. Keep the operator secret in a password manager **before
first launch**: the engine may replace its plaintext configuration value with a
hash. It is separate from ordinary dashboard passwords and inference keys.

### Windows: create private configuration

Run in the extracted directory. This refuses to overwrite an existing instance.

```powershell
if ((Test-Path config.toml) -or (Test-Path config.yaml) -or (Test-Path config.yml)) { throw "Configuration already exists; do not overwrite it" }
$random = New-Object byte[] 32
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($random) } finally { $rng.Dispose() }
$operator = -join ($random | ForEach-Object { $_.ToString("x2") })
$authDir = (Join-Path (Get-Location) 'auths') | ConvertTo-Json -Compress
$config = @"
config-version = 8
[server]
host = "127.0.0.1"
port = 8317
[server.discovery]
enabled = false
[management]
secret-key = "$operator"
disable-control-panel = false
disable-auto-update-panel = true
[oauth]
auth-dir = $authDir
[access]
api-keys = []
"@
[IO.File]::WriteAllText((Join-Path (Get-Location) 'config.toml'), $config, (New-Object Text.UTF8Encoding $false))
notepad .\config.toml
# Save the generated management secret in your password manager, then close Notepad.
.\neoproxy.exe --config .\config.toml --local-model
```

The configuration is under your private user directory. For a shared Windows
machine, additionally restrict its NTFS ACL and the `auths`/`data` directories to
the service account and administrator before accepting users.

### Linux/macOS: create private configuration

The following one-time configuration generator uses Python 3; the running proxy
itself does not require Python or Go. An administrator may instead create the
same TOML using their normal private configuration tooling.

```sh
umask 077
python3 - <<'PY'
import json, secrets
from pathlib import Path
if Path("config.yaml").exists() or Path("config.yml").exists():
    raise SystemExit("Legacy configuration exists; start the engine to import it instead")
config = f"""config-version = 8
[server]
host = "127.0.0.1"
port = 8317
[server.discovery]
enabled = false
[management]
secret-key = "{secrets.token_hex(32)}"
disable-control-panel = false
disable-auto-update-panel = true
[oauth]
auth-dir = {json.dumps(str(Path("auths").resolve()), ensure_ascii=False)}
[access]
api-keys = []
"""
with open("config.toml", "x", encoding="utf-8") as output:
    output.write(config)
PY
${EDITOR:-vi} config.toml
# Save the generated management secret in your password manager before starting.
./neoproxy --config ./config.toml --local-model
```

For an existing instance, do not run these generators over its configuration.
Starting with a YAML path (or with the default `config.yaml` present) imports it
once to a sibling `.toml` file. An existing TOML sibling takes precedence. The
import is atomic, keeps the source permissions, and never edits the YAML source.
The engine, dashboard saves, and watcher then use that TOML sibling. Saves retain
existing key/table comments and order where possible; layout migration may move
comments. Keep the original YAML as a reference.

Keep `management.disable-control-panel` set to `false`: `true` removes the
dashboard and `/neo/` returns 404. Open **http://127.0.0.1:8317/neo/**. Choose
**First-time setup**, enter a new username and password (12–72 UTF-8 bytes), and
the generated **Operator management secret**. Create the first administrator;
bootstrap then closes permanently.
Afterward, ordinary sign-in uses only username/password. A service principal
cannot sign in to the dashboard.

The server creates `data/neoproxy.sqlite` beside the configuration unless
`NEOPROXY_DATA_DIR` selects another directory. Provider credentials live separately
under the configured `oauth.auth-dir`. Keep both directories private, back them
up together with your configuration, and stop the server before copying database
files. Preserve them when upgrading; **never unpack an upgrade over them and do
not delete the database to fix a login problem**. Stop the process, extract the
new archive into a new directory, verify it, and run it with the existing absolute
`--config` path. The configuration-adjacent data path remains stable.

The administrator then creates users, connects proxy-owned provider accounts,
assigns ownership, configures explicitly metered windows where needed, creates
virtual-model chains and grants users access. Users mint scoped inference keys
in the dashboard. Client model discovery is `GET <service-url>/v1/models` with
that user key; only currently granted selectors are listed. Successful local
startup with an empty catalogue is not proof of configured provider inference.

## Shared service and upgrades

Local HTTP above is loopback-only and unencrypted. Do not change the listener to
`0.0.0.0` and send passwords or inference keys over plain office-network HTTP.
For shared service, the administrator must terminate **validated HTTPS**, preserve
session cookies, and provide the service URL and any required trusted CA through
normal managed-device trust. Never use `curl -k` or disable TLS validation as the
default client setup. Run NeoProxy under a dedicated service account with private
configuration/state permissions and an explicit persistent working directory.
Do not use an inference key as an operator key or expose provider credential files
to clients. Service deployment and provider availability are separate from the
archive's SQLite packaging proof.

## Release maintainers

The workflow runs archive/runtime verification on relevant PRs. Publishing uses
an existing integrated source tag `neoproxy-v<semver>`: push it, or dispatch
**NeoProxy releases** with the exact existing tag. Versions containing `-` are
pre-releases. The single final job writes `SHA256SUMS` only after all seven native
jobs pass, creates a draft, uploads the complete asset set, then publishes it.
Failed builds do not publish a partial office release. A published version is
immutable; a corrected build uses a new tag, not replacement assets. CI artifacts
are retained for 14 days even on a PR. No catalog refresh mutates the tagged source.

Builds require Go 1.26.4 and a C compiler with `CGO_ENABLED=1`. Never release a
`CGO_ENABLED=0` build just because compilation succeeds. The release script checks
Go build metadata, then exercises the extracted archive's real SQLite lifecycle.
The portable Linux job uses native `musl-gcc` with `-tags netgo,osusergo` and
`-linkmode external -extldflags=-static`; Windows uses native GCC with static GCC
runtime linkage. Source identity and artifact checksums are recorded in each
archive, and local configuration/credentials are excluded by an exact file list.
