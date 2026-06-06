---
date: 2026-06-06
ticket: AC-0021
title: "Research — Audit log reader: logs --follow / --summary (WP-3.5)"
status: complete
git_commit: b088232335263e02cf21060eba34ef04d3561e7a
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0021 — Audit log reader: `logs --follow` / `--summary` (WP-3.5)

## Research question

How should `internal/audit` power `agent-creance logs --follow` (rotation-aware via
`fsnotify`, not `tail -f`) and `agent-creance logs --summary` (reads `egress.jsonl.1`
then `egress.jsonl` as one logical stream, reporting allow/soft-deny/hard-deny
counts) — given the AC-0018 writer format, the project's `sysdep` testability seam,
and the macOS/kqueue behavior of `fsnotify` across a rename-based rotation?

## Summary

This is a **new Go package `internal/audit`** plus a **new `agent-creance logs`
cobra command** — the read side of the audit log whose write side AC-0018 already
shipped (in the Python enforcer addon). No `internal/audit` package, no `logs`
command, and no `fsnotify` dependency exist yet.

The work splits cleanly into three pieces of very different difficulty:

1. **Parsing + summary (easy, pure).** The log is compact JSONL with two well-known
   entry shapes. `--summary` reads `egress.jsonl.1` (if present) then `egress.jsonl`
   in order, parses each line, and tallies decisions. This is pure logic over
   `[]byte` already obtainable through the existing `sysdep.FileSystem.ReadFile`
   seam — table-driven + golden-file testable, no new seam needed.

2. **One-shot dump (easy).** Bare `logs` (no flags) prints the combined `.1`+current
   stream once. Same read path as `--summary`, different rendering.

3. **`--follow` (the hard part).** A live, rotation-aware tail. The writer rotates by
   **atomic rename** (`os.replace(egress.jsonl, egress.jsonl.1)`) then recreates a
   fresh `egress.jsonl`. The web research is unambiguous: on macOS/kqueue a watch
   placed **on the file** is destroyed by the rename, so the robust pattern is to
   **watch the parent directory** and key rotation off the `Create` event for the new
   `egress.jsonl`, with a stat-poll backstop because kqueue events are coalesced /
   best-effort. This needs incremental (offset-based) reads and inode-identity
   detection, neither of which the current whole-file `FileSystem.ReadFile` seam
   provides — so `--follow` requires **new abstractions** (an event source and an
   incremental reader), and that is the one genuine architectural decision in this
   ticket (see Open Questions).

The format contract, the path layout, the CLI/command patterns, the `sysdep` seam
conventions, and the macOS rename behavior are all now pinned down below.

## The audit log format (from AC-0018, the contract we read)

Source of truth: `internal/proxy/enforcer/audit.py` (the writer) and the AC-0018
ticket/plan. Compact one-object-per-line JSON, UTF-8, newline-terminated, mode
`0600`.

Two entry shapes:

- **Intercepted** (TLS-terminated allow / soft-deny / hard-deny) — key order
  `{ts, method, url, decision, rule, status}`:
  - `ts`: ISO-8601 UTC string (`datetime.now(timezone.utc).isoformat()`).
  - `method`: HTTP method.
  - `url`: request URL, **query-token-scrubbed** (sensitive param values replaced with
    the literal `REDACTED`).
  - `decision`: one of `"allow"`, `"soft-deny"`, `"hard-deny"`.
  - `rule`: `{"list": <name>, "index": <int>}` or `null` (null for soft-deny — no
    matching rule).
  - `status`: HTTP response status code (integer; `403` for synthesized denies).
- **Passthrough** (host-only, for ignored/tunneled connections) — key order
  `{ts, host, decision}`. No `method`/`url`/`path`/`status` — those are unobtainable
  for an ignored connection. `decision` is `"allow"` (clean tunnel) or `"hard-deny"`
  (denied CONNECT).

The decision vocabulary is exactly three values: `allow`, `soft-deny`, `hard-deny`
(`internal/proxy/enforcer/policy.py` `DECISION_*`). The reader must tally these three
and should treat passthrough vs. intercepted as a distinguishable axis (presence of
`host` vs. `method`/`url`).

### Rotation contract

`internal/proxy/enforcer/audit.py`:
- `ROTATED_SUFFIX = ".1"` (line 48) with the explicit comment: *"The reader (AC-0021)
  reads `.1` then current as one logical stream, so this name is part of that
  contract."*
- Rotated path is literal concatenation `path + ".1"` → `egress.jsonl.1` (not a
  `filepath`/`os.path` join).
- `_rotate()` (lines 176-182): `os.replace(current, rotated)` — atomic rename that
  overwrites any prior `.1` — then opens a fresh current. **At most one backup
  exists**, capping disk at ~2× `DEFAULT_MAX_BYTES` (500 MB → ~1 GB/project).
