---
date: 2026-06-23
ticket: AC-0059
topic: "Host-side integrity — confine config includes, atomic policy writes, sound CA verification"
status: complete
commit: 8288375e6afe74b4c563f47b369f36167f331fa4
branch: main
---

# Research: AC-0059 — Host-side integrity (F8 include confinement, F9 atomic config writes, F10 CA verification soundness)

## Research question

The 2026-06-22 security review grouped three **Important / Should-Fix** findings about
host-side machinery the security model leans on:

- **F8** — `include:` paths in `.agent-creance.yaml` are resolved verbatim (absolute,
  `~`, `..`), so a cloned repo's config can read arbitrary user-readable files and leak
  their contents through parse errors.
- **F9** — `allow`/`deny`/`import` write the config with an atomic temp+rename but **no
  lock and no fresh re-read**, so concurrent edits (or one racing the user's editor) can
  silently drop a rule — dangerously, a `deny_always`.
- **F10** — the *real* CA live-verification prober (`OSTLSProber.ProbeViaProxy`) is only
  exercised through a fake; its soundness (validating against the system trust store with
  no `-k`/`--cacert`/`--proxy-insecure`) is unproven by a real curl.

Goal: understand exact current semantics, the cleanest enforcement/fix points, and the
existing patterns/test harnesses to model the fix on.

## Summary / headline findings

1. **All the seams the fix needs already exist.** There is an established atomic
   temp+rename idiom through `sysdep.FileSystem` (used for `policy.json`, the seatbelt
   `.sb`, the extracted enforcer, etc.) and a full `sysdep.Flock` advisory-lock seam
   (currently used only by the proxy lifecycle). `App.Flock` is already wired into the CLI.
   No new sysdep interface is required for F9 — only its *use* in the config write path.

2. **F8 has one chokepoint:** every include entry — from `resolve`, `collectFiles`, and
   `ValidateInclude` — passes through `resolveIncludePath` (`load.go:285-294`). The implicit
   global config (`~/.config/agent-creance.yaml`) is **not** an include entry; it is injected
   directly as a root path, so it is structurally exempt from any check placed in
   `resolveIncludePath`. A confinement check there covers all four entry points uniformly.

3. **F9's subtlety: flock and temp+rename are mutually exclusive on the *same* file** (the
   `flock.go` header documents this — rename swaps the inode out from under the advisory
   lock). The clean resolution is a **separate lock file** (a different inode) guarding a
   read-fresh → append → temp+rename → recompile critical section. The atomic write already
   exists (`writeFileAtomic`); only the lock + fresh-read-under-lock are missing.

4. **F10 is already sound on audit:** `OSTLSProber.ProbeViaProxy` passes **no**
   `-k`/`--insecure`, **no** `--cacert`, **no** `--proxy-insecure`; outcome is by curl exit
   code (0 → `ProbeTrusted`, 60 → `ProbeUntrusted`). The work is to *prove* it: extract the
   argv into a pure function for a table assertion, and add an integration test where a
   fresh-confdir mitmdump (untrusted CA) yields `ProbeUntrusted`. A reusable live harness
   (`liveInstaller` / `TestVerifyLive`) already exists.

---

## F8 — `include:` path confinement

### Current behaviour

`internal/config/load.go`, `resolveIncludePath` (`load.go:285-294`):

```go
func (l *Loader) resolveIncludePath(declaringAbs, home, inc string) string {
	switch {
	case strings.HasPrefix(inc, "~/"):
		return filepath.Join(home, inc[len("~/"):])   // ~/foo → /home/.../foo
	case filepath.IsAbs(inc):
		return inc                                     // /etc/passwd → verbatim
	default:
		return filepath.Join(filepath.Dir(declaringAbs), inc) // ../../x escapes upward
	}
}
```

`filepath.Join` cleans the result, so `..` segments traverse out of the project dir with no
rejection. There is no check that the result stays under any allowed root.

### Where it is reached (all funnel through `resolveIncludePath`)

- `resolve` (`load.go:193-249`) — value-merging recursive walk.
- `collectFiles` (`load.go:138-187`) — path-accumulating walk for the file-watch set.
- `ValidateInclude` (`load.go:258-280`) — pre-write check for the `include` command.

