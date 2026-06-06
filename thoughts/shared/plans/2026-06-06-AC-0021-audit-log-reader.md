---
date: 2026-06-06
ticket: AC-0021
title: "Plan — Audit log reader: logs --follow / --summary (WP-3.5)"
status: ready
research: thoughts/shared/research/2026-06-06-AC-0021-audit-log-reader.md
git_commit: 890fa7e
branch: main
---

# Plan: AC-0021 — Audit log reader: `logs --follow` / `--summary` (WP-3.5)

## Overview

Add a new Go package `internal/audit` (the read side of the egress audit log) and a
new `agent-creance logs` command. The log is the compact JSONL `egress.jsonl` (+
rotated `egress.jsonl.1`) the AC-0018 enforcer writes out-of-tree. The command:

- **`logs` (no flags)** — dump the combined `.1`+current stream once, one
  human-formatted line per entry, then exit.
- **`logs --summary`** — read `.1` then current as one logical stream and report
  allow / soft-deny / hard-deny counts (plus totals and intercepted/passthrough
  split).
- **`logs --follow`** — stream new entries live (starting at end-of-current), staying
  correct across a rename-based rotation, implemented natively with `fsnotify` (not
  `tail -f`).

## Decisions carried from the research checkpoint

- **`--follow` = concrete `fsnotify`, no new `sysdep` seam.** `internal/audit` owns its
  file I/O directly via the `os` package (it does *not* depend on `sysdep`). The
  rotation-follow loop is unit-tested with **real temp files** in a plain `go test`
  (the verification step-2 test: write, rotate mid-stream, write more, assert every
  entry crossed the flip). This is a deliberate, scoped deviation from the project's
  "all filesystem access through `sysdep`" rule — justified because `fsnotify` needs
  real kernel events that no fake can produce, and isolated to this one package. The
  *pure* parse/summarize/format core takes `io.Reader`/bytes and is fully hermetic
  (table + golden tests) regardless.
- **Output = human-formatted, dump-once for bare `logs`.** Bare `logs` prints
  `.1`+current once; `--follow` prints the same formatted lines and keeps streaming.
- **Defaults (minor):** `--follow` + `--summary` together is an error (mutually
  exclusive). `--follow` starts at **end of current** (streams only new entries — avoids
  re-dumping up to ~1 GB; matches the step-2 test, which writes entries *after* follow
  starts). A missing log file: `--summary`/dump report "no entries" and exit 0;
  `--follow` waits for the file to appear (it watches the parent dir) but errors with a
  friendly message if the state dir itself doesn't exist (cage never run).

## Current state

- No `internal/audit` package, no `logs` command, no `fsnotify` dependency
  (`go.mod:5-11` — cobra, go-internal, testify, `golang.org/x/sys`, yaml; Go 1.26.3).
- The format is fixed by the writer `internal/proxy/enforcer/audit.py`: compact JSONL,
  `0600`; intercepted entry `{ts, method, url, decision, rule, status}` (`rule` =
  `{"list","index"}` or `null`); passthrough entry `{ts, host, decision}`; decisions
  `allow`/`soft-deny`/`hard-deny`; `ROTATED_SUFFIX=".1"`; rotation = atomic
  `os.replace(current, .1)` then fresh current; at most one backup.
- `internal/state/state.go`: `Layout.EgressJSONL()` (line 179-180) →
  `<Root>/egress.jsonl`; `Root` is out-of-tree under `~/.cache/agent-creance/projects/<hash>`.
  **No `.1` accessor exists** — the reader currently has to concatenate `".1"` itself.
- CLI composition root `internal/cli/cli.go`: `App` (lines 20-32), `Main()` wiring
  (62-79), `newRootCmd` tree (41-56, registers version/doctor/policy). Commands close
  over `*App`, write through `app.Stdout`, declare flags via
  `cmd.Flags().BoolVar(&local, …)` (pattern: `policy.go:34-131`), and resolve the
  project from cwd `"."` via a `state`/`compile` resolver (`policy.go:137-155`).
