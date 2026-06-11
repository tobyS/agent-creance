# AC-0039: Accept both safehouse binary names — Implementation Plan

**Ticket:** `thoughts/shared/tickets/AC-0039-safehouse-binary-names.md`
**Research:** `thoughts/shared/research/2026-06-11-AC-0039-safehouse-binary-names.md`
**Date:** 2026-06-11
**Status:** Ready

## Overview

Make either executable name — `safehouse` (preferred; what the brew formula
installs) or `agent-safehouse` (fallback) — satisfy the safehouse prerequisite.
Resolve the name once in `prereq.Check` and use that same resolved name for the
version query, doctor/report output, and the cage launch, so a passing check
guarantees the launch execs an existing binary.

## Current state

Four independent literals must agree today and don't (see research §"Summary"):

- `prereq.DefaultTools`: `Name: "agent-safehouse"` (`internal/prereq/prereq.go:39`)
  — used as LookPath key, `--version` argv[0], and display label.
- `cage.Binary = "safehouse"` (`internal/cage/cage.go:38`) — argv[0] of the
  actual launch (`Invocation.Path`, exec'd via `ProcessGroup.Start` with **no
  prior LookPath**).
- `internal/cli/version.go:23`: hardcoded `[]string{"agent-safehouse", "mitmproxy"}`
  keying into `app.Tested`.
- `buildinfo.TestedVersions` key `"agent-safehouse"` (`internal/buildinfo/buildinfo.go:35`).

Result: `run` refuses on a host that has `safehouse` installed (the user-hit
bug), and conversely a host with only `agent-safehouse` would pass the check and
then fail at exec.

## Desired end state

- `prereq.Tool` carries an ordered candidate list; `Check` resolves the first
  name found on PATH, records it on the `Result`, and uses it for the version
  query.
- `run` threads the resolved name into the cage invocation; `cage.Build` uses it
  (falling back to `cage.Binary` when unset, for tests/integration callers).
- Doctor/prereq report shows which name satisfied the check (annotation
  `via <name>` when it differs from the canonical label). Missing case keeps the
  canonical `agent-safehouse` label + brew hint (checkpoint decision).
- `version` stays a static tested-against listing (checkpoint decision), but the
  shared literals move to `buildinfo` constants so the four-way drift cannot
  recur.

### Decisions from ticket + checkpoint (binding)

1. Resolve once, use everywhere (check ⇒ reports ⇒ launch).
2. `safehouse` wins when both names are on PATH.
3. Doctor shows the resolved name; `version` is NOT extended to probe PATH.
4. Missing-tool display label stays `agent-safehouse`.

## What we're NOT doing

- No user-configurable binary path (config key / env var).
- No change to mitmproxy handling (it gets the same `Binaries` field with a
  single candidate — pure refactor, no behavior change).
- No version cross-check when both names are installed.
- No change to the YAML `safehouse:` config key, `.sb` header comments, or init
  scaffold.
- Not switching `Invocation.Path` to an absolute path — we pass the resolved
  *name* (the existing "caller resolves on PATH" contract from AC-0023 holds;
  exec re-resolves the same name).

## Implementation approach

Single source of truth lands in `internal/buildinfo` (the existing home for
"tested-against external tool" facts, already imported by `cli`; it is a leaf
package so neither `prereq` nor `cage` importing it creates a cycle):

```go
// Tool name constants — canonical display labels and TestedVersions keys.
const (
    ToolSafehouse = "agent-safehouse"
    ToolMitmproxy = "mitmproxy"
)

// SafehouseBinaries: executable names agent-safehouse may be installed as,
// in preference order (first found on PATH wins).
var SafehouseBinaries = []string{"safehouse", "agent-safehouse"}
```

`prereq.Tool` gains `Binaries []string` (ordered lookup candidates; empty ⇒
`[Name]` so existing constructors keep working). `Result` gains
`ResolvedName string` (empty when not installed). `cage.Binary` becomes
`var Binary = buildinfo.SafehouseBinaries[0]` so the preferred candidate and
the launch default are the same fact. `cage.Inputs` gains `Binary string`
(empty ⇒ `cage.Binary`). `run.go` extracts the resolved name from the check
results via a new `prereq.ResolvedBinary(results, name)` helper and sets it on
the cage inputs.

