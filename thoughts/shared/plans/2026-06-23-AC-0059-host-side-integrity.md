---
date: 2026-06-23
ticket: AC-0059
topic: "Host-side integrity — confine includes (F8), atomic+locked config writes (F9), sound CA verification (F10)"
status: ready
research: thoughts/shared/research/2026-06-23-AC-0059-host-side-integrity.md
---

# Implementation Plan: AC-0059 — Host-side integrity (F8 / F9 / F10)

## Overview

Three grouped Important/Should-Fix host-side integrity findings from the 2026-06-22
security review:

- **F8** — confine `include:` path resolution to a defensible scope and reject escapes.
- **F9** — make `allow`/`deny`/`import` config writes atomic **and** serialized so a
  concurrent edit can never silently drop a rule.
- **F10** — prove the real CA-trust prober is sound (validates against the system trust
  store only) with a static argv assertion and a real-curl integration test.

Each finding is an independent phase, ordered F8 → F9 → F10, each separately committable
and verifiable.

## Decisions locked at the question checkpoint

1. **F8 policy: confine-and-reject.** Allowed include scope = the declaring file's
   directory subtree ∪ the global config dir (`~/.config/`). Absolute / `~/` / `..`
   escapes fail with a clear error. The implicit global include (`~/.config/agent-creance.yaml`)
   stays allowed because it is injected as a root path, not through `resolveIncludePath`.
2. **F9 lock location: out-of-tree.** A `config-locks/` sibling under
   `~/.cache/agent-creance/`, with one lock file per target keyed by a hash of the
   target's absolute path. This serializes correctly across projects (essential for
   `--global`, which is shared) and keeps the project tree clean.

## Current state (from research)

- **F8:** `internal/config/load.go:285-294` `resolveIncludePath` returns a plain `string`
  and resolves absolute/`~`/`..` verbatim with no scope check. It is the single chokepoint
  for `resolve` (`:193-249`), `collectFiles` (`:138-187`), and `ValidateInclude`
  (`:258-280`). The global include is injected directly at `load.go:55,82,118` (not via the
  chokepoint). Out-of-scope rejection at path-resolution time happens before `ReadFile`/`Parse`,
  so no file content can leak into the YAML parse error (`errors.go:67-68`) for out-of-scope
  reads. Test to flip: `TestResolveFiles_AbsoluteAndHomeIncludes` (`load_test.go:345-359`).
- **F9:** `internal/cli/mutate.go:97-128` `applyAndRecompile` and the inlined `import.go:45-103`
  read once, append (`config.AppendRule`, already robust + re-validating), then atomic
  temp+rename via `writeFileAtomic` (`cli/init.go:347-359`) — **no lock, no fresh re-read**.
  The `sysdep.Flock` seam (`flock.go`) and its fake (real per-path mutex) already exist;
  `App.Flock` is already wired (`cli.go:54,153`). flock and temp+rename are mutually
  exclusive on the *same* inode, so a **separate** lock file is required.
- **F10:** `internal/sysdep/tlsprober.go:73-95` `ProbeViaProxy` builds the curl argv inline
  with **no** `-k`/`--insecure`/`--cacert`/`--proxy-insecure` (sound on audit); outcome by
  exit code (0→`ProbeTrusted`, 60→`ProbeUntrusted`). Setup/doctor mapping is unit-tested with
  the fake prober. Reusable live harness: `setup_integration_test.go` `liveInstaller`.

## Desired end state

`include:` escapes are rejected with a clear, content-free error and documented in
`docs/design.md`; in-scope includes and the implicit global are unchanged. Concurrent
`allow`/`deny`/`import` runs each land (no lost rule, incl. `deny_always`) via a per-target
lock around a fresh-read → append → atomic-write critical section. `ProbeViaProxy`'s
soundness is asserted by a unit test on its argv and by a real-curl integration test where
an untrusted CA yields `ProbeUntrusted`.

---

## Phase 1 — F8: confine `include:` resolution

### Changes

**`internal/config/errors.go`** — add a sentinel alongside `ErrIncludeCycle`/`ErrMaxIncludeDepth`:

```go
// ErrIncludeOutOfScope is returned when an include: path resolves outside the
// allowed scope: the declaring file's directory subtree or the global config dir
// (~/.config). It blocks a cloned repo's config from reading arbitrary user files.
var ErrIncludeOutOfScope = errors.New("config: include path out of scope")
```

