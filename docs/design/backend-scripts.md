# Optional routing scripts: design and dependency audit

This note is committed before evaluator implementation. Slice C owns routing authority; F provides a constrained adapter for C's `ScriptHook`. The signed interface dependency is C commit `30c95002`. Slice D owns the optional administrator-only editor. Empty `VirtualModel.script` preserves the ordinary resolver path. A nonempty source with unavailable evaluator, confinement failure, syntax error, runtime error or invalid proposal fails honestly; it never selects a different route silently.

## Choice and source evidence

Use `github.com/yuin/gopher-lua` **v1.1.2**, upstream commit `b87eac29661715e48e1a2868d76b853e0e757c4c` (2026-04-01), MIT. Official source: <https://github.com/yuin/gopher-lua/tree/v1.1.2>. This is a Go-native Lua 5.1 interpreter, not a JavaScript host, browser or Lua C extension. Its small explicit value interface avoids exporting arbitrary Go objects, database handles or provider objects. `state.go:1406-1433` supports `SkipOpenLibs`; individual library openers allow an explicit capability selection. Lua syntax has ordinary loops, functions, tables and comparisons, sufficient for expressive routing over bounded snapshots without a second application framework.

This is not an in-process hard sandbox. Source inspection found:

- `state.go:2242-2257` implements `SetMx` by sampling **process-wide** Go `runtime.MemStats` every 100 ms, then calling `os.Exit(3)`. It can overshoot and terminates the host. We must not use it in the server or call it a per-state memory limit.
- `vm.go:37-65` checks context cancellation between VM instructions. Compilation and a Go library function are not interrupted by that check. `stringlib.go:392-399` calls `strings.Repeat` directly. A context deadline alone therefore bounds neither allocation nor every CPU path.
- `baselib.go:26-55` includes `dofile`, `loadfile`, loaders, `require`, `print`, environment/metatable access and proxy helpers. Opening base is not itself safe. `baseLoadFile` opens host paths at lines 198-215. We will retain only explicitly selected pure functions, never the complete base surface.
- `oslib.go`, `iolib.go`, `loadlib.go`, `debuglib.go`, coroutine/channel helpers and binary dump/load are unnecessary capabilities and will not be exposed. Even pure string operations may exhaust resources; capability removal is not a memory guard.

Audit performed from a source-only clone under m2 before executing/building the dependency: manifest, license, Makefile, constructors/options, base/library registrations, VM context loop and memory limiter were read. The Makefile invokes a source generator, but ordinary Go package compilation does not execute it. The root package imports Go standard libraries and its own `ast`, `parse` and `pm` packages; the manifest's old readline/logex/test requirements belong to upstream command/tests, not this embedding. No C bridge or native binary is required. Repository metadata observed 2026-10-28: created 2015, not archived, not a fork, 6,998 stars, 717 forks; latest source is April 2026, outside the seven-day cooldown. Recent commits are primarily the long-standing maintainer and include a pattern-stack-overflow fix before v1.1.2. These are provenance signals, not proof of safety. Public GitHub security advisories returned `[]`; the OSV query for the exact Go package/version returned `{}`. These narrowly report no published advisory in those queried indexes, not an absence of vulnerabilities. No paid research service was used. Version and module checksums will be pinned; no automatic script or dependency downloads occur at runtime.

The allocator in `alloc.go:3-78` uses `unsafe.Pointer` and Go interface-layout manipulation to amortize number allocations. Go-native therefore does not mean this interpreter is free of unsafe implementation assumptions. Keeping it in a confined disposable process is intentional.

## Concrete confinement, not a pretend allocator guard

Run every validation/evaluation in a fresh **Linux amd64 subprocess**, re-executing the installed NeoProxy executable in an early internal worker mode before configuration, auth stores, listeners or dotenv are opened. There is no resident evaluator and no credentials in its environment or request. The parent supplies only stdin/stdout/stderr pipes, an empty credential-free environment with fixed Go runtime settings, and a fixed worker argument. There is no shell command construction and no script-derived executable/argument/path.

Before decoding or compiling untrusted input the child installs all controls below, or exits with a confinement error:

