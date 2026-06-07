---
date: 2026-06-07T18:19:50Z
git_commit: ebbf70100fd9fa2fe7ede079478609ece37f80a7
branch: main
repository: agent-creance
topic: "AC-0031: doctor extension (WP-6.2) — CA verify, orphan proxies, exposed host services, flock-unreliable FS, port-change condition, --fix"
tags: [research, codebase, doctor, prereq, lifecycle, ca-verify, sysdep, statfs, lsof]
status: complete
last_updated: 2026-06-07
---

# Research: AC-0031 — `doctor` extension (WP-6.2)

**Date**: 2026-06-07T18:19:50Z
**Git Commit**: ebbf70100fd9fa2fe7ede079478609ece37f80a7
**Branch**: main
**Repository**: agent-creance

## Research Question

How should `agent-creance doctor` be extended (per ticket AC-0031 / WP-6.2) to add: CA live-verify (reusing AC-0026), orphan-proxy scan, exposed-host-service scan, `flock`-unreliable filesystem warning (iCloud/SMB), the port-changed-under-attached-agents condition, plus a `--fix` that remediates what it safely can — while keeping the existing version report and its golden tests green? What exists today, what's reusable, and what new infrastructure (sysdep seams) is required?

## Summary

The current `doctor` (`internal/cli/doctor.go`) is a thin slice: it runs `prereq.Check` and prints `prereq.Report` (version compatibility only), returning a non-zero error only when a tool is missing. Its own header comment already pre-declares the five additions AC-0031 targets. The extension is **additive** — new report sections appended after the version block, and a new `--fix` flag.

Two of the six checks reuse fully-built, fully-wired subsystems:

- **CA live-verify** → call `setup.NewInstaller(...).Verify(ctx)` (AC-0026). All seven seams it needs are already `App` fields wired in `Main()`; **no new App wiring** for this. Mirror `runSetup` (`internal/cli/setup.go:38-42`).
- **Orphan-proxy scan + `--fix` cleanup + port-change condition** → reuse the AC-0020 lock schema (`proxy.lock` JSON: `proxy_pid`/`port`/`policy_hash`/`agents`) and liveness primitives (`ProcessManager.Alive`, `PortAllocator.Probe`). The composite "proxy is up" logic to copy is `internal/proxy/lifecycle.go:112-116`; the ready-made teardown for `--fix` is `Manager.Detach` (`lifecycle.go:152-182`).

Two of the six checks need **new sysdep seams** (no existing coverage):

- **Exposed-host-service scan (0.0.0.0)** → no network-listener enumeration exists. `PortAllocator` is loopback-only. Need a new seam wrapping `lsof`/`netstat` (or `libproc`).
- **flock-unreliable FS warning (iCloud/SMB)** → no `statfs`/filesystem-type seam exists. Need a new seam wrapping `unix.Statfs` (`Fstypename` + `MNT_LOCAL` flag, plus path-match for iCloud).

The remaining checks are wiring/glue: the **version report** stays as-is (it already surfaces every skew incl. patch — `Skew.Loud()` is only consulted by `run`/`setup`, not `doctor`).

Two enumeration **gaps** must be filled regardless: `sysdep.FileSystem` has no `ReadDir`, and `state.Resolver` has no `ProjectsRoot()` — so there is currently no way to enumerate `~/.cache/agent-creance/projects/*/proxy.lock` across projects. AC-0032 (`status`/`clean`) needs the same enumeration; AC-0031 will introduce it first.

## Detailed Findings

### Current `doctor` command + `prereq` package (the extension point)

**`internal/cli/doctor.go:14-38`** — `newDoctorCmd(app *App)`. The whole command:
1. `tools := prereq.DefaultTools(app.Tested)` (`:22`)
2. `results := prereq.Check(cmd.Context(), app.Commander, tools)` (`:23`)
3. `fmt.Fprint(app.Stdout, prereq.Report(results))` (`:25`) — `Report` returns one string ending in `\n`.
4. Missing/exit logic (`:30-34`): if `prereq.MissingInstructions(results) != ""`, write blank line + instructions, then `return fmt.Errorf("%d prerequisite(s) missing", ...)`. Non-nil error → `cli.Main` prints `error: ...` to stderr and exits 1 (`cli.go:106-110`). Otherwise returns `nil` → exit 0.

