---
date: 2026-06-28
ticket: AC-0066
title: "CLI ergonomics bundle (S5-S8) — implementation plan"
status: ready
research: thoughts/shared/research/2026-06-28-AC-0066-cli-ergonomics-bundle.md
---

# AC-0066 — CLI ergonomics bundle (S5–S8): implementation plan

## Overview

Four small, additive ergonomics fixes from the 2026-06-25 UX audit, kept in one
ticket and shipped as independent phases:

- **S5** — `setup` ends with a `Next: run agent-creance init` pointer.
- **S6** — actionable `run`/proxy startup errors and the common config-validation
  errors gain a remediation hint / corrected-form example.
- **S7** — `doctor --json`, `status --json` (dedicated string-valued output
  structs), and `run --quiet`.
- **S8** — shell completion documented in the README with per-shell install/persist
  hints.

Decisions locked at the question checkpoint: dedicated JSON output structs (not
tags on internal structs); keep S5–S8 as one ticket; per-shell completion
persistence hints; **no** root `Long:` here (that overlaps AC-0064).

## Current state

- `setup` (`internal/cli/setup.go:31-99`) ends after its global-config step with
  no next-step pointer. `runSetup` is shared with `init`'s gate
  (`init.go:193`), so a `Next:` line must live in the `setup` command's `RunE`
  closure, not in `runSetup`. `init` already prints `Next:` hints
  (`init.go:139-143`) — the style to mirror.
- `run` startup wraps are bare `fmt.Errorf("<label>: %w", err)`
  (`run.go:117,134,138,149,156,163,194,210`); the single error printer is
  `cli.go:169-173` (`error: <err>`). The proxy-crash path already embeds an
  inline hint (`lifecycle.go:209`); the spawn (`:167`) and readiness-timeout
  (`:215`) paths do not.
- Config validation aggregates into `*ValidationError` (`errors.go:39-54`,
  `Issues []string`), rendered as a bullet list; messages are golden-/table-
  tested and intentionally stable. Loader errors are bare (`load.go:154,212,281`).
- `--json` exists only on `policy` (`policy.go:59,89,126`), branching to
  `render.*JSON` (`render/render.go`), `json.MarshalIndent(x,"","  ")` + trailing
  newline. `doctor.Report`/`status.Report` have no JSON tags and an untagged
  `int` `Status` enum. `run` has no `--quiet`; progress goes through
  `progress.NewPrinter(app.Stderr, …)` (`run.go:124`).
- `completion` is auto-registered and works but is undocumented; README has no
  mention.

## Desired end state

Each acceptance criterion in the ticket is met: `setup` points at `init`; the
config-load and proxy-start/readiness `run` errors point at `doctor`/`init`; the
common config-validation errors show a corrected form; `doctor --json` and
`status --json` emit stable machine-readable JSON with exit codes preserved;
`run --quiet` silences startup progress (agent output and errors unaffected);
shell completion is documented in the README with per-shell install steps.

## What we're NOT doing

- No global `--json`/`--quiet` on every command — only the named ones.
- No `--verbose`.
- No broad error-type refactor — targeted hint-threading only.
- No command grouping and no root `Long:` — both belong to AC-0064.
- No change to `policy`'s existing `--json` surfaces.

---

## Phase 1 — S5: `setup` → `init` next-step pointer

### Changes

1. `internal/cli/setup.go` — in `newSetupCmd`'s `RunE` closure (`setup.go:31-33`),
   after `runSetup(...)` returns nil, print a next-step line to `app.Stdout`:
   `Next: run \`agent-creance init\` in your project.` (plain `fmt.Fprintln`,
   matching `init.go:140/142`; not styled). Placing it in the closure — not in
   `runSetup` — keeps it out of `init`'s inline setup chaining.

### Tests

- `internal/cli/setup_test.go` — extend the existing unit test to assert the
  `Next:` line appears on a successful `setup` run.
- `internal/cli/init_test.go` — confirm the line does **not** leak into `init`'s
  output (init has its own `Next:` hints); add/adjust an assertion if the shared
  path makes this non-obvious.

### Success criteria

#### Automated
- [ ] `make test` passes.
- [ ] `make lint` clean.
- [ ] `go build ./...` succeeds.

#### Manual
- [ ] `bin/agent-creance setup` (or its test harness) ends with the `init` pointer; `init` output is unchanged.

---

## Phase 2 — S6: `run`/proxy startup error remediation hints

### Changes

1. `internal/proxy/lifecycle.go` — add the inline `doctor` hint to the two
   un-hinted proxy startup paths, mirroring the crash-path format at `:209`
   (`<what happened>; <what to check> (try \`agent-creance doctor\`)`):
   - readiness timeout (`:215`): append `(try \`agent-creance doctor\`)`.
   - spawn failure (`:165-168`): append the same pointer.