- macOS/kqueue follow behavior (research-confirmed): watch the **parent directory**, not
  the file (a file watch is destroyed by the rename); key rotation off the `Create`
  for the new `egress.jsonl`; ignore `Chmod`; filter events by `Event.Name == path`;
  use a **stat-poll backstop** because kqueue events are coalesced/best-effort; the
  renamed file keeps its inode, so drain the open handle before reopening.

## Desired end state

- `internal/audit` exposes: a pure core (`Entry`, `ParseLine`, `Summarize`,
  `FormatEntry`, `Summary` + its renderer) and a concrete I/O layer (`Dump`,
  `Summarize`-over-files, `Follow`).
- `state.Layout.EgressJSONLRotated()` returns the `.1` path from one named place.
- `agent-creance logs` / `--summary` / `--follow` work end-to-end, wired into `App`.
- `make test` (race), `go build ./...`, `make lint` green; a testscript golden covers
  `logs` + `logs --summary` over a fixture; the follow-across-rotation test passes in
  plain `go test`.

## What we're NOT doing

- Writing/rotating the log (AC-0018, done).
- The v0.2 structured deny-decision log.
- Adding an `fsnotify`/watcher seam to `sysdep` (per the checkpoint decision).
- Re-dumping historical entries on `--follow` (starts at end).
- Filtering/grep/`--since`/per-host breakdown in `--summary` (only the AC-mandated
  decision counts + a total and an intercepted/passthrough split).

---

## Phase 1 — State `.1` accessor + the pure parse/summarize/format core

### Changes

**`internal/state/state.go`** — add a rotated-log accessor next to `EgressJSONL()`:
```go
const rotatedSuffix = ".1"

// EgressJSONLRotated is the single rotated backup of the audit log. The enforcer
// writes one backup (egress.jsonl.1); the reader (internal/audit) reads it then the
// current file as one logical stream. Naming it here keeps the ".1" contract in one
// Go place, mirroring the writer's ROTATED_SUFFIX.
func (l Layout) EgressJSONLRotated() string {
    return filepath.Join(l.Root, egressJSONLName+rotatedSuffix)
}
```
**`internal/state/state_test.go`** — extend the layout-suffix table to assert
`EgressJSONLRotated()` → `egress.jsonl.1` (mirrors the existing `EgressJSONL` case).

**New `internal/audit/entry.go`** — pure model + parsing (no `os`, no `fsnotify`):
- Decision constants: `DecisionAllow = "allow"`, `DecisionSoftDeny = "soft-deny"`,
  `DecisionHardDeny = "hard-deny"`.
- `type Rule struct { List string \`json:"list"\`; Index int \`json:"index"\` }`
- `type Entry struct { TS, Method, URL, Host string; Decision string; Rule *Rule; Status int }`
  with json tags (`method,omitempty`, `url,omitempty`, `host,omitempty`,
  `rule,omitempty`, `status,omitempty`).
- `func (e Entry) IsPassthrough() bool { return e.Host != "" }` (passthrough entries
  carry `host` and no `method`/`url`/`status`).
- `func ParseLine(line []byte) (Entry, error)` — `json.Unmarshal`; returns a wrapped
  error on malformed JSON so callers can skip-and-count.

**New `internal/audit/summary.go`** — pure aggregation + render:
- `type Summary struct { Total, Allow, SoftDeny, HardDeny, Intercepted, Passthrough, Unknown, Malformed int }`
- `func Summarize(readers ...io.Reader) (Summary, error)` — for each reader, scan
  line-by-line with a `bufio.Reader` + `ReadString('\n')` (not `bufio.Scanner`, to
  tolerate very long URLs > 64 KB); skip blank lines; `ParseLine` each; on parse error
  increment `Malformed` and continue (a corrupt line never aborts the summary); tally by
  decision (unknown decision → `Unknown`), and `Intercepted`/`Passthrough` by
  `IsPassthrough()`. Callers pass the `.1` reader then the current reader (order
  documented).