The header comment (`doctor.go:11-13`) explicitly names "orphan proxies, CA trust, exposed host services" as landing "as those subsystems are built" — i.e. AC-0031.

**`internal/prereq/prereq.go`** — types and entry functions:
- `Tool{Name, VersionArgs, Tested, InstallHint}` (`:22-31`); `DefaultTools(tested)` returns `agent-safehouse` + `mitmproxy` (`:36-51`).
- `Result{Tool, Installed, Version, Skew}` (`:54-60`).
- `Check(ctx, cmd sysdep.Commander, tools) []Result` (`:65-90`) — never errors; per tool does `LookPath` + `Output(--version)` + `classify`.
- `Missing(results) []string` (`:94-103`); `MissingInstructions(results) string` (`:108-132`).

**`internal/prereq/report.go`** — the rendering extension point:
- Glyph constants `glyphOK="✓"`, `glyphWarn="⚠"`, `glyphMiss="✗"` (`:10-14`) — kept as constants so golden file and code agree byte-for-byte.
- `Report(results) string` (`:20-48`) — header `"Version compatibility:\n"`, two-pass column alignment, per-row `installedField`/`statusField`, trailing 4-line advisory. **New sections would be appended here (or composed in `doctor.go` after this call).**
- `installedField` (`:51-60`), `statusField` (`:63-76`).

**`internal/prereq/version.go`** — `Skew` enum (`SkewExact/Patch/Minor/Major/Unparseable`, `:10-25`), `Skew.String()` (`:27-40`), `Skew.Loud()` → true only for Minor/Major (`:44-46`, used by run/setup not doctor), `classify(installed, tested)` (`:79-95`). **Doctor already surfaces every skew incl. patch** — AC requirement is already met by the current `Report`.

**Golden-test mechanism** — `internal/prereq/report_test.go`: external `prereq_test` package, `var update = flag.Bool("update", ...)` (`:21`), fixed `goldenResults()` fixture (`:25-34`), golden file `internal/prereq/testdata/doctor_report.golden`. `make golden` = `go test ./... -update`. `version_test.go` is internal-package table tests of `classify`.

**Testscript fixtures** — `internal/cli/testdata/script/doctor_healthy.txtar` and `doctor_missing.txtar`. Harness `script_test.go` registers `agent-creance → cli.Main()` (`:26-30`) and exposes `$CREANCE_BIN` (the testscript bindir holding the CLI stub, `:38-52`) for a minimal PATH. Healthy fixture stubs `bin/agent-safehouse` (echoes `0.10.1`) + `bin/mitmproxy` (echoes `12.2.3`) matching `buildinfo.TestedVersions` → exact-match `✓`. Missing fixture uses `PATH=$CREANCE_BIN` only and asserts non-zero exit + `not installed`.

**`App` struct (`internal/cli/cli.go:20-52`)** — all injected seams (every one already wired in `Main()`):
`Commander` (`:21`), `Stdout`/`Stderr` (`:22-23`), `Tested` (`:25`), `FS` (`:28`), `Paths` (`:29`), `Clock` (`:30`), `HTTP` (`:31`), `Keychain` (`:35`), `ProcessGroup` (`:39`), `Flock` (`:44`), `ProcessManager` (`:45`), `PortAllocator` (`:46`), `TLSProber` (`:50`), `Sleeper` (`:51`).

### CA live-verify reuse (AC-0026)

The reusable surface is `(*setup.Installer).Verify(ctx) (setup.Result, error)` — `internal/setup/setup.go:203-226`. It:
1. `ports.Allocate()` → ephemeral loopback port,
2. `proc.Spawn(ctx, "mitmdump", bareMitmArgs(port)...)` → bare mitmdump (`--listen-host 127.0.0.1 --listen-port <port> -q`),
3. `defer proc.Signal(pid, SIGTERM)` tears it down,
4. `prober.ProbeViaProxy(ctx, "http://127.0.0.1:<port>", "https://example.com")`,
5. maps outcome: `ProbeTrusted → Result{StatusTrusted}, nil`; `ProbeUntrusted → Result{StatusUntrusted}, nil`; probe error → wrapped error; other → "could not validate" error.

