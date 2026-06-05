---
date: 2026-06-05
ticket: AC-0009
title: "Research: sysdep seam extensions (WP-1.4)"
status: complete
tags: [research, sysdep, testability, WP-1.4]
git_commit: a1169ec
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0009 — sysdep seam extensions (WP-1.4)

## Research question

What is required to grow `internal/sysdep` with the OS-abstraction seams later
phases need — `Clock`, `FileSystem` (write/stat/mkdir/remove/rename),
`Keychain`, `Flock`, and `ProcessGroup`/signals — each with a fake in
`internal/sysdep/sysdeptest`, following the existing `Commander` pattern, so that
later tickets stay hermetic? What are the exact conventions to mirror, what
already exists, and what design decisions are open?

## Summary

- `internal/sysdep` today holds **three** seams — `Commander`, `PathResolver`,
  `FileSystem` — each an identical shape: a tiny interface, a zero-field
  `OS*`/`Exec*` production struct with a `var _ Iface = (*Impl)(nil)`
  compile-time assertion, value-receiver one-line stdlib delegations, and a
  scripted map-based fake in `sysdeptest/`. This is the template AC-0009 copies.
- The **`FileSystem` seam already exists** (added in WP-1.3, commit `4a1b0fa`)
  with only `ReadFile`. Its own doc comment hands the growth to this ticket:
  *"Later phases (AC-0009) grow it with the write/stat/mkdir/remove/rename
  methods their consumers require."* So AC-0009 **extends** `FileSystem` in
  place rather than creating it.
- The remaining four named seams — `Clock`, `Keychain`, `Flock`,
  `ProcessGroup` — **do not exist anywhere** in the tree yet.
- WP-1.4's deliverable is explicitly **interfaces + fakes only**; real impls
  land with their consumers. The ticket adds that "real impls may be stubs
  returning `ErrNotImplemented` until their consumer ticket."
- There is **no `ErrNotImplemented` sentinel**, **no call-recording fakes**, and
  **no dedicated fake smoke-tests** in the repo today. Real impls are
  smoke-tested in-package against `t.TempDir()`/`t.Setenv`. Each of these is a
  pattern AC-0009 may need to *introduce* — see Open Questions.
- The codebase convention is decisively **narrow, single-purpose, grow-as-needed
  interfaces** (PathResolver "locates" vs FileSystem "reads" is a deliberate
  concern split). This answers the ticket's "fat vs narrow FileSystem" and
  "where does Flock belong" questions in favour of: grow `FileSystem` in place,
  give `Flock` its own seam.

## The canonical pattern (what every new seam must match)

Reference file: `internal/sysdep/commander.go`. The three existing seams agree on:

**File layout / naming**
- One interface per file, file named after the concept (lowercased, no
  separators): `commander.go`, `pathresolver.go`, `filesystem.go`.
- The package doc lives only on `commander.go` (`internal/sysdep/commander.go:1-13`);
  new interface files do **not** repeat it.
- Fakes mirror the per-concept split in `sysdeptest/` (`pathresolver.go`,
  `filesystem.go`), **except** `FakeCommander` lives in `fake.go`, which carries
  the `sysdeptest` package doc (`internal/sysdep/sysdeptest/fake.go:1-12`).
- Real-impl smoke tests live in `internal/sysdep/<concept>_test.go`,
  `package sysdep` (white-box), exercising the real struct against a real temp
  dir/env. (`Commander` is the one seam with no smoke test.)

**Interface** (`internal/sysdep/commander.go:20-31`, `filesystem.go:15-20`)
- A short doc paragraph stating the seam's narrow scope and how it differs from
  neighbouring seams, plus the "Why route through the seam (for someone coming
  from PHP/TS)… production wires `OS*`, tests wire the fake in sysdeptest"
  paragraph.