- `func (s Summary) Render() string` — fixed, golden-stable layout, e.g.:
  ```
  Audit summary: 42 entries (35 intercepted, 7 passthrough)
    allow      30
    soft-deny   8
    hard-deny   4
  ```
  When `Total == 0`: `No audit entries yet.` When `Malformed > 0`: append a
  `(N malformed line(s) skipped)` note.

**New `internal/audit/format.go`** — pure single-entry formatter:
- `func FormatEntry(e Entry) string`:
  - intercepted: `"<ts>  <decision padded to 9>  <method> <url> -> <status>"`.
  - passthrough: `"<ts>  <decision padded to 9>  <host> (passthrough)"`.
  - decision padded to width 9 (`soft-deny`/`hard-deny` are 9 chars) for column
    alignment.

### Tests
- `internal/audit/entry_test.go` — table test for `ParseLine`: intercepted (with
  `rule`), soft-deny (`rule:null` → nil), passthrough, malformed JSON (error), blank.
  `IsPassthrough` cases.
- `internal/audit/summary_test.go` — table/golden for `Summarize` over fixture
  multi-line `io.Reader`s (string readers): mixed decisions across two readers (`.1`
  then current), a malformed line counted not fatal, empty input. Golden for
  `Summary.Render()` (`testdata/summary.golden`) via the `-update` pattern
  (`internal/prereq/report_test.go:21,36-48`).
- `internal/audit/format_test.go` — table/golden for `FormatEntry` across all entry
  kinds (`testdata/format_lines.golden`).
- `internal/state/state_test.go` — `EgressJSONLRotated` suffix assertion.

### Success criteria

#### Automated
- [ ] `make test` passes incl. the new `internal/audit` + `internal/state` tests.
- [ ] `go build ./...` green.
- [ ] Golden files `internal/audit/testdata/summary.golden` and `format_lines.golden`
      exist and match.

#### Manual
- [ ] `Summarize` is decision-correct, never aborts on a malformed line, and reads
      `.1`-then-current order as one stream.

---

## Phase 2 — File-backed `Dump` + `Summarize` over `.1`+current

### Changes

**New `internal/audit/read.go`** — concrete file reading (uses `os` directly; this
package owns its I/O per the checkpoint decision):
- `func openStreams(currentPath string) (readers []io.ReadCloser, closeAll func(), err error)`
  — open `currentPath+".1"` if it exists, then `currentPath` if it exists, in that
  order; a missing file is skipped (not an error); returns the open readers and a
  combined closer. (Callers pass `layout.EgressJSONL()`; the `.1` is derived once here
  — or accept both paths from the caller via `layout.EgressJSONLRotated()`; prefer
  passing both explicit paths so the contract is visible.)
  - Refinement: signature `openStreams(rotatedPath, currentPath string)` taking both
    paths from `state.Layout` so `internal/audit` has no knowledge of the `.1` suffix.
- `func Dump(w io.Writer, rotatedPath, currentPath string) error` — open streams, scan
  line-by-line (same `bufio.Reader` approach), `ParseLine` + `FormatEntry` each,
  `fmt.Fprintln(w, …)`; skip malformed lines. No entries → write nothing (exit 0 at the
  command layer).
- `func SummarizeFiles(rotatedPath, currentPath string) (Summary, error)` — open
  streams and delegate to the pure `Summarize(readers...)`.

### Tests
- `internal/audit/read_test.go` (real temp files via `t.TempDir()`):
  - `Dump`: write a fixture `.1` + current with known entries; assert formatted output
    (compare against `FormatEntry` of the expected entries, or a golden).
  - `SummarizeFiles`: `.1`+current with known decisions → expected `Summary` counts
    (directly satisfies AC "reads `.1` and current in order … reports counts").
  - Missing `.1` (only current) and missing both (zero `Summary`, no error).