All four public entry points (`Load`, `ResolveLayer`, `ResolveFiles`, `ValidateInclude`)
route every `cfg.Include` entry through `resolveIncludePath`. Cycle detection (canonical-path
`stack` scan, `ErrIncludeCycle`) and the depth limit (`maxIncludeDepth = 10`,
`ErrMaxIncludeDepth`) live inside each walker and are **correct and well-tested** — leave
them untouched.

### The implicit global include (must stay allowed)

The global is injected directly, not via `resolveIncludePath`:

```go
globalPath := filepath.Join(home, ".config", "agent-creance.yaml") // load.go:55, :82, :118
```

It is passed as the **root** `path` to `resolve`/`collectFiles` with `optional=true`. A
confinement check inside `resolveIncludePath` never sees it → structurally exempt. (Its own
`include:` entries, if any, *would* flow through `resolveIncludePath` and be subject to the
check — which is correct.) There is no helper for "the allowed roots"; the global dir literal
is inlined three times. The exported `GlobalPath()` (`load.go:77-83`) is the closest helper.

### Parse-error content leak

`Parse` (`config.go:144-178`) → `reformat` (`errors.go:54-69`). Syntax errors
(`errors.go:67-68`):

```go
msg := strings.TrimPrefix(err.Error(), "yaml: ")
return &ValidationError{Issues: []string{"invalid YAML: " + msg}}
```

`yaml.v3` syntax errors embed the offending source line/snippet, so file content can surface
in `invalid YAML: ...`. **However**, if confinement rejects out-of-scope includes at
path-resolution time (before `ReadFile`/`Parse`), out-of-scope targets never reach the YAML
parser → no leak. So the AC "out-of-scope/unreadable targets do not echo file contents" is
satisfied by reject-before-read; no separate scrubbing of the parser message is needed for
out-of-scope files. (In-scope-but-malformed files still echo, but those are trusted.)

### Cleanest fix point

`resolveIncludePath` — but it returns only a `string` today; it needs a second `error`
return (or a sibling `resolveAndConfine`) so escapes become an `errors.Is`-checkable error
(new sentinel alongside `ErrIncludeCycle`/`ErrMaxIncludeDepth` in `errors.go:16-23`). Allowed
scope per the ticket's proposed default: **declaring file's directory subtree** ∪ **global
config dir (`~/.config/`)**. Reject absolute/`~`/`..` escapes with a clear error.

### Test to change

`TestResolveFiles_AbsoluteAndHomeIncludes` (`load_test.go:345-359`) currently asserts an
absolute include (`/etc/ac/abs.yaml`) and a `~/frag.yaml` both resolve into the watch set —
i.e. it tests the unconfined behaviour **as supported**. The fix flips this to expect
rejection. Test file conventions: package `config` (white-box), standalone `func Test...`
(not table-driven), `newLoader(map[string]string)` harness (`load_test.go:21-29`),
`testHome = "/home/toby"`, error assertions via `errors.Is(err, Sentinel)` +
`strings.Contains` on the message. `FakePathResolver` (`sysdeptest/pathresolver.go:39-57`)
cleans abs paths and joins relatives onto `Cwd`, enabling exact resolved-path assertions.

### Key files (F8)

- `internal/config/load.go` — `resolveIncludePath` (`:285-294`), `resolve` (`:193-249`),
  `collectFiles` (`:138-187`), `ValidateInclude` (`:258-280`), global injection (`:55,82,118`).
- `internal/config/errors.go` — sentinels (`:16-23`), parse-error reformat (`:54-69`).
- `internal/config/config.go` — `Parse` (`:144-178`).
- `internal/config/load_test.go` — test to change (`:345-359`), harness (`:15-29`).

---

## F9 — atomic + locked config writes

### Current behaviour

In-memory append + re-validation (`internal/config/edit.go`) is **robust** and well-covered:
`AppendRule(src, list, rule) (out, changed, err)` (`edit.go:52-81`) splices rendered lines in
(comment/format-preserving), then `validateAppend` (`edit.go:232-257`) re-parses the candidate
with the strict `Parse` and asserts the diff is exactly "old set + this rule". It operates
purely on the bytes handed in — it does **not** read the file.

