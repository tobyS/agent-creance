---
date: 2026-06-09
ticket: AC-0035
title: "Mount the redirected CLAUDE_CONFIG_DIR read-write into the cage"
status: ready
branch: main
research: thoughts/shared/research/2026-06-09-AC-0035-incage-claude-config-dir-writable.md
tags: [plan, cage, safehouse, config-dir, AC-0035]
---

# AC-0035 — Make CLAUDE_CONFIG_DIR writable inside the cage

## Overview

The cage redirects the agent's config to an ephemeral
`<cache>/agent-creance/projects/<hash>/claude` dir (the `CLAUDE_CONFIG_DIR` env
var) and seeds it from the uncaged side, but it **never mounts that dir into the
cage**. In-cage writability therefore depends on the dir living under a path
safehouse's base policy already grants RW (`/tmp`, `$TMPDIR`, toolchain dirs). At
the real default `~/.cache/agent-creance` none of those apply, so the caged
agent's config writes are denied — breaking the documented config-persistence
behavior. (design.md:435 already *claims* the dir is "mounted read-write"; this
brings the implementation in line with that.)

**Fix (decided at checkpoint):** mount **exactly** the config dir RW via
safehouse's `--add-dirs` — the idiomatic "extra RW path" mechanism. Narrowest
possible (only `claude/`, not the state `Root`, not a blanket `~/.cache`), keeps
the dir distinct from `~/.claude`, and avoids the firmlink/symlink-literal
fragility of the SBPL-fragment route. Re-guard with the AC-0033 battery by moving
its cache to a *non-granted* location so the `doc-config-dir` vector reproduces the
failure (RED) then proves the fix (GREEN).

## Current state

- `internal/cage/cage.go:80-90` (`Build`): RW mounts come **only** from
  `sh.AddDirsRW`; `--add-dirs` is emitted only when that list is non-empty.
  `ClaudeConfigDir()` / `Layout.Root` are added to neither mount flag.
- `internal/cage/cage.go:172-183` (`Prepare`): creates the config dir (`0o700`) and
  seeds `settings.json` (`{}`) — runs before `Build` references it
  (`Resolve`→`Prepare`→`Build`).
- `internal/cage/cage.go:239-240` (`buildEnv`): sets `CLAUDE_CONFIG_DIR =
  in.Layout.ClaudeConfigDir()`.
- `internal/verify/verification_integration_test.go:138-145` (`runBattery`): sets
  `XDG_CACHE_HOME = t.TempDir()` (under `$TMPDIR`, which safehouse grants) — masks
  the gap.
- `internal/verify/testdata/fake-agent.sh:221-229`: `doc-config-dir` probe writes
  `$CLAUDE_CONFIG_DIR/hooks/creance-escape.json`, emits `planted` / `blocked`.
- `internal/cage/cage_test.go:66-89` (`TestExpandPathViaArgs`): asserts the **exact**
  `--add-dirs` value — will need updating when the config dir is appended.
- `internal/cage/testdata/invocation.golden.json`: pins the argv incl. `--add-dirs`.

## Desired end state

- `cage.Build` always emits `--add-dirs` including `in.Layout.ClaudeConfigDir()`
  (RW), regardless of whether the user configured `add_dirs_rw`.
- A caged run with the cache at a **non-`$TMPDIR`, non-granted** location can create
  dirs and write files under `CLAUDE_CONFIG_DIR`.
- The mounted dir is still `<root>/claude`, distinct from `~/.claude`; no broader
  path (state root, `~/.cache`, egress log, lock) becomes in-cage-writable.
- The AC-0033 battery's `doc-config-dir` vector exercises the real-location
  writability and is GREEN; the negative control still detects the raw-egress
  escape.