---

## Phase 1: prereq multi-name resolution + buildinfo constants

### Changes

1. **`internal/buildinfo/buildinfo.go`**: add `ToolSafehouse` / `ToolMitmproxy`
   constants; key `TestedVersions` with them; add `SafehouseBinaries`
   (`[]string{"safehouse", ToolSafehouse}` — note the fallback executable name
   happens to equal the canonical label).
2. **`internal/prereq/prereq.go`**:
   - `Tool` gains `Binaries []string` with doc: ordered PATH-lookup candidates,
     first found wins; empty means `[Name]`. `Name` is re-documented as the
     canonical display label (and the executable name when `Binaries` is empty).
   - `DefaultTools`: safehouse entry uses `Name: buildinfo.ToolSafehouse`,
     `Binaries: buildinfo.SafehouseBinaries`,
     `Tested: tested[buildinfo.ToolSafehouse]`; mitmproxy uses
     `Name: buildinfo.ToolMitmproxy` (no `Binaries` — single-candidate default).
     (`tested` stays a parameter; only the key literals move to constants.)
   - `Result` gains `ResolvedName string`.
   - `Check`: iterate candidates in order; first `LookPath` hit sets
     `Installed=true` + `ResolvedName`; `Output` is called with the **resolved**
     name (today's discarded-LookPath behavior preserved otherwise: bare name,
     not absolute path).
   - New helper `ResolvedBinary(results []Result, name string) (string, bool)`:
     returns the `ResolvedName` for the tool whose `Tool.Name == name` and which
     is installed.
   - `Missing` / `MissingInstructions` unchanged (display label = `Tool.Name`,
     per checkpoint decision).
3. **`internal/prereq/report.go`**: `installedField` renders
   `installed X via <resolved>, tested against Y` when
   `ResolvedName != "" && ResolvedName != Tool.Name`; unchanged otherwise
   (mitmproxy and the missing case never get the annotation; results
   constructed without `ResolvedName` — e.g. doctor's existing render tests —
   are unaffected).
4. **Tests** (`internal/prereq/prereq_test.go`, table-driven):
   - only `safehouse` on PATH ⇒ installed, `ResolvedName == "safehouse"`,
     version queried via `safehouse`.
   - only `agent-safehouse` ⇒ installed, `ResolvedName == "agent-safehouse"`.
   - both ⇒ `ResolvedName == "safehouse"` (preference order, deterministic).
   - neither ⇒ missing; `MissingInstructions` still shows
     `agent-safehouse:` + brew hint.
   - `ResolvedBinary` helper happy/missing cases.
   - Use `FakeCommander.WithTool` — no fake changes needed (research §5).
5. **Golden**: extend `report_test.go` cases with a resolved-via-safehouse row;
   regenerate `internal/prereq/testdata/doctor_report.golden` (`make golden`),
   review diff.

### Success criteria

- [ ] Automated: `make test` green; `make lint` green; `go build ./...` clean.
- [ ] Automated: new table cases above pass; golden diff reviewed and only
      shows the intended new/changed rows.

## Phase 2: thread the resolved binary into the cage launch

### Changes

1. **`internal/cage/cage.go`**: `Binary` becomes
   `var Binary = buildinfo.SafehouseBinaries[0]` (same value `"safehouse"`;
   comment updated to point at buildinfo as the source of truth). `Inputs`
   gains `Binary string` — "the resolved safehouse executable (from the prereq
   check); empty means the default `Binary`". `Build` sets
   `Invocation.Path = inputs.Binary` with the empty⇒`Binary` fallback.
2. **`internal/cli/run.go`**: after the prereq gate (line ~51), extract
   `bin, ok := prereq.ResolvedBinary(results, buildinfo.ToolSafehouse)`; the
   gate guarantees `ok` on the launch path; set it on the cage inputs at the
   `Build` call site (~line 145).