**`internal/config/load.go`** — change `resolveIncludePath` to `(string, error)` and enforce
the scope. Compose the path as today, then accept only if it is within the declaring file's
directory or within the global config dir; otherwise return `ErrIncludeOutOfScope` wrapped
with the include text and resolved path (a path, **not** file content):

```go
func (l *Loader) resolveIncludePath(declaringAbs, home, inc string) (string, error) {
	var resolved string
	switch {
	case strings.HasPrefix(inc, "~/"):
		resolved = filepath.Join(home, inc[len("~/"):])
	case filepath.IsAbs(inc):
		resolved = inc
	default:
		resolved = filepath.Join(filepath.Dir(declaringAbs), inc)
	}
	declaringDir := filepath.Dir(declaringAbs)
	globalDir := filepath.Join(home, ".config")
	if pathWithin(declaringDir, resolved) || pathWithin(globalDir, resolved) {
		return resolved, nil
	}
	return "", fmt.Errorf("%w: %q resolves to %q (allowed: under %s or %s)",
		ErrIncludeOutOfScope, inc, resolved, declaringDir, globalDir)
}

// pathWithin reports whether target is dir itself or lies below it.
func pathWithin(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
```

Update the three callers to propagate the error:
- `resolve` (`load.go:234-243`): `target, err := l.resolveIncludePath(abs, home, inc); if err != nil { return nil, err }` before recursing.
- `collectFiles` (`load.go:179-185`): same, returning the error.
- `ValidateInclude` (`load.go:267`): same — so the `include` command rejects out-of-scope at validation time.

The global-dir allowance (`~/.config/`) keeps the global config's own includes working and
lets a project file deliberately include a file under `~/.config/`. The implicit global file
is injected as a root path (`load.go:55,82,118`) and never passes through `resolveIncludePath`,
so it is unaffected.

**`docs/design.md`** — in "The configuration" (`include:` semantics), document the policy:
includes are confined to the declaring file's directory subtree plus the global config dir
(`~/.config/`); absolute, `~/`, and `..`-escaping includes are rejected; this prevents a
cloned repo's config from reading arbitrary user-readable files.

### Tests (`internal/config/load_test.go`, white-box, standalone funcs)

- **Change** `TestResolveFiles_AbsoluteAndHomeIncludes` → assert an absolute include
  (`/etc/ac/abs.yaml`) and a `~/frag.yaml` outside `~/.config` are now rejected with
  `errors.Is(err, ErrIncludeOutOfScope)`.
- Add `TestLoad_IncludeRelativeInScopeStillWorks` — `team.yaml` under the project dir loads.
- Add `TestLoad_IncludeParentEscapeRejected` — `../x.yaml` → `ErrIncludeOutOfScope`.
- Add `TestLoad_IncludeAbsoluteEscapeRejected` and `TestLoad_IncludeHomeEscapeRejected`.
- Add `TestLoad_IncludeIntoGlobalConfigDirAllowed` — project file includes
  `~/.config/agent-creance-shared.yaml`; loads (global-dir allowance).
- Add `TestLoad_ImplicitGlobalUnaffected` — the implicit `~/.config/agent-creance.yaml`
  (and a relative include *it* declares, resolving under `~/.config/`) still load.
- Add `TestLoad_OutOfScopeIncludeErrorHasNoFileContents` — seed an out-of-scope target with
  recognizable content; assert the error is `ErrIncludeOutOfScope` and the message does not
  contain that content (rejected before read).
- During implementation, grep the config test suite for other cases relying on absolute/`~`
  includes (e.g. in `merge`/`validate` tests) and reconcile them.

### Success criteria

- [ ] `make test` green (config package incl. new/changed cases).
- [ ] `make lint` green.
- [ ] `go build ./...` clean.
- [ ] Manual: a `.agent-creance.yaml` with `include: /etc/passwd`, `include: ~/.ssh/x`, or
      `include: ../../x.yaml` is rejected; an in-scope relative include and the implicit
      global still load.

---

## Phase 2 — F9: atomic + locked config writes

### Changes

**`internal/state/state.go`** — add a cross-project locks dir + a per-target lock-path helper
(modeled on `RegistriesRoot`/`GeneratorsRoot`/`EnforcerRoot`):