### Success criteria

#### Automated
- [ ] `make test` green incl. `read_test.go`.
- [ ] `SummarizeFiles` over a `.1`+current pair yields the expected allow/soft-deny/
      hard-deny counts (AC criterion 2).
- [ ] `go build ./...` green.

#### Manual
- [ ] `.1` is read before current; a missing file is a skip, not a crash.

---

## Phase 3 — Rotation-aware `Follow` (fsnotify + poll backstop)

### Changes

**`go.mod`** — `go get github.com/fsnotify/fsnotify@latest` (≥ v1.7; Go 1.26.3 ok).
Run `go mod tidy`; review the `go.sum` diff.

**New `internal/audit/follow.go`**:
- `func Follow(ctx context.Context, w io.Writer, dirPath, currentPath string) error`
  (the command passes `filepath.Dir(layout.EgressJSONL())` and `layout.EgressJSONL()`).
- Strategy (per research):
  1. Verify `dirPath` exists; if not, return a friendly error ("no audit log directory
     yet — has the cage run?").
  2. `w := fsnotify.NewWatcher()`; `w.Add(dirPath)` (watch the **directory**, not the
     file). `defer w.Close()`.
  3. Open `currentPath` if present, `Seek(0, io.SeekEnd)`; record offset and identity
     (`os.Stat` → `FileInfo`, compared later with `os.SameFile`). If absent, leave the
     handle nil and wait for a `Create`.
  4. Start a `time.NewTicker(1 * time.Second)` poll backstop.
  5. Loop on `select { ctx.Done() | watcher.Events | watcher.Errors | ticker.C }`:
     - Ignore events whose `Name != currentPath` and `Chmod`-only ops.
     - **Write** to `currentPath` (or ticker tick with `size > offset`): `readNew()` —
       read from offset to EOF via the open handle, append to a persistent leftover
       buffer, split on `\n`, `ParseLine`+`FormatEntry`+print each *complete* line,
       keep the partial remainder buffered (a `Write` can flush a partial JSONL line).
     - **Rotation** — a `Create` for `currentPath` whose new inode differs from the
       tracked one (or a ticker tick where `os.SameFile` says the on-disk inode
       changed): `readNew()` to drain the **old open handle** to EOF first, `Close()`
       it, open the new `currentPath` from offset 0, update identity, `readNew()` again
       (don't wait for a `Write` — first appends may be coalesced into the `Create`).
     - **watcher.Errors**: surface as the returned error.
     - **ctx.Done()**: flush nothing partial, return `ctx.Err()` (or nil on
       cancellation).
  - Rotation detected by **inode identity** (`os.SameFile`), never by size shrinking.
    The ticker makes fsnotify a latency reducer, not a correctness dependency.

### Tests
- `internal/audit/follow_test.go` (real temp files; the AC step-2 test):
  - Create `dir/egress.jsonl`, write one entry. Start `Follow` in a goroutine writing
    to a synchronized sink (a mutex-guarded buffer or a channel drained by the test).
  - Write N entries → assert they appear (poll the sink with a generous timeout, e.g.
    `require.Eventually`, rather than fixed sleeps — keeps it robust against kqueue
    latency).
  - **Rotate mid-stream**: `os.Rename(path, path+".1")`, create a fresh `path`, write M
    more entries. Assert all N+M post-start entries are emitted in order and the
    follower did **not** get stuck on the renamed file (AC criterion 1).
  - Cancel the context; assert `Follow` returns promptly.
  - A second test: `Follow` on a not-yet-existing `currentPath` in an existing dir →
    waits, then emits once the file is created and written.
  - Use `require.Eventually` / channel reads with timeouts (no brittle sleeps); guard
    shared output with a mutex for `-race`.

### Success criteria

#### Automated
- [ ] `make test` (race) green incl. `follow_test.go` — the follow-across-rotation test
      passes (AC criterion 1; verification step 2 follow test).
- [ ] `go build ./...` green; `go mod tidy` leaves a clean tree.

#### Manual
- [ ] No `tail` shell-out anywhere; following uses `fsnotify` on the parent dir with a
      stat-poll backstop (AC criterion 3).
- [ ] Partial trailing lines are buffered until the newline arrives (no half-line
      output).

---

## Phase 4 — The `logs` command + wiring + testscript + close-out

### Changes

**New `internal/cli/logs.go`** — `func newLogsCmd(app *App) *cobra.Command`:
- `Use: "logs"`, `Short: "Read the egress audit log (dump, --summary, or --follow)"`,
  `Args: cobra.NoArgs`.
- Flags: `var follow, summary bool`; `cmd.Flags().BoolVar(&follow, "follow", false,
  "stream new entries live, rotation-aware")`; `cmd.Flags().BoolVar(&summary,
  "summary", false, "print allow/soft-deny/hard-deny counts and exit")`.
- `RunE`:
  - If `follow && summary` → return an error ("--follow and --summary are mutually
    exclusive").
  - Resolve the project layout from cwd: `resolver := state.New(app.Paths)`; `layout,
    err := resolver.Resolve(".")`. (Add a tiny `resolveLayout(app, ".")` helper in
    `cli` if it reads cleaner, mirroring `resolvePolicy`.)
  - `cur := layout.EgressJSONL()`, `rot := layout.EgressJSONLRotated()`.
  - `--summary`: `s, err := audit.SummarizeFiles(rot, cur)`; `fmt.Fprint(app.Stdout,
    s.Render())`.
  - `--follow`: `audit.Follow(cmd.Context(), app.Stdout, filepath.Dir(cur), cur)`.
  - default (no flags): `audit.Dump(app.Stdout, rot, cur)`.
- Register in `newRootCmd`: `root.AddCommand(newLogsCmd(app))` (`cli.go:52-54`).

(`App` needs no new field — `Paths` is already injected and `internal/audit` reads
files itself.)

**New testscript `internal/cli/testdata/script/logs_summary.txtar`** (hermetic; mirror
`policy_refresh.txtar`'s `HOME`/`XDG_CACHE_HOME` redirect):
- `env HOME=$WORK/home` / `env XDG_CACHE_HOME=$WORK/cache`.
- Embed a fixture `egress.jsonl` (and a `.1`) under the resolved state dir. **Wrinkle:**
  the state path is `…/projects/<hash>/egress.jsonl` where `<hash>` derives from the
  realpath of `$WORK` — not knowable statically. Resolve this by **running `logs` from a
  fixed working dir and asserting on stdout patterns** rather than pre-seeding by hash:
  the cleanest hermetic approach is a small txtar that (a) for the empty case asserts
  `logs --summary` prints `No audit entries yet.` (no file present — exercises the
  missing-file path end-to-end through the real resolver), and (b) leaves
  golden-content assertions to the Go-level `read_test.go`/`summary_test.go`. If a
  seeded-fixture summary is wanted in-script, add a tiny test-only env override for the
  state root, or compute the hash in the test `Setup` — **decide during implementation**;
  do not over-engineer the txtar. At minimum the script proves command wiring +
  flag parsing + the empty-log message + mutually-exclusive-flags error
  (`! agent-creance logs --follow --summary`).
- Also assert bare `logs` on an empty log prints nothing and exits 0, and
  `logs --follow --summary` errors.

(`--follow` is a long-lived streaming command and is **not** driven from testscript —
the package-level `follow_test.go` covers it, consistent with the note in
`policy_refresh.txtar` that un-stubbable behaviors are unit-tested.)

**`docs/design.md`** — no change expected (lines 380-381/414-420 already describe
`logs --follow`/`--summary`); re-read and only adjust if the implemented surface
diverges.

**`thoughts/shared/tickets/AC-0021-audit-log-reader.md`** — tick the three acceptance
criteria, answer the open research question (macOS fsnotify: watch the parent dir; a
file watch is destroyed by the rename; `Create` keys rotation; stat-poll backstop),
set **Status: Done**, and add a Notes entry summarizing the design (pure core +
concrete fsnotify follower; `EgressJSONLRotated()` accessor; the scoped sysdep
deviation).

### Tests
- `internal/cli/logs_test.go` (optional, if a non-testscript unit assertion is cleaner
  for the mutually-exclusive-flags and empty-log paths) — construct an `App` with fakes
  + a temp `XDG_CACHE_HOME`, run the command, assert stdout/err. Prefer the testscript
  for end-to-end and keep this minimal.
- The `logs_summary.txtar` testscript (above).

### Final verification (AC-level — run everything that could be affected)
- `make test` (race) — green (unit + audit + testscript).
- `go build ./...` — green.
- `make lint` — green.
- `make golden` only if intentionally regenerating goldens; review the diff.
- Manually: build, point at a state dir with a hand-written `egress.jsonl`(+`.1`), run
  `logs`, `logs --summary`; start `logs --follow`, append a line + simulate a rotation,
  confirm live output crosses the flip. (Document the outcome; this is the real-macOS
  confirmation of the fsnotify question.)

### Success criteria

#### Automated
- [ ] `agent-creance logs --summary` reads `.1`+current and reports allow/soft-deny/
      hard-deny counts (AC criterion 2) — covered by `read_test.go` + testscript.
- [ ] `agent-creance logs --follow` streams across a rotation without getting stuck
      (AC criterion 1) — covered by `follow_test.go`.
- [ ] Native `fsnotify`, no `tail` shell-out (AC criterion 3) — code review + grep.
- [ ] `--follow --summary` is rejected; empty log prints a friendly message.
- [ ] `make test`, `go build ./...`, `make lint` all green.

#### Manual
- [ ] Ticket acceptance boxes ticked, research question answered, Status: Done.
- [ ] Bare `logs` and `--follow` render human-readable lines; `--summary` matches the
      golden layout.

---

## Testing strategy summary

| AC criterion | Test |
|---|---|
| `--follow` streams across a rotation, not stuck on renamed file | `internal/audit/follow_test.go` (real temp files, rotate mid-stream) |
| `--summary` reads `.1`+current in order, reports decision counts | `internal/audit/summary_test.go` (pure) + `read_test.go` (files) |
| Native fsnotify, no `tail` shell-out | `follow.go` impl + grep/review; `follow_test.go` |
| Parse correctness / robustness to malformed lines | `entry_test.go`, `summary_test.go` (table) |
| Human-formatted output | `format_test.go` golden + testscript |
| Command wiring, flags, empty-log, mutual exclusion | `logs_summary.txtar` testscript |
| `.1` path contract centralized | `state_test.go` (`EgressJSONLRotated`) |

## Rollout / risk notes

- One new direct dependency (`fsnotify`); pure stdlib otherwise (`encoding/json`,
  `bufio`, `os`, `io`, `time`, `context`).
- `follow_test.go` depends on real kqueue timing (the accepted trade-off); use
  `require.Eventually`/channel-with-timeout and a mutex-guarded sink, never fixed
  sleeps, to keep it `-race`-clean and non-flaky.
- The stat-poll backstop (1 s) guarantees correctness even if a kqueue event is
  coalesced/dropped — fsnotify only reduces latency.
- Scoped convention deviation: `internal/audit` touches `os`/`fsnotify` directly rather
  than `sysdep`. Documented in the package doc + ticket notes; the pure core stays
  seam-free and hermetically tested via `io.Reader`.
- Commit signing: if the SSH signing key is unreadable in this environment (as in
  AC-0018/0020), commits this session may be `--no-gpg-sign`; note it.
