# NeoProxy

NeoProxy is the Neo ecosystem's inference server, built on
[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It owns provider
accounts and their refresh, user accounts, dashboard sessions, scoped inference
keys and pooled virtual-model routing. **NeoPi is a client of NeoProxy, not its
credential broker.**

The dashboard is at `/neo/`. Administrators connect provider accounts, assign
ownership, create virtual-model chains and grant users access. Users sign in with
username/password and receive their own model catalogue and scoped client keys.
The API supports OpenAI Chat Completions, OpenAI Responses and Anthropic Messages.
Provider credentials stay on the server.

## Office users: connect to the shared service

You do **not** need to run another proxy, clone a repository, install Bun, or write
a model configuration file. Ask your administrator for the **HTTPS NeoProxy URL**
and your NeoProxy username/password. Sign in at `<service-url>/neo/` to see your
access. An operator management secret is never an ordinary user's login.

1. Download the published minimum office client
   [`v18.10.3-nightly.fd5698a0772a.41001835518.1`](https://github.com/PsychedelicShayna/neopi/releases/tag/v18.10.3-nightly.fd5698a0772a.41001835518.1)
   or a later release retaining native NeoProxy login. Its assets are raw
   executables: choose `npi-linux-x64`, `npi-windows-x64.exe`, or the matching
   platform asset, and verify the same release's `SHA256SUMS.txt`. Name it `npi`
   / `npi.exe` on your current terminal PATH. The
   [full installation guide](https://github.com/PsychedelicShayna/NeoProxy/blob/nightly/docs/downloads.md#connect-using-a-released-neopi-client)
   has exact Windows/Linux steps and the verified NixOS host-loader requirements.
   NixOS uses glibc; don't assume the Linux musl client was tested there. A version of
   `18.10.3` alone is not a sufficient release identifier. Check
   `npi login neoproxy --help` for `--url`; don't fall back to source/Bun/manual YAML.
2. Run the released executable:

   ```sh
   npi login neoproxy --url https://YOUR-PROXY-HOST
   ```

   Use the actual URL supplied by your administrator. Enter your username and the
   hidden password when prompted. Account owners also explicitly select their
   owned accounts. NeoPi creates a scoped inference key and configures endpoint
   discovery through its normal authentication storage; the temporary dashboard
   session is logged out. No provider OAuth token is copied to the client.
3. Start `npi`, open `/model`, and select a granted model under NeoProxy. If you use
   model roles, open `/models` and assign the discovered model through the normal
   role picker. Choose a model the server actually lists; don't invent aliases.
4. Send a short prompt, such as `Reply with the word connected.` A returned reply
   proves more than seeing a model list. If the catalogue is empty, your
   administrator must grant a virtual model and make its account pool eligible.
   Do not replace the setup with an operator key or disable TLS verification.

Login is also available through `npi login` → NeoProxy, or `/login neoproxy` inside
NeoPi. The pinned client release contains native connection support; publication
alone is not a claim that a particular office service or account has been tested.
The login, model selection and real reply above remain the functional check.

## Running a server

Use [NeoProxy releases](https://github.com/PsychedelicShayna/NeoProxy/releases),
not an unrelated CLIProxyAPI download. Nightlies are **pre-releases**, so choose an
explicit NeoProxy tag rather than assuming GitHub's `latest` points to one.

- **Windows:** extract the `NeoProxy_<version>_windows_amd64.zip` archive.
- **Linux, including NixOS:** choose the matching `linux_amd64_static` or
  `linux_arm64_static` archive. SQLite works without a system glibc loader; native
  dynamic-library plugins are unavailable in that static variant.
- **macOS:** choose the matching `darwin_amd64` or `darwin_arm64` archive.
- Download `SHA256SUMS` from the same release and verify your archive before
  extracting it. `BUILD.json` records the exact source and binary identity.

Follow the complete [download, private first-admin setup, HTTPS and upgrade guide
on nightly](https://github.com/PsychedelicShayna/NeoProxy/blob/nightly/docs/downloads.md),
also included as `DOWNLOADS.md` in NeoProxy archives. There are no shipped default
users, runtime databases or real credentials. Loopback HTTP is only for local
setup; shared service requires validated HTTPS and private server-side state.
Do not distribute operator secrets, dashboard passwords or provider files as
client configuration.

## Release and branch status

NeoProxy's implemented backend, dashboard and product delivery live on
[`nightly`](https://github.com/PsychedelicShayna/NeoProxy/tree/nightly) and the
`neoproxy-v...` release tags. Its [backend contract and delivery receipts](https://github.com/PsychedelicShayna/NeoProxy/blob/nightly/docs/design/backend-plan.md)
distinguish actual verification from pending deployment or release work.
Published office archives must pass real SQLite bootstrap, user/key creation,
session login/logout and restart-persistence checks on each native runner.
A non-CGO binary can compile with a SQLite runtime stub and is not a supported
NeoProxy office release.

The default branch still carries the earlier upstream-engine source until a
separate promotion. **This README-only landing update does not promote backend
or runtime code.** For an office installation, use a published NeoProxy build and
the instructions tied to its exact release, not a build of the older default ref.

## Upstream and licence

NeoProxy retains CLIProxyAPI's provider translators, executors and reusable Go
SDK. [CLIProxyAPI's documentation](https://github.com/router-for-me/CLIProxyAPI)
and [the fork's preserved upstream documentation](https://github.com/PsychedelicShayna/NeoProxy/tree/nightly/docs/upstream)
remain the reference for the inherited engine. The NeoProxy session, ownership,
grant and virtual-model policies are fork-specific.

Derived from [router-for-me/CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI),
originally written by Luis Pater and maintained by Router-For.ME. Original
copyright notices are retained. The fork and upstream use the [MIT Licence](LICENSE).