The disk write path (`internal/cli/mutate.go`), `applyAndRecompile` (`mutate.go:97-128`):

1. `data, _ := app.FS.ReadFile(path)` — **read once** (`mutate.go:98`; `ErrNotExist` → `nil`).
2. `out, changed, _ := apply(data)` — calls `config.AppendRule` (`mutate.go:106`).
3. no-op if `!changed` (`mutate.go:110-113`).
4. `MkdirAll(dir)` + `writeFileAtomic(app.FS, path, out, configFilePerm)` (`mutate.go:115-118`).
5. `recompile(ctx, app, dir)` (`mutate.go:122`).

`writeFileAtomic` (`cli/init.go:347-359`): `WriteFile(name+".tmp")` then `Rename(tmp, name)`,
`Remove(tmp)` on failure — **atomic single-file replace**, but:

- **No lock** anywhere in `mutate.go`/`import.go`/`allow.go`/`deny.go`.
- **No fresh re-read** — `out` derives from the single read at step 1; two concurrent
  mutations both read the same base and the later `Rename` clobbers the earlier (last-writer-
  wins, losing a rule). The validation gate runs against the stale `before`, so it cannot
  catch the concurrent change either.

`import` (`cli/import.go:45-103`) inlines the **same** read-once → merge → `writeFileAtomic`
shape (no lock) rather than calling `applyAndRecompile`.

Targets (`mutationTarget`, `mutate.go:62-79`): default project `filepath.Join(dir, ".agent-creance.yaml")`;
`--global` → `Loader.GlobalPath()` (`~/.config/agent-creance.yaml`); `--once` →
`layout.SessionOverlay()` (out-of-tree). `import` always targets the project file.

### The seam already exists

`sysdep.Flock` (`internal/sysdep/flock.go:26-105`) — `Acquire(path) (LockedFile, error)`
takes a blocking `LOCK_EX` advisory lock; `LockedFile` exposes `ReadAll`/`Write` (in-place,
truncate-then-write same fd)/`Release`. Real `OSFlock` uses `unix.Flock`. The header
(`flock.go:11-21`) explicitly warns: **a temp+rename swaps the inode out from under the lock
and breaks exclusion** — which is why `proxy.lock` is written in place.

`App.Flock` is already declared (`cli.go:54`) and wired (`cli.go:153`), and tests already
pass `sysdeptest.NewFakeFlock()`. `FakeFlock` (`sysdeptest/flock.go`) takes a **real**
per-path `sync.Mutex`, so `-race` concurrency tests genuinely serialize. The atomic-write
idiom is established (`policy/compile/compile.go:608-630`, `setup/skill.go:52-73`,
`cli/init.go:347-359`). The recent commit `a07ac35` tightened `FakeFileSystem` so `WriteFile`
returns `fs.ErrNotExist` when the parent dir is absent — any temp write needs the parent as a
recorded `Dir` (the existing `MkdirAll` already ensures this).

### Design implication (key decision)

flock-in-place and temp+rename cannot both apply to the **config file** itself. Resolution:
use a **separate lock file** (a distinct inode) to serialize, while keeping the existing
temp+rename for the data write. Shape:

```
Acquire(lockPath)            // flock, blocking — separate inode from the config
defer Release()
data := FS.ReadFile(target)  // FRESH read, now under the lock
out, changed := AppendRule(data, list, rule)  // re-validates against the fresh bytes
if changed { writeFileAtomic(FS, target, out) }  // atomic temp+rename
recompile(...)
```

This satisfies all three F9 ACs: atomic temp+rename (already there), serialized via flock,
re-validated against bytes read at write time (read is now inside the lock). **Open design
choice — where the lock file lives** (one per target: project / `--global` / `--once`):

- (A) out-of-tree, e.g. a hashed lock under `~/.cache/agent-creance/…` — keeps the project
  tree clean, matches the "host-only state out-of-tree" convention, works uniformly for all
  three targets, but needs a path-derivation helper (no global lock dir exists today; the
  proxy lock is per-project via `layout.ProxyLock()`).
- (B) sidecar `<target>.lock` next to the config — simplest and uniform, but creates a
  `.agent-creance.yaml.lock` in the user's repo (needs `.gitignore`, visible in the cage).

This is raised at the question checkpoint.