2. `internal/cli/run.go` — thread a remediation pointer into the two actionable
   config wraps. Rather than blanket-suffixing (which would stack on the
   validation hints from Phase 3), append a concise pointer to the user-facing
   surfaces:
   - `load config` (`run.go:149`) and `compile policy` (`run.go:138`): append a
     hint pointing at the config file / `agent-creance doctor`
     (e.g. `… (check .agent-creance.yaml; try \`agent-creance doctor\`)`).
   - Leave the genuinely-internal wraps (`resolve project`, `init compiler`,
     `compile profile`, `extract enforcer`, `resolve cage inputs`) unhinted.
   - The `start proxy` wrap (`run.go:194`) needs no change — the hint now rides
     up from `lifecycle.go` via `%w`.

   Note during implementation: confirm the Phase-3 validation hint and a
   Phase-2 wrap hint don't read as redundant when a malformed config surfaces
   through `compile policy`/`load config`. If they stack awkwardly, prefer the
   validation message carrying the corrected form and keep the run-wrap pointer
   to `doctor` only (diagnostic, not corrective).

### Tests

- `internal/proxy/` — extend existing lifecycle unit tests (if any cover the
  timeout/spawn paths) to assert the hint substring; otherwise add a focused
  test for the error strings. Real-tool proxy startup is integration-only, so
  test the error-formatting at the unit level where possible.
- `internal/cli/run_test.go` / a testscript under `testdata/script/` — assert
  the config-wrap hint appears for a malformed/invalid project config on `run`
  (without invoking real external tools — the config load/compile step fails
  before proxy start).

### Success criteria

#### Automated
- [ ] `make test` passes.
- [ ] `make lint` clean.
- [ ] `go build ./...` succeeds.

#### Manual
- [ ] A forced proxy readiness timeout / spawn failure shows the `doctor` pointer; a malformed config on `run` shows the config/`doctor` pointer.

---

## Phase 3 — S6: config-validation corrected-form hints

### Changes

1. `internal/config/validate.go` — append a short corrected-form clause to the
   most common, hand-edit-prone validation messages (keeping them single-line and
   golden-stable):
   - passthrough-with-paths (`:37-39`) and passthrough-with-methods (`:40-42`):
     add `(valid form: use mode: intercept to filter by path/method, or drop
     them for a passthrough tunnel)`.
   - invalid host (`:197-200`): append a pointer to the valid host syntax
     (bare hostname, `*`, or `*.suffix`).
   - invalid/non-uppercase method (`:201-205` via `ValidateMethods`): the reason
     already names the issue; append the allowed uppercase verb set if concise.
   - unknown mode (`:43-44`) already names valid options — leave as-is.
2. `internal/config/load.go` — leave the bare loader IO errors (`:154,212,281`)
   essentially as-is (a missing file is already gated up front by `run`), but if
   trivially helpful, confirm the include-out-of-scope precedent (`:320`) covers
   the actionable case. No corrected-form example applies to a missing-file IO
   error. (Keep this phase focused on validation messages.)

Decision: extend the message **strings** (simplest, golden-stable) rather than
adding a hint field to `*ValidationError` — the struct's flat `[]string` design
and golden-test discipline favor self-contained messages.

### Tests

- `internal/config/validate_test.go` — update the table cases asserting these
  message strings to expect the new corrected-form clause.
- Any golden fixtures that pin these messages (search and `make golden`, then
  review the diff).

### Success criteria

#### Automated
- [ ] `make test` passes.
- [ ] `make lint` clean.
- [ ] `make golden` produces only the intended message diffs (reviewed).

#### Manual
- [ ] An `.agent-creance.yaml` with `mode: passthrough` + `paths:` reports the error plus a corrected-form hint.

---

## Phase 4 — S7: `doctor --json` and `status --json`

### Changes

1. `internal/doctor/report.go` — add a `RenderJSON(r Report) (string, error)`
   that builds a dedicated lowercase-tagged output struct mirroring the
   `render/render.go` convention: map the `int` `Status` enum to strings
   (`ok`/`warn`/`problem`/`skipped`), flatten the sub-sections and the embedded
   `proxy.Diagnosis` into stable JSON fields, `json.MarshalIndent(_,"","  ")`,
   wrap marshal errors as `render: marshal …: %w`, append `"\n"`.
2. `internal/cli/doctor.go` — add `var asJSON bool` + `cmd.Flags().BoolVar(&asJSON,
   "json", false, "emit the diagnostic report as JSON")`. In `runDoctor`, after
   `chk.Run` (`:57`): if `asJSON`, print `doctor.RenderJSON(rep)` instead of the
   human `Render`, then **still** run the `rep.Actionable()` check and return the
   same error (preserving exit-code semantics).