```go
const configLocksSubdir = "config-locks"

// ConfigLocksRoot returns <cache>/agent-creance/config-locks — the cross-project
// home of advisory locks serializing config mutations (allow/deny/import). It is a
// sibling of projects/<hash>/ and keyed by target file, not by project, so two
// projects editing the shared global config contend on the same lock.
func (r *Resolver) ConfigLocksRoot() (string, error) {
	cache, err := r.cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, appCacheSubdir, configLocksSubdir), nil
}

// ConfigLock returns the advisory lock path for a config target. The target's
// absolute path is hashed so the same file always maps to the same lock regardless
// of the caller's working directory.
func (r *Resolver) ConfigLock(target string) (string, error) {
	abs, err := r.paths.Abs(target)
	if err != nil {
		return "", fmt.Errorf("state: absolute path for %q: %w", target, err)
	}
	root, err := r.ConfigLocksRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, hashPath(abs)+".lock"), nil
}
```

(Reuses the existing `hashPath`; the target need not exist yet, so `Abs` is used rather than
`EvalSymlinks`. Add a `state_test.go` case for `ConfigLock`/`ConfigLocksRoot` path shape.)

**`internal/cli/mutate.go`** — add a `withConfigLock` helper and wrap the critical section.
The fresh `ReadFile` then happens **inside** the lock, giving re-read-under-lock + the
existing `AppendRule` re-validation against those fresh bytes:

```go
// withConfigLock serializes a config read-modify-write against concurrent
// allow/deny/import runs (and the user's editor) by holding an exclusive advisory
// lock on a per-target lock file in the out-of-tree cache. The data file itself is
// still written via atomic temp+rename inside fn; the lock is a separate inode
// because flock and rename are mutually exclusive on the same file.
func withConfigLock(app *App, target string, fn func() error) error {
	lockPath, err := state.New(app.Paths).ConfigLock(target)
	if err != nil {
		return err
	}
	if err := app.FS.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(lockPath), err)
	}
	lf, err := app.Flock.Acquire(lockPath)
	if err != nil {
		return fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer func() { _ = lf.Release() }()
	return fn()
}
```

Wrap the body of `applyAndRecompile` (the `ReadFile`→`apply`→`writeFileAtomic`→`recompile`
sequence, `mutate.go:98-127`) in `withConfigLock(app, path, func() error { ... })`.

**`internal/cli/import.go`** — wrap `runImport`'s read-of-dest → merge → confirm → write →
recompile (`import.go:56-101`) in the same `withConfigLock(app, dest, ...)`. The interactive
confirmation runs inside the lock; acceptable because import is an interactive, reviewed
action and concurrent import is rare — holding the lock keeps the previewed merge consistent
with what is written. (The fragment read at `import.go:46` stays outside the lock.)

`App.Flock` is already wired in production (`cli.go:153`); no new App plumbing.

### Tests

- **`internal/cli`** (testscript or table, using `FakeFlock` + `FakeFileSystem`):
  - A "two interleaved edits both land" test: simulate writer A reading the base, then writer
    B completing a full locked edit, then A completing — assert both rules survive, especially
    a `deny_always` (the dangerous direction). With `FakeFlock`'s real per-path mutex this can
    be a `-race` goroutine test, or a sequential simulation asserting the lock is acquired
    before the read.
  - Assert `Acquire`/`Release` ordering via `FakeFlock.Acquired`/`Released` for allow, deny,
    and import (lock taken before `ReadFile`, released after `Rename`).
  - Lock-acquire failure (`FakeFlock.AcquireErr`) aborts before any write (no temp file left,
    no recompile).
  - Existing allow/deny/import behavior tests still pass (wire `app.Flock` in their fakes if
    not already).
- **`internal/state`**: `ConfigLock` returns `<cache>/agent-creance/config-locks/<hash>.lock`;
  same target → same lock; different targets → different locks; honors `XDG_CACHE_HOME`.

### Success criteria

- [ ] `make test` green (incl. the interleaved-edit / `-race` test and state path test).
- [ ] `make lint` green.
- [ ] `go build ./...` clean.
- [ ] Manual: two back-to-back `agent-creance deny` runs both persist (no lost `deny_always`);
      a `.tmp` is never left behind on success.

---

## Phase 3 — F10: prove CA-verification soundness

### Changes