### Key files (F9)

- `internal/config/edit.go` — `AppendRule` (`:52-81`), `validateAppend` (`:232-257`).
- `internal/cli/mutate.go` — `applyAndRecompile` (`:97-128`), `mutationTarget` (`:62-79`).
- `internal/cli/import.go` — inlined write path (`:45-103`, write at `:95`).
- `internal/cli/allow.go`, `deny.go` — call shapes (`allow.go:37-50`, `deny.go:32-42`).
- `internal/cli/init.go` — `writeFileAtomic` (`:347-359`), `configFilePerm = 0o644`.
- `internal/sysdep/flock.go` — the seam; `internal/sysdep/sysdeptest/flock.go` — the fake.
- `internal/proxy/lifecycle.go` — existing flock-guarded RMW precedent (`:129-142`, helpers
  `readLock`/`writeLock` at `:480,496`).

---

## F10 — CA live-verification soundness

### Audit result (the prober IS sound)

`internal/sysdep/tlsprober.go`, `OSTLSProber.ProbeViaProxy` (`tlsprober.go:73-95`), exact argv
(`:80-84`):

```go
cmd := exec.CommandContext(ctx, "curl",
	"-sS", "-o", "/dev/null",
	"--proxy", proxyURL,
	"--retry", "5", "--retry-connrefused", "--retry-delay", "1",
	targetURL)
```

- **No** `-k`/`--insecure`, **no** `--cacert`, **no** `--proxy-insecure`. The comment
  (`:74-79`) states trust must come from the system store. `--proxy` is plain HTTP (CONNECT
  tunnel), so `--proxy-insecure` is irrelevant; the validated cert is the re-signed leaf.
- Outcome by **exit code** (`ClassifyCurlExit`, `:57-66`): `0` → `ProbeTrusted`, `60`
  (untrusted CA) → `ProbeUntrusted`, else → `ProbeError`. Non-`ExitError` (curl couldn't
  start) → `ProbeError` + wrapped err. Enum at `:33-47`.

### How setup & doctor consume it

`Installer.Verify` (`setup/setup.go:223-246`): allocate loopback port → spawn bare
`mitmdump --listen-host 127.0.0.1 --listen-port <port> -q` → `defer SIGTERM` →
`prober.ProbeViaProxy(ctx, "http://127.0.0.1:<port>", "https://example.com")` → map
`ProbeTrusted`→`StatusTrusted`, `ProbeUntrusted`→`StatusUntrusted` (clean verdict, not error),
`ProbeError`→error.

`Bootstrap` (`setup.go:268-295`): the cancelled-dialog trap — `Verify` **before** any keychain
write (skip dialog if already trusted), then `InstallCA` (whose `security add-trusted-cert`
returns 0 even on cancel, so trust is never inferred from it), then **re-Verify** (`:287`);
`!res.OK()` → actionable error. `prober` field (`setup.go:63`) is the test seam.

`doctor` (`doctor/doctor.go:53-69`, `checkCA`): `CAGenerated()` (read-only, no mitmdump spawn)
→ if generated, `Verify(ctx)` → `StatusOK "trusted"` / `StatusProblem` (`res.Message()`) /
`StatusWarn` on env error.

### Existing tests & harness to reuse

- Unit (fake prober): `TestBootstrapUntrustedReturnsActionableError` (`verify_test.go:181-205`)
  sets `Outcome = ProbeUntrusted`, asserts `Bootstrap` error == `msgUntrusted`, cert
  re-installed, `beforeInstall` called once. Siblings cover trusted / probe-error / fresh-
  install (`Outcomes = [Untrusted, Trusted]`). Keep these as-is.
- `FakeTLSProber` (`sysdeptest/tlsprober.go`): `Outcome`, `Outcomes` (per-call queue), `Err`,
  `Calls`.
- **Live harness to reuse:** `internal/setup/setup_integration_test.go` (`//go:build integration`):
  `liveInstaller(t)` (`:33-49`) skips when `mitmdump`/`curl` absent, wires real `OS*` seams
  incl. `OSTLSProber{}`; `TestVerifyLive` (`:51-73`) runs `EnsureCA`+`Verify` (mitmdump spun
  up/torn down inside `Verify`). `TestBootstrapLive` is gated behind `CREANCE_LIVE_CA_INSTALL=1`.