- Each method's doc names the stdlib function it mirrors and states the error
  contract (e.g. *"a non-existent file yields an error satisfying
  `errors.Is(err, fs.ErrNotExist)`"*, `filesystem.go:16-19`).

**Real impl** (`internal/sysdep/commander.go:39-51`, `filesystem.go:27-33`)
```go
type OSFileSystem struct{}                 // zero-field struct, OS*/Exec* prefix

var _ FileSystem = (*OSFileSystem)(nil)    // compile-time assertion, right below

func (OSFileSystem) ReadFile(name string) ([]byte, error) {  // value receiver
	return os.ReadFile(name)               // one-line stdlib delegation
}
```

**Fake** (`internal/sysdep/sysdeptest/filesystem.go:10-34`, `fake.go:18-63`)
```go
type FakeFileSystem struct {
	Files map[string][]byte   // scripted return values, keyed by the identifier
	Errs  map[string]error    // per-key error knob, checked FIRST
}
func NewFakeFileSystem() *FakeFileSystem { /* init every map to empty (never nil) */ }
func (f *FakeFileSystem) ReadFile(name string) ([]byte, error) {  // POINTER receiver
	if err, ok := f.Errs[name]; ok { return nil, err }   // Errs first
	if b, ok := f.Files[name]; ok { return b, nil }
	return nil, fs.ErrNotExist                            // default: stdlib sentinel
}
```
- Exported map fields so tests populate them directly; pointer receivers.
- A parallel `Errs`-style error knob checked before the scripted value. The
  `FakePathResolver` variant uses **per-method scalar error fields** (`HomeErr`,
  `AbsErr`, `EvalErr`) plus a default for absent keys
  (`internal/sysdep/sysdeptest/pathresolver.go:19-27`) — the model to copy for
  write/stat/etc. failure simulation.
- Optional fluent `WithX` builder (only `FakeCommander.WithTool` has one).
- No call-recording slices anywhere; "records nothing it isn't told about."

**Smoke test** (`internal/sysdep/filesystem_test.go:15-39`,
`pathresolver_test.go`) — construct the zero-value real struct, exercise against
`t.TempDir()`/`t.Setenv`/`os.Symlink`, assert real behaviour and `errors.Is` for
sentinel cases. A leading comment notes the real impl is verified once here while
logic packages use the fake.

**Consumer wiring** — consumers take the interface as a constructor arg and store
it (`internal/config/load.go:34-39` `NewLoader(fsys sysdep.FileSystem, paths
sysdep.PathResolver)`); the composition root injects the real impl
(`internal/cli/cli.go:20-26,55-61`); tests inject the fake
(`internal/config/load_test.go`, `internal/state/state_test.go`). Per AC-0009
"Out of Scope," **do not wire unused deps into `cli.App`** — wiring lands when a
consumer arrives.

## Current state of each seam

| Seam | Interface | Real impl + assertion | Fake | Smoke test |
|---|---|---|---|---|
| `Commander` | ✅ `commander.go` | ✅ `ExecCommander` | ✅ `FakeCommander` | — (none) |
| `PathResolver` | ✅ `pathresolver.go` | ✅ `OSPathResolver` | ✅ `FakePathResolver` | ✅ |
| `FileSystem` | ⚠️ `ReadFile` only | ⚠️ `ReadFile` only | ⚠️ `ReadFile` only | ⚠️ `ReadFile` only |
| `Clock` | ❌ | ❌ | ❌ | ❌ |
| `Keychain` | ❌ | ❌ | ❌ | ❌ |
| `Flock` | ❌ | ❌ | ❌ | ❌ |
| `ProcessGroup` | ❌ | ❌ | ❌ | ❌ |

## Design rationale per seam (informs method shapes)

From `docs/design.md` and the v0.1 spec
(`thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`):

- **Clock** — for the 30-day per-package registry-cache expiry (design
  "Allowlist generators / Caching") and audit-log timestamps. Consumers: WP-2.2
  (cache refresh), WP-3.2 (audit writer). Minimal shape: `Now() time.Time`.
- **FileSystem (writes)** — out-of-tree state writers need write/stat/mkdir/
  remove/rename: `policy.json`, `network.sb`, the `proxy.lock` file, the audit
  log, the extracted `enforcer.py`, the ephemeral `CLAUDE_CONFIG_DIR`. Broad
  consumer set (WP-1.1, 2.4, 2.5, 3.2, 3.3, 3.5, 6.x). The interface's own doc
  already names the methods to add.
- **Keychain** — for the Anthropic OAuth credential, the login-Keychain
  generic-password item `Claude Code-credentials` (account = login short name;
  spike S2). v0.1's host-side job is **detection**: is the item present, and is
  the keychain **locked** (the one non-interactive `doctor` failure)? Refresh
  happens inside the cage, not host-side. Consumer: WP-4.1 (`internal/cred`,
  gated on S2). Shape implied: read a generic-password item by service+account,
  distinguishing "absent" vs "present" vs "keychain locked."
- **Flock** — for atomic read-modify-write of `proxy.lock` (proxy PID, port,
  policy hash, attached-agent PIDs); reacquired on teardown to decrement and
  kill the proxy when the agents array empties. Backed by
  `golang.org/x/sys/unix.Flock` on an open fd (not `syscall.Flock`). `doctor`
  also warns on filesystems with unreliable flock (iCloud Drive, SMB). Consumer:
  WP-3.4 (proxy lifecycle). Shape implied: acquire (exclusive) and release on a
  held descriptor — likely open+lock returning an unlock closure.
- **ProcessGroup / signals** — for deterministic Ctrl-C teardown of the agent's
  whole subtree: start the child in a new process group (`Setpgid: true` /
  `setsid`), forward `SIGINT`/`SIGTERM` to the group via `kill(-pgid, sig)`,
  wait for the whole group before the lock-file decrement. Consumer: WP-4.3
  (`internal/cage`). Shape implied: start-with-new-pgid, signal-group,
  wait-group, plus OS-signal notification (`os/signal.Notify` equivalent). This
  is the **least consumer-determined** of the five.

## What WP-1.4 / C2 prescribe

- **WP-1.4** (spec): "Interfaces + fakes for the OS touchpoints later phases
  need: `Clock`, `FileSystem`, `Keychain`, `Flock`, `ProcessGroup`/signals. Ship
  the interfaces and fakes now (C2); real impls land with their consumers. *Done
  when:* each interface has a fake; compile-time `var _ Iface` assertions in
  place."
- **C2** (cross-cutting concern): each new OS touchpoint becomes a small sysdep
  interface + fake *before* the consuming logic is written; WP-1.4 seeds them.

## Open questions / decisions for planning

1. **Real impls now vs. `ErrNotImplemented` stubs.** AC #2 requires a
   `var _ Iface = (*RealImpl)(nil)` assertion, so a real-impl *type* must exist
   for each seam — but its methods can be real or stubs. `Clock.Now` and the
   `FileSystem` write methods are trivial, fully-testable stdlib one-liners and
   match the established "delegate to stdlib" pattern; `Keychain`, `Flock`, and
   `ProcessGroup` are non-trivial, macOS-specific, and explicitly deferred to
   consumer tickets. Proposal: write real stdlib impls now for `Clock` + the
   `FileSystem` writes; stub the other three (returning `ErrNotImplemented`) with
   the compile-time assertion. **Decision needed.**

2. **Introduce an `ErrNotImplemented` sentinel?** There is none today and the
   codebase grows interfaces incrementally rather than stubbing. If we stub the
   complex seams (per Q1), we need a sentinel — a new pattern. Alternative: don't
   stub at all, defer the *entire* real-impl file for the complex seams and ship
   only interface + fake now (but then there is no `RealImpl` type for the
   compile-time assertion AC #2 demands). **Decision needed**, tied to Q1.

3. **FileSystem: grow in place vs. narrow split.** (Ticket's own question.)
   Codebase convention (grow-as-needed, single concern) and the interface's own
   doc both point to **growing `FileSystem` in place** with write/stat/mkdir/
   remove/rename. Recommend grow-in-place. **Confirm.**

4. **Flock placement.** (Ticket's own question.) Concern-separation convention
   points to a **thin `Flock` seam in sysdep** (wrapping `unix.Flock`), with any
   higher-level lock abstraction living in the proxy package later (WP-3.4), not
   in sysdep. Recommend thin sysdep seam. **Confirm.**

5. **ProcessGroup interface shape.** This seam is the hardest to design without
   its consumer (WP-4.3). Options: (a) define a best-effort minimal interface now
   covering start-new-pgid / signal-group / wait-group / signal-notify per the
   design, accepting likely churn when the consumer lands; (b) keep it as
   minimal/stubbed as the assertion allows. **Decision needed** (overlaps Q1).

## Code references

- `internal/sysdep/commander.go:1-13,20-51` — package doc + canonical seam.
- `internal/sysdep/filesystem.go:5-33` — existing FileSystem (`ReadFile` only);
  doc explicitly delegates write/stat/mkdir/remove/rename growth to AC-0009.
- `internal/sysdep/pathresolver.go:8-58` — concern-split rationale + multi-method
  real impl.
- `internal/sysdep/sysdeptest/fake.go:14-63` — `FakeCommander` (maps + `WithTool`
  builder).
- `internal/sysdep/sysdeptest/filesystem.go:10-34` — `FakeFileSystem` (to extend).
- `internal/sysdep/sysdeptest/pathresolver.go:10-68` — per-method error-knob fake
  pattern (model for write-failure simulation).
- `internal/sysdep/filesystem_test.go:15-39`, `pathresolver_test.go` — real-impl
  smoke-test template.
- `internal/cli/cli.go:20-26,55-61` — composition root (where real impls *would*
  wire; AC-0009 leaves unused deps unwired).
- `internal/config/load.go:34-39`, `load_test.go` — consumer wiring + fake
  injection example.
- `docs/design.md` — Tech stack (~448), multi-agent lifecycle / process groups
  (~390-413), credential story (~425-444), threat model / locked keychain (~67),
  spike S2 (~20).
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` —
  WP-1.4 (154-159), C2 (102-104), conventions (40-50), consumer WPs.

## Verification (from the ticket)

1. `go build ./...` compiles (incl. compile-time assertions).
2. `go vet ./...` clean.
3. `go test -race ./internal/sysdep/...` passes (fakes/real impls smoke-tested).
4. Grep guard: each new interface has a matching fake in `sysdeptest/`.
5. `make test` green (no regressions in `cli`/`prereq`/`config`/`state`).
</content>