1. Linux hard `RLIMIT_AS` of **2 GiB** bounds the worker's entire virtual address space, including Go's reserved ranges; it is not a 64 MiB or 192 MiB RSS promise. Before installing seccomp, the worker reads only its own `/proc/self/statm` and rejects an inherited address space already above that ceiling: lowering a resource limit does not shrink existing mappings. Hard `RLIMIT_DATA=192MiB` remains defense in depth, but **is not relied on as the heap ceiling**: final tests on this host demonstrated a 1 GiB `string.rep` allocation surviving DATA=192MiB with the original AS=4GiB. Tightening AS to 2 GiB makes that allocation fail in the child. `GOMEMLIMIT=64MiB` assists collection only. An unrecoverable Go OOM kills only the disposable worker. At most two workers can reserve up to 4 GiB of virtual address space together; kernel overhead and server memory remain separate.
2. Hard CPU limit **1 second** and zero core/file-size limits. The parent independently kills/reaps the child on cancellation or a **500 ms** wall deadline, including parse/library calls. VM context cancellation would be cooperative only; execution does not rely on it. Parent output handling remains bounded even if the child misbehaves.
3. `PR_SET_NO_NEW_PRIVS` and a seccomp filter synchronized across all child threads. A small allowlist permits only Go runtime memory/thread/signal/timing primitives and reads/writes of the supplied protocol pipes. Filesystem opens, sockets/network, process exec/fork, ptrace, kernel keyrings, mount, and other syscalls are denied. Runtime `clone` is restricted to threads in the worker's own group; `clone3` is unavailable. No filesystem, credentials, provider files, processes or host sockets become Lua capabilities. Unsupported OS/architecture or a rejected kernel filter fails closed.
4. At most **two active children** per evaluator; additional callers fail with a resource-busy error rather than queueing unbounded work. There is no retry framework. Each finished or killed worker is waited/reaped before releasing its slot.

Tradeoff: process startup and Linux-specific confinement cost more latency than an in-process interpreter. This is acceptable for an optional administrator-authored hook. It avoids making the inference server share the interpreter's heap/OOM fate. Unsupported platforms may use ordinary unscripted routing but cannot silently execute configured scripts without these controls. This is resource and host-capability confinement, not a proof against a kernel vulnerability, arbitrary native-code execution or unlimited hostile administrator request volume. The server's existing request admission remains separate.

## Bounded data and proposal interface

C's concrete interface is `Evaluate(context.Context, ScriptInput) (ScriptProposal, error)` with `Source`, `Asked`, `Protocol`, `UserID`, `VirtualModel`, pre-credential client business `Request` JSON, validated stage usage/candidate snapshots and account health. Source is at most **16 KiB**; serialized snapshot at most **256 KiB**, bounded before child launch. The worker protocol itself is bounded, and responses/errors are capped at **8 KiB**. Source is not repeated inside the script's `input`. `input.request` is the JSON business payload before any provider credential/header injection: scripts can inspect messages, Responses input, generation parameters and other client-supplied business fields. Actual HTTP authorization headers/cookies, provider credentials and engine auth metadata never enter the snapshot. The payload is decoded to plain values, never arbitrary Go objects. Oversized or invalid JSON fails honestly rather than truncating prompts or changing routes.