- The rename preserves the inode: a renamed file keeps its identity, so a follower
  holding an open handle to the old file can drain it to EOF after the rename.

So the **logical stream order** for both `--summary` and the bare dump is:
`egress.jsonl.1` (older) **then** `egress.jsonl` (newer).

## Where the log lives (path layout)

`internal/state/state.go`:
- `Layout.EgressJSONL()` (line 179-180): `filepath.Join(l.Root, "egress.jsonl")`
  where `egressJSONLName = "egress.jsonl"` (line 41).
- `Layout.Root` = `<cache>/agent-creance/projects/<hash>` — out-of-tree, under
  `$XDG_CACHE_HOME` or `$HOME/.cache` (`cacheRoot()`, lines 159-168; deliberately not
  `os.UserCacheDir`, so macOS uses `~/.cache`, not `~/Library/Caches`).
- `<hash>` = first 8 bytes of SHA-256 of the realpath-resolved project dir, 16 lowercase
  hex chars (`hashPath()`, lines 97-100).
- **There is no Go constant for the `.1` suffix.** The reader derives the rotated path
  itself as `layout.EgressJSONL() + ".1"`. *Recommendation:* add a
  `Layout.EgressJSONLRotated()` accessor (mirroring the other `Layout` accessors) so
  the `.1` contract lives in one Go place, matching how `ROTATED_SUFFIX` is a single
  named constant on the writer side.

`Layout` is produced by `state.New(paths sysdep.PathResolver).Resolve(dir)`
(`state.go:78-93`). The `logs` command resolves the current project the same way the
policy commands do — from cwd `"."`. The proxy lifecycle already consumes
`Layout.EgressJSONL()` to point the enforcer at the log
(`internal/proxy/lifecycle.go:236`), so the producer and reader agree on the path by
construction.

## How a new command wires in (CLI patterns)

Composition root `internal/cli/cli.go`:
- `App` struct (lines 20-32) holds injected seams: `Commander`, `Stdout`, `Stderr`,
  `Tested`, `FS sysdep.FileSystem`, `Paths sysdep.PathResolver`, `Clock sysdep.Clock`,
  `HTTP sysdep.HTTPGetter`.
- `Main()` (lines 62-79) wires the real impls (`sysdep.OSFileSystem{}` etc.) and
  executes the root command.
- `newRootCmd(app)` (lines 41-56) builds the tree: `root.AddCommand(newVersionCmd,
  newDoctorCmd, newPolicyCmd)`. A `logs` command is added here as
  `root.AddCommand(newLogsCmd(app))`.

Command shape (mirror `internal/cli/policy.go`):
- `func newLogsCmd(app *App) *cobra.Command` closing over `app`, with `Args:
  cobra.NoArgs`, a `RunE` that writes through **`app.Stdout`** (project convention —
  `version.go:16-17`: "we write through app.Stdout … so output is captured in tests").