- Docs (cage-verification.md known-limitation #2) updated; ticket closed.

## What we are NOT doing

- Not mounting the project state `Root` or a generic `~/.cache` (narrowest only).
- Not changing what is seeded into the config dir (still `{}` settings).
- Not relocating the out-of-tree state root (out of scope per ticket / CLAUDE.md).
- Not touching the SBPL `--append-profile` fragments or AC-0034's CA handling.

---

## Phase 1 — Reproduce the gap in the battery (RED, WP)

Make the AC-0033 battery exercise a non-granted cache location so `doc-config-dir`
fails for the *right* reason (config dir not mounted), mirroring AC-0034's RED step.

### Changes

**`internal/verify/verification_integration_test.go`** — in `runBattery`
(currently lines 138-139), replace the `$TMPDIR`-backed cache with a temp dir under
`$HOME` (outside safehouse's base RW grants), cleaned up after the test:

```go
// Real-location guard (AC-0035): place the cache under $HOME — NOT $TMPDIR — so it
// falls outside safehouse's base RW grants (/tmp, $TMPDIR, toolchain dirs), matching
// the production ~/.cache/agent-creance. This exercises whether the cage actually
// mounts CLAUDE_CONFIG_DIR RW (the doc-config-dir vector). A t.TempDir() cache lives
// under $TMPDIR, which safehouse grants, and would mask the gap.
home, err := os.UserHomeDir()
require.NoError(t, err)
cacheDir, err := os.MkdirTemp(home, ".agent-creance-battery-")
require.NoError(t, err)
t.Cleanup(func() { _ = os.RemoveAll(cacheDir) })
t.Setenv("XDG_CACHE_HOME", cacheDir)
```

(The subsequent `layout, err := state.New(paths).Resolve(proj)` reuses `err`.)

### Why this is safe for the other vectors

The only in-cage write to the cache is the `doc-config-dir` plant. All other state
files (`network.sb`, `proxy.sb`, `ca.sb`, `policy.json`, `proxy.lock`,
`egress.jsonl`, enforcer) are written/read by the **uncaged** side, which can access
`$HOME` freely. So relocating the cache turns only `doc-config-dir` RED and leaves
the other 17 vectors and the negative control unaffected.

### Success criteria

#### Automated

- [ ] `make test` (fast) stays green (battery is integration-tagged, not run here).
- [ ] `make test-integration` now FAILS on `TestCageVerificationBattery`:
      `doc-config-dir` observes `blocked` (Failed) and the host-side
      `assert.FileExists` on the plant fails. *(If the host cannot nest a sandbox
      the battery `t.Skip`s — note this in the commit if so.)*
- [ ] `go build ./...` passes.

#### Manual

- [ ] Confirm the RED failure message names `doc-config-dir` (not an unrelated
      vector), proving the reproduction targets the config-dir gap.

### Commit

`test(AC-0035): reproduce in-cage config-dir write failure at real cache loc (RED, WP)`

---

## Phase 2 — Mount CLAUDE_CONFIG_DIR read-write (GREEN)

### Changes

**`internal/cage/cage.go`** — in `Build`, replace the conditional RW-mount block
(lines 80-86, the `if len(sh.AddDirsRW) > 0 { … }`) so the config dir is always
appended and `--add-dirs` is always emitted:

```go
// CLAUDE_CONFIG_DIR is always mounted read-write so the caged agent can persist its
// own ephemeral config/session state (AC-0035) even when the cache lives outside
// safehouse's base RW grants (the real ~/.cache/agent-creance). The real ~/.claude
// is never added. The dir is created/seeded by Prepare before Build runs.
rw := append(append([]string{}, sh.AddDirsRW...), in.Layout.ClaudeConfigDir())
args = append(args, "--add-dirs", expandColonList(rw, in))
```

Notes:
- `ClaudeConfigDir()` is absolute & clean, so `expandPath` passes it through
  unchanged (no firmlink resolution needed — safehouse canonicalizes mount paths
  itself, consistent with how existing `AddDirsRW` entries are handled).
- The RO block (`--add-dirs-ro`) is unchanged.

**`internal/cage/cage_test.go`**:

1. Update `TestExpandPathViaArgs` (lines 66-89): the expected `--add-dirs` value now
   has the config dir appended. Change the assertion to:
   ```go
   want := tc.want + ":" + in.Layout.ClaudeConfigDir()
   require.Equal(t, want, argValue(t, inv.Args, "--add-dirs"))
   ```
2. Add `TestBuildAlwaysMountsConfigDir` covering the "always emit" behavior the
   golden (non-empty `AddDirsRW`) doesn't:
   ```go
   func TestBuildAlwaysMountsConfigDir(t *testing.T) {
       t.Run("empty AddDirsRW still mounts config dir", func(t *testing.T) {
           in := fixtureInputs()
           in.Config.Safehouse.AddDirsRW = nil
           inv, err := cage.Build(in)
           require.NoError(t, err)
           require.Equal(t, in.Layout.ClaudeConfigDir(), argValue(t, inv.Args, "--add-dirs"))
       })
       t.Run("config dir mounted alongside user dirs", func(t *testing.T) {
           inv, err := cage.Build(fixtureInputs())
           require.NoError(t, err)
           require.Contains(t, argValue(t, inv.Args, "--add-dirs"), in.Layout.ClaudeConfigDir())
       })
   }
   ```
   (Second subtest reads `in` via a fresh `fixtureInputs()`; adjust to capture it.)

   `TestBuildNeverMountsRealClaude` (lines 91-109) needs no change — the config dir
   is not `~/.claude` and the test already tolerates extra `--add-dirs` content.

**`internal/cage/testdata/invocation.golden.json`** — regenerate via `make golden`;
the `--add-dirs` value becomes
`/proj:/home/test/.cache/agent-creance/projects/0123456789abcdef/claude`. Review the
diff: the ONLY change must be that one appended segment.

### Success criteria

#### Automated

- [ ] `make test` (fast) green, including the updated/new cage tests.
- [ ] `make golden` produces only the single `--add-dirs` segment change; `make test`
      green afterward.
- [ ] `go build ./...` and `make lint` pass.
- [ ] `make test-integration`: `TestCageVerificationBattery` GREEN — `doc-config-dir`
      observes `planted` and the plant exists under `ClaudeConfigDir()`; all vectors
      PASS; `TestCageVerificationNegativeControl` still detects the escape. Run with
      `-count=2` for stability if the host supports it. *(Note any `t.Skip`.)*

#### Manual

- [ ] `git diff internal/cage/testdata/invocation.golden.json` shows only the
      config-dir mount addition.
- [ ] Reason-check the security property: the mounted path is `<root>/claude`, still
      not `~/.claude`; `fs-real-claude` vector unaffected.

### Commit

`feat(AC-0035): mount CLAUDE_CONFIG_DIR read-write in-cage (GREEN)`

---

## Phase 3 — Docs & close

### Changes

**`docs/cage-verification.md`** (known-limitation #2, ~lines 124-131): rewrite from
a "confirm on your host" caveat to a resolved statement, mirroring how AC-0034
rewrote item #1 — the redirected `CLAUDE_CONFIG_DIR` is now mounted RW via
`--add-dirs` (not reliant on `$TMPDIR`), guarded by the `doc-config-dir` vector
which exercises a non-granted cache location; the dir remains distinct from
`~/.claude`.

**`docs/design.md`** (optional, light): design.md:435 already says the config dir is
"mounted read-write", which is now true. Add a short parenthetical that the mount is
an explicit `--add-dirs` of exactly `…/claude` (AC-0035), so the next reader sees the
mechanism. Do not contradict design.md:292 ("none of these files are mounted") — that
sentence is about policy/enforcer/lock/audit-log, not the config dir.

**`thoughts/shared/tickets/AC-0035-incage-claude-config-dir-writable.md`**: tick all
acceptance criteria, set `Status: Done`, add a dated note referencing the RED/GREEN
commit SHAs, and answer the four research questions (mechanism = `--add-dirs`; only
`claude/` mounted; no interaction with AC-0034 since the CA lives in `~/.mitmproxy`).

### Success criteria

#### Automated

- [ ] `make test` green (docs-only changes; no code touched in this phase).

#### Manual

- [ ] All five ticket acceptance criteria are satisfied and ticked.
- [ ] Known-limitation #2 no longer reads as an open caveat.

### Commit

`docs(AC-0035): close ticket — config dir mounted RW in-cage & guarded`

---

## Testing strategy

- **Unit (fast, `make test`):** `cage_test.go` golden + `TestExpandPathViaArgs`
  (updated) + `TestBuildAlwaysMountsConfigDir` (new) prove the `--add-dirs`
  construction deterministically, including the empty-`AddDirsRW` path the golden
  doesn't exercise.
- **Integration (`make test-integration`):** the AC-0033 battery is the
  real-location guard — RED in Phase 1 (non-granted cache, config dir unmounted),
  GREEN in Phase 2 (config dir mounted). This is the "equivalent guard" the ticket
  asks for, without writing to the user's actual `~/.cache`.
- **Honesty note:** if the host cannot nest a sandbox policy, the battery `t.Skip`s;
  in that case the fast unit tests + golden + the static argv guard remain the proof
  of the mount, and the RED/GREEN claim is recorded as skipped, not asserted.

## Rollback

Single-line revert of the `Build` change restores prior behavior; the battery cache
relocation is independent and can stand or revert on its own.