- Untrusted-CA precedent (Python): `test_intercept_host_fails_without_mitm_ca`
  (`enforcer/test_integration.py:268-273`) — a fresh-confdir mitmdump presents a leaf signed
  by a CA not in the system store → curl without `--cacert` fails (exit 60). Same logic the Go
  test can use: spawn `mitmdump --set confdir=<tmpdir> --listen-port <port> -q`, call
  `OSTLSProber{}.ProbeViaProxy(...)`, assert `ProbeUntrusted` — **deterministic, no host-trust
  dependency**. The trusted branch depends on host trust (cover via the existing host-trust-
  dependent path, skip if untrusted).

### Planned shape

- Unit: extract the argv into a pure `curlProbeArgs(proxyURL, targetURL) []string` and
  table-assert the absence of `-k`/`--insecure`/`--cacert`/`--proxy-insecure` (satisfies the
  "asserted by test" AC without running curl — matches "pure logic → table test").
- Integration (`//go:build integration`, `internal/setup/` or `internal/sysdep/`): fresh-
  confdir mitmdump → `ProbeViaProxy` → assert `ProbeUntrusted`; trusted branch conditional.
  Run via `make test-integration`; document the real-mitmproxy/curl requirement.

### Key files (F10)

- `internal/sysdep/tlsprober.go`, `internal/sysdep/sysdeptest/tlsprober.go`.
- `internal/setup/setup.go` (`Verify` `:223-246`, `Bootstrap` `:268-295`),
  `internal/setup/verify_test.go`, `internal/setup/setup_integration_test.go`.
- `internal/doctor/doctor.go` (`checkCA` `:53-69`), `internal/cli/doctor.go`, `cli/cli.go`.
- `internal/proxy/enforcer/test_integration.py` (untrusted-CA curl precedent), `Makefile`
  (`test-integration` `:54-57`, `test-enforcer-integration` `:77-79`).

---

## Patterns to model the fix on

- **Atomic temp+rename via `sysdep.FileSystem`:** `policy/compile/compile.go:608-630`
  (`MkdirAll` → `WriteFile(dest+".tmp")` → `Rename(tmp, dest)` → `Remove(tmp)` on error);
  also `setup/skill.go:52-73` (read-compare-write variant), `cli/init.go:347-359`.
- **flock-guarded RMW via `sysdep.Flock`:** `proxy/lifecycle.go:129-142` (`MkdirAll` parent →
  `Acquire` → `defer Release` → read → mutate → write in place).
- **Integration test shape:** `//go:build integration` + blank line, `exec.LookPath` +
  `t.Skip` guard, real `OS*` seams, `t.TempDir()` + `t.Setenv("XDG_CACHE_HOME", …)`
  (`proxy/lifecycle_integration_test.go:1-49`).
- **Fakes with error injection:** `FakeFileSystem` (`WriteErrs`/`RenameErrs`,
  `setup/skill_test.go:39-51`, `registry/registry_test.go:63,161-166`), `FakeFlock`
  (real per-path mutex), `FakeTLSProber`.

## Open questions for the checkpoint

1. **F8 policy** (explicit ticket Open Question): confine-and-reject (allowed scope =
   declaring file's subtree + `~/.config/`) vs warn-and-allow with an opt-in. Recommended:
   confine-and-reject per the ticket's proposed default.
2. **F9 lock-file location:** out-of-tree (hashed/per-target under `~/.cache/agent-creance/`)
   vs sidecar `<target>.lock`. Recommended: out-of-tree, to keep the project tree clean and
   match the host-only-state convention.

(F10 has no open question — approach is determined by the audit above.)

## Related

- Review: `thoughts/shared/reviews/2026-06-22-codebase-quality-review.md` (F8 §131-137,
  F9 §139-143, F10 §145-151; F10 also in "Immediate Actions" §314-315). The review names no
  AC ticket; the F8/F9/F10 grouping into AC-0059 is the ticket author's.
- Ticket: `thoughts/shared/tickets/AC-0059-host-side-integrity.md`.
- Out of scope (per ticket): proxy lock-file concurrency hardening is AC-0061; v0.2 secret
  injection / `op://`.