The Lua chunk sees a fresh deeply read-only `input` table (nested arrays use Lua's one-based indexes). Only plain booleans, finite numbers, bounded strings and read-only tables are supplied. A whitelist of pure base helpers plus pure table/string/math helpers is exposed; no Go userdata, channels, closures over authority or writable ledger references cross the seam. Each evaluation gets a fresh state. No mutation can persist to another request.

The original selector interface is retained: return exactly one plain table `{stageId = "configured-stage", accountId = "eligible-account"}`. Keys must be exactly those two; values must be nonempty bounded strings. No metatable-dependent conversion, executable values, unknown keys, model override or terminal override is accepted. If no authorized stage is eligible, the configured terminal remains authoritative.

F validates syntax, result shape and membership in the supplied eligible candidates. C must still check current grants, account-owner/key scope, provider/model exposure, health, budget and transactional reservations against its authoritative store. Script output is only a preference among valid candidates, never a privileged decision. Simulation uses the same optional evaluator without usage writes. Administrator-only virtual-model persistence runs bounded syntax validation on save; runtime and invalid-output errors remain visible in simulation/inference, with no secret-bearing dumps. D uses the same optional `script` field and displays errors from real endpoints.

### Named boolean and numeric pool policies

Stages may add `lua: {eligible: "reported_room", perAccount: "available_share", cap: "pool_share"}`. Each field is optional; an omitted function uses the built-in behavior. Names are simple Lua identifiers of at most 64 characters and refer to functions in the same `VirtualModel.script` source. A policy-only chunk defines the functions and returns nothing. A chunk can also retain its exact stage/account selector return, which must survive all callback reductions.

- `eligible(ctx)` runs per authorized, healthy, metered candidate and returns exactly one **boolean**. `false` excludes that account.
- `perAccount(ctx)` runs per candidate and returns exactly one **finite number in 0..1**. It is an absolute share of the stage's configured local window before grant scaling, not tokens or percent points: `0.05` means 5%.
- `cap(ctx)` runs once per eligible stage and returns the same finite 0..1 units for the whole stage. It has no `ctx.account` or `ctx.window`.

Every configured function runs in the existing confined worker, including numeric functions for candidates whose condition is false. Invalid types, multiple/missing return values, NaN/infinity, out-of-range fractions, missing functions and runtime errors reject the route with a stage/function diagnostic. The effective budgets are `min(static ceiling, returned fraction) * grant.allotment`. Zero is a valid exclusion. The transaction reserves against these exact reduced budgets and all existing in-flight reservations; it never trusts a larger script value.

`ctx` contains read-only `asked`, `protocol`, `userId`, `request`, `stage` and `allotment`. Account callbacks also receive `account`, `window`, `used` and `budget`. `stage.total` and candidate `used` are real usage/reservation fractions; `stage.cap` and candidate `budget` are hard ceilings already scaled by the grant. `window` is the selected **local configured budget meter**. `account.providerWindows` are **separate native reports**; inspect their `known` flags before interpreting `used`. Missing/unknown/expired reporting is not zero usage and does not establish that a plan has no five-hour window. No vendor plan is inferred, and native percentages never supply a token denominator.

For example, deliberately prefer accounts with a *reported* five-hour window below 80%, with a calculated local allocation:

```lua
function reported_room(ctx)
  for _, w in ipairs(ctx.account.providerWindows or {}) do
    if w.kind == "5h" and w.known then return w.used < 0.8 end
  end
  return false -- Unknown is explicitly excluded, not assumed unlimited.
end

function available_share(ctx)
  for _, w in ipairs(ctx.account.providerWindows or {}) do
    if w.kind == "5h" and w.known then
      return math.min(0.05, (1 - w.used) * 0.1)
    end
  end
  return 0
end

function pool_share(ctx)
  return 0.15
end
```

Add a later built-in stage if unknown-reporting accounts should remain an explicit fallback. Static draw order still chooses among candidates admitted by the functions. Current model/account/key grants, local denominators and hard ceilings remain authoritative; neither a true condition nor a result of 1 widens them. Source-backed virtual-only models cannot escape their meters through direct selectors or an unbudgeted passthrough terminal.

The existing Virtual models editor separates **Hard budget ceilings** from **Pool policy**, gives each function an explicit built-in/Lua choice, and supplies the example without overwriting existing source. Save validates syntax; **Try it** calls the same functions as execution and reservation rechecks without recording or reserving usage. The administrator's decision trail shows conditions, calculated fractions and effective grant-scaled ceilings. Member HTTP projections omit named Lua configuration and policy-bearing trails, including effective cap/candidate values; the shared renderer requires an explicit administrator flag to show callback results. This is a narrow guard for the new policy data, not the broader member-only dashboard/role/key migration: pre-existing non-Lua stage visibility and self-issued keys are unchanged. A simulation is not a reservation: a concurrent request can consume capacity before inference is admitted.


## Administrator and embedding interface

Save Lua source in the optional `script` field using the existing administrator-only virtual-model create/update endpoints. An empty string disables the hook. Saving checks syntax without executing the chunk. Runtime behavior is checked by the real simulation endpoint; a successful save does not claim every possible request will produce a valid decision. For example:

```lua
for _, stage in ipairs(input.stages) do
  if stage.skipped == nil then
    local best = nil
    for _, candidate in ipairs(stage.candidates) do
      if best == nil or candidate.used < best.used then best = candidate end
    end
    if best ~= nil then return {stageId = stage.stageId, accountId = best.accountId} end
  end
end
error("no eligible stage candidate")
```

The snapshot's `accounts` list supplies only scoped `{accountId, providerId, state, sourceId?, windows, providerWindows}` records; each window has `{kind, known, used, resetsAt}`. `windows` are configured local meters and `providerWindows` are native reports. Check `known` before interpreting `used`: unknown quota is not zero. `stages` supplies `{stageId, model, total, cap, candidates, skipped?, lua?, allotment, window}`; candidates have `{accountId, used, budget}`. Array indexes start at one. Available helpers are `assert`, `error`, `tonumber`, `tostring`, `type`, `select`, `next`, `pairs`, `ipairs`, math excluding random/randomseed, string excluding dump, and the table helpers. Input tables reject assignment and mutation through table helpers; local tables remain writable. Table iteration order is not a routing guarantee; use ordered `ipairs` arrays when order matters.

The protocol labels are `openai` (Chat Completions), `openai-response` (Responses), and `claude` (Messages). For example, a script may inspect `input.request.messages[1].content` for Chat Completions/Messages or `input.request.input` for Responses before choosing among eligible candidates. Message content can be structured; scripts must handle the real request shape instead of assuming every content value is text. The same read-only and total-size limits apply to nested request data.

The server command dispatches `neoscript.WorkerMain()` at the start of `main`, then attaches `neoscript.New()` through C's `SetScriptHook`. An SDK embedding executable must perform the same early worker dispatch before opening its own credential/configuration stores, and attach one shared evaluator to its routing store. Without worker dispatch, re-execution fails closed instead of silently taking an unscripted route. Do not execute `WorkerMain` in a goroutine or a resident server; its kernel restrictions apply to the entire disposable process.


## Verification

The focused package checks exercise real re-executed children, not an in-process substitute: request-dependent decisions across all three protocol labels; changed usage/health/unknown-quota decisions; deeply read-only input and table-mutator rejection; plain-output/candidate checks; syntax/runtime errors; actual kernel filesystem/network/exec/signal/ptrace denial; CPU deadline, 1 GiB allocation failure, growing tables, cancellation, bounded I/O/admission, concurrent workers and successful recovery. Real identity/pool/routing/HTTP integration tests enforce administrator-only persistence, current scopes/grants/exposure/health/budgets and honest errors. They are synthetic policy fixtures, not genuine provider inference.

`TestInstalledServerScriptSimulationAndInference` additionally requires `NEOPROXY_SCRIPT_TEST_SERVER` pointing to the built `cmd/server` artifact. It launches that executable from an isolated scratch directory with no inherited credentials or profile, bootstraps a dashboard session, configures a local synthetic upstream and meter, saves Lua through the real API and uses a member inference key. Different request bodies select different stages in both simulation and actual HTTP inference; a runtime error must return 422 without upstream dispatch. This proves the installed startup hook and worker dispatch. It does not replace slice E's real-provider fallback receipt. Without the artifact variable this particular check explicitly skips.

The final commands and observed results are recorded in PR #10. Builds use the shared heavy lock; focused checks use the light lock, both through the existing polite relay with its aggregate five-CPU quota and per-job 4 GiB high / 6 GiB hard memory limits. No full suite or deployment belongs to F.

### Local delivery receipt, 2026-10-28

- Executable source pin: `feaf1932873e20d69fbabc3e6979684f3e5a29b5`, including C's signed `828864c0` routing checkpoint and F's installed hook. Subsequent receipt edits do not change the executable source.
- Artifact: `/mnt/alias/m2/agents/scratch/neoproxy-f-server`; SHA-256 `c57f4b1c84650da7bf2a923b8cbd1f87805c7b2ac2313140fe8d43586280bf52`.
- Build passed: `flock /run/user/1000/npi-afk-heavy.lock /home/shayna/.local/share/polite-relay/polite --mem --label neoproxy-f-bound-final-build -- go build -buildvcs=false -o /mnt/alias/m2/agents/scratch/neoproxy-f-server ./cmd/server`.
- Focused checks passed: `flock /run/user/1000/npi-afk-light.lock /home/shayna/.local/share/polite-relay/polite --mem --label neoproxy-f-bound-final-tests -- env TMPDIR=/mnt/alias/m2/agents/scratch NEOPROXY_SCRIPT_TEST_SERVER=/mnt/alias/m2/agents/scratch/neoproxy-f-server go test -count=1 -v ./internal/neoscript`. Package result: `ok`, 3.563 seconds.
- The installed-binary check ran rather than skipped: request-dependent simulation and HTTP inference passed over Chat Completions, Responses and Messages, choosing both configured stages. A script runtime failure returned 422 and caused no upstream request. The upstream was explicitly synthetic and local.
- Earlier checks found two real defects rather than being suppressed: the original DATA/AS combination allowed a 1 GiB allocation, corrected here with the verified 2 GiB outer AS limit; ordinary bounded text was mistaken for media by C's token estimator, corrected in signed C commit `648304a7` and consumed through `828864c0`. Neither is hidden by a weaker assertion or a known-context fixture.
- Merge recommendation: 90% confidence for this optional Linux-amd64 hook. Unsupported kernels/platforms and oversized inherited worker mappings fail closed for configured scripts. Ordinary unscripted routing stays on C's existing path. Integration merge, nightly installation, dashboard Chromium proof and genuine-provider E proof remain with their assigned owners.