- Flags via closure-captured locals bound after construction:
  `cmd.Flags().BoolVar(&follow, "follow", false, …)`,
  `cmd.Flags().BoolVar(&summary, "summary", false, …)` (pattern: `policy.go:61,91`,
  and `policy explain`'s two-flag form at `policy.go:98-131`).
- Resolving the project's state dir from cwd: build a `state.New(app.Paths)` resolver
  and `Resolve(".")` (the policy commands wrap this in `resolvePolicy(ctx, app, ".")`,
  `policy.go:137-155`; `logs` needs only the `Layout`, not a compiled policy).

`fsnotify` is **not** yet a dependency (`go.mod` has only cobra, go-internal, testify,
`golang.org/x/sys`, yaml). Adding it means `go get github.com/fsnotify/fsnotify`
(requires Go ≥1.23; we're on 1.26.3 — fine). `buildinfo` pins *external tool*
versions (agent-safehouse/mitmproxy), not Go module deps, so no `buildinfo` change.

## The `sysdep` seam and what `--follow` needs that it lacks

`internal/sysdep` is the project's single testability seam — every OS facility is a
small interface with an `OS*` production impl (`var _ Iface = (*OSImpl)(nil)` compile
assertion) and a scripted fake in `internal/sysdep/sysdeptest`. The documented
convention (`commander.go:1-13`): keep interfaces small, define at point of use,
production injects real, tests inject fakes; new side-effecting deps get a new
interface + fake, never an inline `os`/`exec` call.

Existing seams: `Commander`, `Clock`, `FileSystem`, `Flock`/`LockedFile`,
`HTTPGetter`, `Keychain`, `PathResolver`, `ProcessGroup`/`Process`, `ProcessManager`,
`PortAllocator`.

Relevant gaps for `--follow`:
- `FileSystem` (`filesystem.go:19-39`) is **whole-file only**: `ReadFile` →`[]byte`,
  `WriteFile`, `Stat` → `fs.FileInfo`, `MkdirAll`, `Remove`, `Rename`. **No** `Open`/
  `io.Reader`/`Seek` and **no** line streaming. `--summary` and the one-shot dump are
  fine with `ReadFile`; `--follow` needs **incremental, offset-based reads** of a file
  that's actively being appended.
- **No file-watching / fsnotify / event-notification seam exists** anywhere (the only
  `Notify` is `ProcessGroup.Notify`, which wraps `os/signal` — unrelated).
- `FileSystem.Stat` returns `fs.FileInfo`; the fake's `fakeFileInfo`
  (`sysdeptest/filesystem.go:147-159`) returns a **zero `ModTime`** and exposes no
  inode/identity — so rotation-by-inode detection isn't expressible against the
  current fake without extending it.

The closest precedent for the *shape* a follow seam would take is `Flock.Acquire →
LockedFile` (`flock.go`): a method that returns a **stateful handle interface**. A
watcher seam (`Watch(dir) → Watcher` with an events channel) and/or an incremental
reader (`Open(name) → ReadSeekCloser`) would follow that idiom: real impl in
`internal/sysdep`, fake in `sysdeptest`, real impl smoke-tested once, consumers tested
against the fake. The `Clock` seam (`clock.go` + `sysdeptest/clock.go`) is the
smallest complete end-to-end example to copy.

## fsnotify on macOS across a rename rotation (the open research question)

The ticket's open question — *"fsnotify behavior on macOS for rename events — does it
fire reliably for the rotation pattern?"* — is answered by the web research, citing
the fsnotify README/godoc and issues:

1. **Watching the file directly is fatal across rotation.** The `Rename` op godoc:
   *"The path was renamed to something else; any watches on it will be removed."* On
   kqueue the watch is bound to the moved inode and fsnotify removes it internally;
   you get one `Rename` event then silence, and never see the new `egress.jsonl`'s
   events. Re-adding after `Rename` is racy (issue #214).
2. **Watch the parent directory instead** — this is the canonical, officially
   documented pattern (fsnotify's own `cmd/fsnotify/file.go` example: *"instead of
   watching the file directly it watches the parent directory"*). The directory watch
   is stable across the rename and delivers a **`Create` for the new `egress.jsonl`**
   at the original path — that `Create` is the rotation trigger. Filter every event by
   `Event.Name == path`.
3. **Events to expect** (watching the dir): `Rename` on `egress.jsonl` (old name) →
   `Create` on `egress.jsonl` (new file) → `Write` as appends land. **Ignore `Chmod`**
   (kqueue fires it on truncate; README: best to ignore). A `Write` on the *directory*
   itself also fires on kqueue — filter it out.
4. **Treat events as hints, not deltas.** macOS coalesces/duplicates events (Spotlight,
   issues #277/#600), so the robust design runs a **low-frequency stat-poll backstop**
   (every ~1-2 s): if the on-disk inode at `path` differs from the tracked one, force a
   rotation; if size > offset, force a read. fsnotify becomes a latency reducer, not a
   correctness dependency. (Our state dir is under `~/.cache`, outside common
   Spotlight-indexed paths, which helps.)
5. **Reading across the flip (POSIX, not fsnotify):** the renamed file keeps the same
   inode, so drain the **already-open handle** to EOF *before* closing and reopening
   the new `egress.jsonl` from offset 0 — do **not** reopen `.1` by name (racy if a
   second rotation happens). Buffer incomplete trailing lines (a `Write` may flush a
   partial JSONL line) until a newline arrives. Detect rotation by inode identity
   (`os.SameFile`), not by size shrinking.

Recommended state machine: `FOLLOWING → ROTATING → FOLLOWING`. In `FOLLOWING`, on a
`Write` for the path read from offset to EOF; on `Create`/`Rename` for the path (new
inode) go to `ROTATING`. In `ROTATING`: drain old handle to EOF, close, open new from
0, immediately read to EOF (don't wait for a `Write` — appends may be coalesced into
the `Create`), back to `FOLLOWING`. Verify against real macOS in an integration test.

## Testing approach (project conventions)

Per the profile + CLAUDE.md:
- **Pure logic (parse, scrub-tolerant tally, summary aggregation) → table-driven
  tests** (e.g. `internal/prereq/version_test.go`).
- **Rendered artifacts (summary report, formatted entry lines) → golden-file tests**
  with a `-update` flag (`internal/prereq/report_test.go:21,36-48`;
  `testdata/*.golden`; `make golden`).
- **CLI behavior → hermetic testscript `.txtar`** under
  `internal/cli/testdata/script/`. The `policy_refresh.txtar` precedent shows
  redirecting `HOME`/`XDG_CACHE_HOME` under `$WORK` and embedding fixture files — the
  same trick lets a `logs --summary` script point at a fixture `egress.jsonl` in a
  fake state dir and assert golden output. The harness registers `agent-creance →
  cli.Main()` (`script_test.go:24-51`) and exposes `$CREANCE_BIN`.
- **fsnotify against real files → `//go:build integration`** (the project never
  invokes real external tools or relies on real OS event timing in `make test`). This
  is also where the open macOS-rename question gets its real-world answer.

The ticket's verification step 2 (`go test -race ./internal/audit/...` — a follow test
that triggers a rotation mid-stream and asserts every entry crossed the flip) is the
crux: it must be runnable under `make test`. That is achievable **either** by testing
the follow *state machine* against a fake event source + fake incremental reader
(fully hermetic), **or** by exercising real fsnotify against real temp files in a
plain `go test` (faithful but dependent on real kqueue timing → potential flakiness,
and it bends the "filesystem through sysdep" convention). This fork is the main Open
Question.

## Code references

- `internal/proxy/enforcer/audit.py:44-48,80-128,140-182` — writer: entry shapes,
  URL scrubbing, compact JSONL encode, `ROTATED_SUFFIX = ".1"`, `os.replace` rotation.
- `internal/proxy/enforcer/policy.py` — `DECISION_*` vocabulary (allow/soft-deny/hard-deny).
- `internal/state/state.go:41,63-72,78-93,159-180` — `Layout`, `EgressJSONL()`,
  `Resolver.Resolve`, cache root; no `.1` accessor yet.
- `internal/proxy/lifecycle.go:236` — existing `EgressJSONL()` producer-side consumer.
- `internal/cli/cli.go:20-32,41-56,62-79` — `App`, `newRootCmd`, `Main()` wiring.
- `internal/cli/policy.go:20-27,34-63,98-131,137-155` — command/flag/`resolve*` patterns.
- `internal/cli/version.go:11-29`, `internal/cli/doctor.go:14-38` — minimal command shape, `app.Stdout`.
- `internal/sysdep/filesystem.go:19-39` — whole-file `FileSystem` (no Open/Seek/stream).
- `internal/sysdep/flock.go` — the "method returns a stateful handle interface" precedent.
- `internal/sysdep/clock.go` + `internal/sysdep/sysdeptest/clock.go` — smallest end-to-end seam example.
- `internal/sysdep/sysdeptest/filesystem.go:147-159` — `fakeFileInfo` (zero ModTime, no inode).
- `internal/cli/script_test.go:24-51`, `internal/cli/testdata/script/policy_refresh.txtar` — testscript harness + state-dir-redirect fixture pattern.
- `internal/prereq/report_test.go:21,36-48` — golden-file `-update` pattern.

## Architecture insights

- **The format is a contract shared with a constant on the writer side.** `.1` is named
  once in Python (`ROTATED_SUFFIX`); mirror that with one named place in Go
  (`Layout.EgressJSONLRotated()`), so the two sides can't drift silently.
- **Two of the three behaviors need no new infrastructure.** `--summary` and the bare
  dump are pure functions over `ReadFile([.1]) + ReadFile(current)`; they're the easy,
  high-coverage core and should land first (and they satisfy the summary AC + the
  testscript golden AC on their own).
- **`--follow` is the only part that touches new OS surface** (fsnotify + incremental
  reads) and is therefore where the testability seam decision matters. Keeping the
  parse/tally logic decoupled from the follow mechanism means the risky part is small
  and isolated.
- **The robust follow design leans on a poll backstop**, per the research — which, if
  adopted, also happens to be the most hermetically testable (a `FakeClock`-driven
  loop over a fake filesystem needs no real kernel events). That nudges the seam
  decision toward an abstraction the follow loop drives via injected deps.

## Open Questions

1. **`--follow` architecture & how to satisfy the `go test -race ./internal/audit/...`
   follow test** — pure fsnotify (integration-tested only) vs. a seam that makes the
   rotation-follow loop hermetically unit-testable vs. a poll-based follower with
   fsnotify as a latency hint. Multiple valid approaches; needs a decision.
2. **Bare `logs` behavior and entry rendering** — what does `logs` with no flags do
   (one-shot dump of `.1`+current? require a flag?), and do `--follow`/dump print raw
   JSONL lines or a human-formatted line per entry?
3. **(Minor, defaultable) `--summary` granularity** — per-decision counts
   (allow/soft-deny/hard-deny) + total is the AC-mandated minimum; whether to also
   split intercepted vs. passthrough or surface top hosts is a nice-to-have.
4. **(Minor, defaultable)** `--follow` + `--summary` together → treat as mutually
   exclusive (error); missing log file → summary reports zero/"no entries" and exits 0,
   follow watches the dir and waits for the file to appear.