**Status-as-data contract:** a non-nil error means a genuine *environment* failure (port alloc / spawn / probe-couldn't-run); an untrusted `Result` is a trust *finding*. `setup.Result` (`setup.go:160-191`): `Status` (`StatusTrusted`/`StatusUntrusted`), `OK()` → trusted, `Message()` → `""` when trusted else a golden-pinned actionable string — **render `Message()` for an untrusted CA finding**.

**Critical:** `Verify` does NOT pass `--cacert`/`-k` — trust must come from the system store, which is what catches the silent `security add-trusted-cert` cancel (`tlsprober.go:77-79`, design.md:443). `OSTLSProber.ProbeViaProxy` runs `curl -sS -o /dev/null --proxy <proxyURL> --retry 5 --retry-connrefused --retry-delay 1 <targetURL>`; `ClassifyCurlExit`: `0→Trusted`, `60→Untrusted`, else `Error` (`tlsprober.go:57-66`).

**Do NOT use `Bootstrap`** (`setup.go:232-248`) — it chains `EnsureCA → InstallCA → Verify` and *mutates* trust (runs `security add-trusted-cert`, may prompt). Doctor's "live-verify" is `Verify` alone. (Open question: whether doctor should call `EnsureCA` first to tolerate a not-yet-generated CA — `Verify` does not generate.)

**Construction (mirror `runSetup`, `internal/cli/setup.go:38-42`):**
```go
setup.NewInstaller(app.FS, app.Keychain, app.ProcessManager, app.PortAllocator,
    app.TLSProber, app.Sleeper, app.Paths).Verify(ctx)
```
All seven seams are already `App` fields wired in `Main()` — **no new wiring**. (`Verify` itself only exercises `ports`, `proc`, `prober`.)

**Cheap-vs-live split is load-bearing:** `run` uses the cheap `setupcheck.Verify` (keychain presence only, `internal/setupcheck/setupcheck.go:100-125`); the live `setup.Verify` is reserved for `setup` and `doctor`. design.md:443: "The same verification runs as part of `agent-creance doctor` on every invocation."

**Test surface:** mirror `setup_test.go:33-56` with `sysdeptest.FakeTLSProber` (`.Outcome`/`.Err`, records `.Calls`), `FakeProcessManager`, `FakePortAllocator`, etc. — fully hermetic.

### Orphan proxies, locks, port-change (AC-0020)

**Lock schema** (`internal/proxy/lifecycle.go:36-45`) — the de-facto public contract a doctor reader must unmarshal:
```go
type lockState struct {
    ProxyPID   int    `json:"proxy_pid"`
    Port       int    `json:"port"`
    PolicyHash string `json:"policy_hash"`
    Agents     []int  `json:"agents"`
}
```
Written in-place (not temp+rename) as indented JSON, mode `0600`. Path = `state.Layout.ProxyLock()` → `<cache>/agent-creance/projects/<hash>/proxy.lock` (`state.go:188-189`), `<hash>` = SHA-256(realpath(projectdir))[:8] as 16 hex chars. The wire shape is mirrored in tests as `lockJSON` (`once_lifecycle_test.go:16-21`).

**"Proxy is up" composite (copy this — `lifecycle.go:111-116`):**
```go
alive := m.pruneDead(cur.Agents)  // ProcessManager.Alive over each PID
proxyUp := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) && m.ports.Probe(cur.Port)
```
PID-liveness alone is explicitly NOT trusted (recycled PID) — must AND with `PortAllocator.Probe` (TCP dial). An **orphan proxy** = `proxyUp == true` while `pruneDead(Agents)` is empty (live, listening mitmproxy whose every attached agent is dead).

**`--fix` cleanup** — `Manager.Detach(layout, selfPID)` (`lifecycle.go:152-182`) already implements last-out teardown: SIGTERM proxy by PID (`:167-171`), `RemoveIfPresent(SessionOverlay())` (`:172`), clear proxy state in lock keeping the file as flock target (`writeLock(lf, lockState{PolicyHash: ...})`, `:177`). `--fix` can reuse `Detach` or replicate `ProcessManager.Signal(pid, SIGTERM)` + `FileSystem.Remove`/`RemoveIfPresent`. Note: last-out does NOT delete `proxy.lock` today (it clears + retains it).

**Port-changed-under-attached-agents** — `lockState.Port` holds the *current* port. On crash restart, `choosePort(cur.Port)` (`lifecycle.go:199-215`) tries `TryReclaim`; failure → fresh `Allocate()` + `changed=true`. Condition fires when `changed && len(alive) > 0` (`:127`) → `warnPortChanged` prints to stderr (`:219-226`) and **never signals agents**; `Attachment.PortChanged=true`. There is **no persisted old-vs-new port history** — the only persistent, detectable condition is "live attached agents exist but the proxy isn't listening on the recorded port" (compare lock `Port` + `Probe`/`Alive` against `pruneDead(Agents)`). The cage bakes the port into each agent's env + Seatbelt fragment at launch (`cage.go:202`, `cage.go:177-184`).

**Reusable primitives (signatures):**
- `ProcessManager.Alive(pid) bool` (`processmanager.go:62-72`; `kill(pid,0)`, ESRCH→dead, EPERM→alive), `Signal(pid, sig) error` (`:74-89`; ESRCH→nil).
- `PortAllocator.Probe(port) bool` (`portallocator.go:68-75`; 200ms dial), `TryReclaim(port) (ok, err)` (`:56-66`), `Allocate() (int, err)` (`:47-54`).
- `Flock.Acquire(path) (LockedFile, err)` → `LockedFile.ReadAll/Write/Release` (`flock.go:26-105`).
- `FileSystem.ReadFile/Remove`, `RemoveIfPresent(fsys, name)` (`filesystem.go:82-93`).
- `state.New(paths).Resolve(dir) (Layout, err)`; `Layout.ProxyLock()/SessionOverlay()/Root` (`state.go:84-99,188-206`).

### sysdep seams: what exists vs. what's new

**Covered by existing seams:** PID liveness/signal (`ProcessManager`), loopback port probe/reclaim (`PortAllocator`), lock RMW (`Flock`/`LockedFile`), proxy-routed TLS CA verify (`TLSProber.ProbeViaProxy`), file read/remove/stat (`FileSystem`). Every seam has a fake in `internal/sysdep/sysdeptest/`.

**NEW seam #1 — filesystem-type detection.** No `statfs`/fs-type accessor exists. `FileSystem.Stat` returns stdlib `fs.FileInfo` (`Sys()` is `nil` in the fake); `PathResolver` only canonicalises. `golang.org/x/sys/unix` is already a dependency (`flock.go:8`). New interface should wrap `unix.Statfs(path, &buf)` exposing `Fstypename` (`[16]byte`, NUL-trim) + `Flags` (test `MNT_LOCAL == 0x1000`). Logic (pure, table-testable): warn if `Flags & MNT_LOCAL == 0` (network: `smbfs`/`afpfs`/`nfs`/`webdav`) OR resolved path is under `~/Library/Mobile Documents/` (iCloud — modern iCloud is FileProvider over APFS, reports `apfs` + `MNT_LOCAL` set, so **fstype alone won't catch it — must path-match**). Advisory-lock unreliability on SMB/iCloud is real & documented (SMB OFD locks → `EOPNOTSUPP`; iCloud eviction/async sync).

**NEW seam #2 — network-listener enumeration.** No host-wide listener scan exists; `PortAllocator` is loopback-only by contract. New interface should enumerate listening TCP sockets + bind addresses, unprivileged. Recommended impl: shell out to `lsof -nP -iTCP -sTCP:LISTEN -F pcnPTL` (field output: `p`=pid, `c`=cmd, `n`=name, parse `n` starting with `*:` as wildcard/exposed) — with `-n -P`, both `0.0.0.0` and `::` render as `*:port`; `127.0.0.1`/`[::1]` are loopback (not exposed). `lsof` without root only sees your-uid sockets; `netstat -an` sees all listeners but no PID attribution. Pure parse function is table-testable.

**Partial gap — kill process group by bare pgid:** `Process.Signal` (`kill(-pgid)`) needs a live `Process` handle from `Start`; `ProcessManager.Signal` is single-PID only. Orphan cleanup by single PID is fine (that's what `Detach` does); killing an orphan *group* by recorded pgid without a handle is not exposed (likely not needed).

### Design intent, exit codes, and `--fix` semantics (from spec/design)

Six diagnostics required (spec WP-6.2 `:335-341`; ticket `:27`): CA live-verify, orphan-proxy scan, exposed-0.0.0.0 scan, flock-unreliable FS warning, port-change condition, version report (every mismatch incl. patch).

**Exit codes:** ticket says "exit code reflects whether actionable problems remain" (`AC-0031:29`), but the codebase has only a **binary** error→exit-1 mechanism (no distinct codes; `SilenceErrors/SilenceUsage`). So: return non-nil error when unfixed actionable problems remain, nil otherwise. **Which conditions are "actionable" (fail exit) vs. pure warnings is underspecified** — see Open Questions. Version skew currently never fails doctor (design treats even major skew as warn-only on every command, design.md:333).

**`--fix`:** "auto-fix what it can" (spec `:339`); the only concretely-specified fix is orphan-proxy cleanup (spec `:340-341`, ticket `:28,37`), and it "reports what it changed" (`:28`). The warn-never-kill rule means `--fix` must **never kill live (attached) agents** to recover a stranded session.

**0.0.0.0 — design's stance (design.md:36, 106-108):** the cage explicitly does NOT hide host services bound to `0.0.0.0`; the user must bind to `127.0.0.1`. `localhost` in the whitelist matches *every* address on the machine (loopback + interface IPs), so a `0.0.0.0` service on a whitelisted port is reachable. Doctor's job is to **warn**, not block.

## Code References

- `internal/cli/doctor.go:14-38` — current command (the thing being extended); `:11-13` pre-declares the additions
- `internal/prereq/report.go:20-48` — `Report()` rendering (append sections here)
- `internal/prereq/version.go:44-46` — `Skew.Loud()` (run/setup only; doctor shows every skew)
- `internal/prereq/report_test.go:21,38` — golden `-update` mechanism; `internal/prereq/testdata/doctor_report.golden`
- `internal/cli/testdata/script/doctor_healthy.txtar`, `doctor_missing.txtar` — testscript pattern
- `internal/cli/cli.go:20-52` — `App` seams (all wired in `Main()` `:88-105`)
- `internal/setup/setup.go:203-226` — `(*Installer).Verify` (CA live-verify to reuse)
- `internal/setup/setup.go:160-191` — `setup.Result` (`OK()`/`Message()`)
- `internal/cli/setup.go:38-42` — `runSetup` Installer construction (mirror this)
- `internal/sysdep/tlsprober.go:57-95` — curl-based probe + `ClassifyCurlExit`
- `internal/proxy/lifecycle.go:36-45` — `lockState` JSON schema
- `internal/proxy/lifecycle.go:111-116` — "proxy is up" composite (orphan detection inputs)
- `internal/proxy/lifecycle.go:152-182` — `Manager.Detach` (last-out teardown for `--fix`)
- `internal/proxy/lifecycle.go:199-226` — `choosePort`/`warnPortChanged` (port-change condition)
- `internal/state/state.go:188-206` — `Layout.ProxyLock()`/`SessionOverlay()`
- `internal/sysdep/processmanager.go:62-89` — `Alive`/`Signal`
- `internal/sysdep/portallocator.go:47-75` — `Allocate`/`TryReclaim`/`Probe` (loopback-only)
- `internal/sysdep/filesystem.go:19-39,82-93` — `FileSystem` (no `ReadDir`); `RemoveIfPresent`
- `internal/cli/once_lifecycle_test.go:16-50` — driving `Detach` standalone over fakes

## Impact Analysis

### Existing usages that must stay green
- `internal/prereq/*` — version report + golden (`doctor_report.golden`) and table tests. **AC requires these stay green** (`AC-0031:30`). The extension is additive (new sections after the version block), so `Report()` output for the existing fixture should be unchanged unless the version block itself is restructured. Recommendation: leave `prereq.Report` untouched; add new render functions and compose them in `doctor.go`.
- `internal/cli/testdata/script/doctor_healthy.txtar` / `doctor_missing.txtar` — assert substrings, not byte-equality, so new appended sections won't break them *unless* a new section emits text matching a negative assertion (`doctor_healthy` asserts stdout does NOT contain `not installed`). New sections must avoid that exact phrase or the fixture needs updating.
- `internal/setup.Installer.Verify` — read-only reuse; no contract change.
- `internal/proxy.Manager.Detach` / lock schema — if `--fix` reuses `Detach`, no change; if it reads `proxy.lock` directly, it depends on the unexported `lockState` shape (consider an exported reader to avoid duplicating the JSON contract).

### New surface required (no backward-compat concern — all additive)
- New `--fix bool` flag on the doctor cobra command.
- Two new `sysdep` interfaces (+ real impls + fakes in `sysdeptest/`): filesystem-type prober, network-listener enumerator. Each needs `App` fields + `Main()` wiring + fake.
- Cross-project lock enumeration: either add `ReadDir` to `sysdep.FileSystem` (+ fake) and a `state.Resolver.ProjectsRoot()`, or scope the orphan scan to the current project only (see Open Questions). AC-0032 (`status`/`clean`) will need the same enumeration.

## Architecture Documentation

- **Never call the OS from logic packages** — all OS interaction goes through `internal/sysdep` interfaces injected into `App`; tests use `sysdeptest` fakes. The two new checks (fs-type, listener scan) MUST follow this (new interface + fake, not inline `os/exec`/`unix.Statfs`).
- **Status-as-data** — `setup.Verify` returns `(Result, error)` where the verdict is data and error means environment failure. New checks should follow: a finding is a value, an error is "the check couldn't run."
- **Pure logic → table tests; generated output → golden; CLI behavior → testscript.** The fs-type classification and lsof-parse are pure (table-testable); the rendered doctor report is golden/testscript-testable.
- **Out-of-tree state** — locks live under `~/.cache/agent-creance/projects/<hash>/`; don't move in-tree.
- **Live CA verify via proxy, no `--cacert`** — the only way to catch the silent-trust-cancel failure.

## Historical Context (from thoughts/)

- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — WP-6.2 (`:335-341`); version handling (`:320-348`); Commands/doctor (`:375-378`); lifecycle (`:383`).
- `docs/design.md` — version handling (`:320-348`); Commands (`:375-378`); Multi-agent lifecycle (`:389-412`, port-change warn-never-kill at `:410-412`); CA verify on every doctor invocation (`:443`); 0.0.0.0 stance (`:36`, `:106-108`).
- `thoughts/shared/research/2026-06-07-AC-0026-ca-bootstrap-verification.md` + plan — CA reuse surface; cheap-vs-live split.
- `thoughts/shared/research/2026-06-06-AC-0020-proxy-lifecycle-manager.md` + plan — lock/orphan/port model; explicitly defers flock-reliability + orphan detection to AC-0031 (plan `:109-110`, `:618`).
- `thoughts/shared/tickets/AC-0032-status-clean-commands.md:26` — `status`/`clean` enumerate `~/.cache/agent-creance/projects/*/proxy.lock` (shared enumeration; `status`/`clean` themselves out of scope for AC-0031 per `AC-0031:43`).
- `thoughts/shared/plans/2026-06-07-AC-0028-setup-command.md` — added `TLSProber`/`Sleeper` to `App`.

## Related Research

- AC-0026 CA bootstrap verification research/plan (reused here)
- AC-0020 proxy lifecycle manager research/plan (reused here)
- AC-0028 setup command plan (added the last two App seams)

## Open Questions

1. **Exit-code policy.** Which conditions are "actionable problems" that should make `doctor` exit non-zero (untrusted CA? exposed 0.0.0.0 service? orphan proxy when `--fix` not given?) vs. pure warnings that keep exit 0 (patch skew, iCloud/SMB FS, port-changed)? The ticket says "exit code reflects whether actionable problems remain" but the codebase only has binary exit-1. **Needs a decision.**
2. **Orphan-scan scope: current project only, or all projects?** A full scan needs new cross-project enumeration (`ReadDir` on `sysdep.FileSystem` + `state.Resolver.ProjectsRoot()`), which doesn't exist yet and overlaps AC-0032. Scoping to the current project avoids that infra now but is a narrower diagnostic. **Needs a decision.**
3. **CA verify when no CA generated yet.** Should doctor call `EnsureCA` before `Verify` (generating a CA as a side effect — arguably not read-only), or report "CA not generated, run `setup`" without generating? `Verify` alone does not generate.
4. **`--fix` boundary vs. AC-0032 `clean`.** Orphan cleanup overlaps `clean`'s teardown. Should `--fix` reuse `Manager.Detach` (preferred — avoids duplicating teardown) and should a shared helper be factored now or left to AC-0032?
5. **Exposed-service scan implementation:** `lsof` (per-uid without root, structured `-F` output) vs `netstat -an` (all listeners, no PID). Which, and does doctor need PID/process attribution or just "a non-loopback listener exists on a port"?
6. **fs-type warning target path(s):** warn on the project working dir, the cache dir (`~/.cache/agent-creance/`), or both? The lock lives in the cache dir, so cache-on-iCloud is the lock-reliability risk; working-dir-on-iCloud is a separate (cage-mount) concern.
</content>