3. **Tests**:
   - `internal/cage/cage_test.go`: `Inputs.Binary` set ⇒ `Path` honors it;
     unset ⇒ `Path == Binary` (golden `invocation.golden.json` unchanged).
   - `internal/cli/run_test.go`: existing fixture scripts
     `WithTool("agent-safehouse", ...)` and asserts `started[0].Name ==
     "safehouse"` — **this expectation flips**: with only the fallback name
     installed, the launch must now use `"agent-safehouse"`. Add/adjust cases:
     only `safehouse` installed ⇒ started name `safehouse`; only
     `agent-safehouse` ⇒ started name `agent-safehouse`; both ⇒ `safehouse`.

### Success criteria

- [ ] Automated: `make test`, `make lint`, `go build ./...` green.
- [ ] Automated: `internal/cage/testdata/invocation.golden.json` unchanged
      (default path) — confirms no-regression for integration callers.

## Phase 3: CLI surface — version constants, testscripts, doctor goldens

### Changes

1. **`internal/cli/version.go`**: loop over
   `[]string{buildinfo.ToolSafehouse, buildinfo.ToolMitmproxy}` (pure literal
   de-duplication; output byte-identical).
2. **testscripts** (`internal/cli/testdata/script/`):
   - New `doctor_brew_binary.txtar`: stub `-- bin/safehouse --` (echo
     `Agent Safehouse 0.10.1`), assert doctor output contains
     `via safehouse` and exits healthy. Mirrors the user's real machine.
   - `doctor_healthy.txtar` keeps stubbing `bin/agent-safehouse` (now covers
     the fallback name end-to-end; output unchanged — no `via` annotation since
     resolved == canonical label).
   - `run_missing_prereq.txtar` / `doctor_missing.txtar`: unchanged (assert the
     canonical missing label + brew hint still renders).
3. **`internal/doctor` goldens**: expected unchanged (render tests construct
   results without `ResolvedName`); verify, regen only if a test gains a
   resolved case.
4. Final sweep: `grep -rn '"agent-safehouse"\|"safehouse"' internal --include="*.go"`
   — every remaining executable-name literal outside tests must come from
   `buildinfo` (YAML key / comments exempt).

### Success criteria

- [ ] Automated: `make test`, `make lint`, `go build ./...` green; `make golden`
      produces no unexpected diff.
- [ ] Manual: on the user's machine (organAIze.eu project,
      `/opt/homebrew/bin/safehouse` 0.10.1): `agent-creance run` passes the
      prereq gate and launches; `agent-creance doctor` shows
      `installed 0.10.1 via safehouse`.

## Testing strategy

- Pure resolution logic → table-driven tests in `internal/prereq` (project
  convention; FakeCommander `Paths`/`Outputs` scripting).
- Report text → golden files with `-update` (review every regen diff).
- End-to-end CLI behavior → hermetic testscript `.txtar` (PATH stubs; the new
  `bin/safehouse` stub mirrors the brew install).
- Launch wiring → `FakeProcessGroup.Started()` name assertions in
  `internal/cli/run_test.go` (the one place check-vs-exec agreement is visible).
- Integration tests already use `cage.Binary`/`safehouse` and keep working; no
  new integration tests (no new real-tool behavior).

## Ticket AC coverage map

| Acceptance criterion | Covered by |
|---|---|
| only `safehouse` ⇒ run passes + launches via it | Phase 1 table + Phase 2 run_test + `doctor_brew_binary.txtar` |
| only `agent-safehouse` ⇒ run passes + launches via it | Phase 1 table + Phase 2 run_test + `doctor_healthy.txtar` |
| both ⇒ `safehouse` wins everywhere | Phase 1 + Phase 2 "both" cases |
| neither ⇒ brew hint refusal | Phase 1 missing case + `run_missing_prereq.txtar` |
| check and launch use same binary | Phase 2 (`ResolvedBinary` → `Inputs.Binary`) |
| doctor/version report resolved name | Phase 1 report annotation (doctor); version: static per checkpoint decision |
| version detection/skew works on resolved binary | Phase 1 (`Output` via resolved name; banner `Agent Safehouse 0.10.1` parse already covered) |

## References

- `internal/prereq/prereq.go:36-90`, `internal/prereq/report.go:20-76`
- `internal/cage/cage.go:36-38,119`, `internal/cage/run.go:53-57`
- `internal/cli/run.go:51-56,137-154`, `internal/cli/version.go:23`
- `internal/buildinfo/buildinfo.go:34-37`
- Research doc §6 for the full golden/txtar inventory.