**`internal/sysdep/tlsprober.go`** — extract the argv into a pure function so it is unit-
testable without running curl; `ProbeViaProxy` calls it:

```go
// curlProbeArgs builds the curl argv for the trust probe. It deliberately passes
// no -k/--insecure, no --cacert, and no --proxy-insecure: trust must come from the
// system store or the probe is meaningless (the re-signed leaf is validated against
// the OS trust anchors only).
func curlProbeArgs(proxyURL, targetURL string) []string {
	return []string{
		"-sS", "-o", "/dev/null",
		"--proxy", proxyURL,
		"--retry", "5", "--retry-connrefused", "--retry-delay", "1",
		targetURL,
	}
}
```

`ProbeViaProxy` becomes `exec.CommandContext(ctx, "curl", curlProbeArgs(proxyURL, targetURL)...)`.

**`internal/sysdep/tlsprober_test.go`** (new or existing unit, package `sysdep`) — table test
asserting `curlProbeArgs`:
- contains none of `-k`, `--insecure`, `--cacert`, `--proxy-insecure`;
- contains `--proxy` immediately followed by the proxy URL, and the target URL last.

**`internal/sysdep/tlsprober_integration_test.go`** (new, `//go:build integration`,
package `sysdep`) — real-curl proof:
- Skip if `mitmdump` or `curl` is absent (`exec.LookPath`) or egress is unavailable.
- **Untrusted (deterministic):** spawn `mitmdump --set confdir=<t.TempDir()>
  --listen-host 127.0.0.1 --listen-port <freePort> -q`. A fresh confdir generates a CA the
  system does **not** trust. Wait until the listen port is open and `<confdir>/mitmproxy-ca-cert.pem`
  exists. Call `OSTLSProber{}.ProbeViaProxy(ctx, "http://127.0.0.1:<port>", "https://example.com")`;
  assert `ProbeUntrusted`. SIGTERM teardown. (This mirrors the Python precedent
  `test_intercept_host_fails_without_mitm_ca`.)
- **Trusted (conditional):** spawn mitmdump with the host's default confdir (`~/.mitmproxy`);
  if the host already trusts that CA, assert `ProbeTrusted`; otherwise `t.Skip`. (The existing
  `setup` `TestVerifyLive` already covers the trusted path end-to-end; this branch is the
  symmetric assertion.)
- Add a small `freePort` helper (model `internal/verify/verification_integration_test.go`).

**`docs/design.md`** (optional, "Post-install CA verification") — one line noting the live
probe validates only against the system trust store (no `-k`/`--cacert`/`--proxy-insecure`),
which is why a cancelled keychain dialog is caught.

No setup/doctor code changes: the audit confirms the prober is already sound, and the
existing fake-prober unit tests already assert the setup/doctor untrusted-mapping end-to-end.

### Success criteria

- [ ] `make test` green (incl. the `curlProbeArgs` table test).
- [ ] `make lint` green; `go build ./...` clean.
- [ ] `make test-integration` runs the new probe integration test on a machine with real
      mitmproxy/curl: untrusted CA → `ProbeUntrusted` (trusted branch passes or skips).
      Document that this path needs real mitmproxy/curl.

---

## Final verification (whole ticket)

- [ ] `make test` green.
- [ ] `make lint` green.
- [ ] `go build ./...` clean.
- [ ] `make test-integration` exercised for the F10 path (document the real-tool requirement).
- [ ] `make build` so `bin/agent-creance` reflects the final commit.
- [ ] All AC-0059 acceptance criteria (F8/F9/F10) satisfied.
- [ ] `docs/design.md` updated for the F8 include-confinement policy (and the optional F10 note).
- [ ] Ticket status → Done; dated note under `## Notes & Updates`.

## Notes / risks

- **F8 scope interpretation:** "declaring file's subtree" is enforced per-file (each include
  must stay within the directory of the file that declares it). A nested include therefore
  cannot reach back above its own directory. This is the literal reading of the ticket and
  the most defensive; document it so the constraint is discoverable.
- **F9 import lock during prompt:** the interactive confirmation is held under the lock by
  design (keeps the previewed merge consistent with the write); acceptable for an interactive,
  rarely-concurrent command.
- **F10 trusted branch** is host-dependent and may skip in CI; the untrusted assertion is the
  load-bearing, deterministic one the review asked for.