3. `internal/status/report.go` — add a `RenderJSON(r Report) (string, error)`
   building a string-valued output struct (reuse the `stateLabel`
   `orphan`/`stranded`/`running`/`down` mapping; include hash, port). Empty set
   serializes as `[]`, not `null` (pre-allocate the slice, per `RefreshJSON`
   precedent).
4. `internal/cli/status.go` — add the `--json` flag + branch between `Scan()`
   and `Render` (`status.go:36-40`); `status` keeps exiting 0 except on scan
   error.

### Tests

- `internal/doctor/report_test.go` — add golden cases `render_*.json.golden`
  for healthy/problems/stranded (mirror the existing `render_*.golden` set and
  the `policy/render` `.json.golden` convention), driven by the `-update` flag.
- `internal/status/report_test.go` — add `render_*.json.golden` for
  empty/mixed/running.
- `internal/cli/doctor_test.go` / `status_test.go` — assert `--json` emits valid
  JSON and that `doctor --json` preserves the non-zero exit on actionable
  problems. Optionally add/extend a testscript (`doctor_*`/`status_*.txtar`).

### Success criteria

#### Automated
- [ ] `make test` passes (including new golden tests).
- [ ] `make golden` diff reviewed.
- [ ] `make lint` clean; `go build ./...` succeeds.

#### Manual
- [ ] `bin/agent-creance doctor --json | jq .` parses; exit code matches the human run.
- [ ] `bin/agent-creance status --json | jq .` parses (incl. the no-cages `[]` case).

---

## Phase 5 — S7: `run --quiet`

### Changes

1. `internal/cli/run.go` — add `var quiet bool` to `newRunCmd` +
   `cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress startup progress output (errors and agent output are unaffected)")`.
   Thread `quiet` into `runRun`. At the printer construction (`run.go:124`),
   choose the writer: `w := app.Stderr; if quiet { w = io.Discard }` and pass `w`
   to `progress.NewPrinter`. This silences both the run step lines and the
   compiler `Reporter` events (same `prog`). Errors still flow through
   `cli.go:171` to stderr; the agent's stdout is untouched.

### Tests

- `internal/cli/run_test.go` or a testscript — assert that with `--quiet` no
  progress text reaches stderr up to the point external tools would be invoked
  (the test must not require real `mitmproxy`/`agent-safehouse`; assert on the
  pre-proxy progress suppression, e.g. via a failure path that still prints
  progress today). Confirm the flag is registered and documented in `run --help`.

### Success criteria

#### Automated
- [ ] `make test` passes.
- [ ] `make lint` clean; `go build ./...` succeeds.

#### Manual
- [ ] `bin/agent-creance run --quiet` emits no `▶`/step progress on stderr; a forced error still prints `error: …`.

---

## Phase 6 — S8: document shell completion

### Changes

1. `README.md` — add a `## Shell completion` section (placed among the usage
   sections, e.g. after "First-run config (init imports)"). Cover:
   - that completion is built in (`agent-creance completion <shell>`),
   - per-shell generate + persist steps for **bash** and **zsh** (and mention
     `fish`/`powershell` are supported by the same command),
   - a one-time install snippet (e.g. writing the script into the shell's
     completion dir or sourcing it from the rc file).
   No code change (grouping/root `Long:` are AC-0064).

### Tests

- Docs-only; no automated test. Verify `agent-creance completion bash` and
  `completion zsh` still emit valid scripts against `bin/agent-creance`.

### Success criteria

#### Automated
- [ ] `go build ./...` succeeds (no code change, sanity only).

#### Manual
- [ ] README renders; the completion section's commands work against the built binary.

---

## Final verification (before marking done)

- [ ] `make test` green.
- [ ] `make lint` clean.
- [ ] `make build` so `bin/agent-creance` reflects the final commit.
- [ ] `make golden` diff reviewed and intentional.
- [ ] Walk each ticket acceptance criterion against the built binary.
- [ ] Set ticket `**Status:** Done`, bump `**Updated:**`, tick acceptance
      criteria, append a dated note.

## Testing strategy

- Pure logic (config messages, JSON output structs) → table/golden tests with
  `-update`.
- CLI behavior (flags, hints, exit codes) → unit tests on `App` and/or hermetic
  testscript `.txtar` (stub external tools; never invoke real `mitmproxy`/
  `agent-safehouse`/`security`).
- Proxy startup hints tested at the error-formatting level (real startup is
  integration-only).

## Phase commit mapping (commit after each verified phase)

- `feat(AC-0066): point setup at init with a next-step line` (Phase 1)
- `feat(AC-0066): add remediation hints to run/proxy startup errors` (Phase 2)
- `feat(AC-0066): show corrected form in common config validation errors` (Phase 3)
- `feat(AC-0066): add doctor --json and status --json` (Phase 4)
- `feat(AC-0066): add run --quiet to suppress startup progress` (Phase 5)
- `docs(AC-0066): document shell completion in the README` (Phase 6)
