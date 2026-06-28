# AC-0066: CLI ergonomics bundle — setup orientation, error remediation, machine-readable output, completion

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-27
**Updated:** 2026-06-28

## Problem Statement

The 2026-06-25 UX audit
(`thoughts/shared/research/2026-06-25-ux-audit.md`, findings S5–S8) found four
smaller ergonomics gaps. They are bundled here because each is individually small;
they may be split during planning if any one grows.

- **S5 — no `setup`↔`init` orientation.** The host-once (`setup`) vs project-once
  (`init`) split is a classic confusion point and nothing orients the user to it.
  `setup` prints per-step `✓` lines and returns with **no final "Next:" pointer**
  to `init` (`internal/cli/setup.go`); `init` chains setup inline
  (`internal/cli/init.go:158-197`) but a user who starts at `setup` is left without
  a next step.
- **S6 — a bare second tier of errors with no remediation.** The up-front
  precondition refusals are excellent, but once past them the error quality drops
  to raw Go wraps: `run`'s step errors (`resolve project`, `init compiler`,
  `compile policy`, `load config`, `compile profile`, `extract enforcer`,
  `start proxy`, `resolve cage inputs` — `internal/cli/run.go:90-183`) carry no
  next-step pointer (the proxy *crash* path points at `doctor`,
  `internal/proxy/lifecycle.go:209`, but the generic `start proxy` wrap and the
  readiness timeout `lifecycle.go:215` do not). Config validation pinpoints the
  offending field but never shows the corrected form (e.g.
  `egress <list> rule <n> uses mode: passthrough, which cannot carry paths` —
  `internal/config/validate.go:38`), and loader errors are bare
  (`internal/config/load.go:154,212,281`).
- **S7 — thin machine-readability for a scripting audience.** `--json` exists
  **only** on the three `policy` subcommands
  (`internal/cli/policy.go:59,89,126`); `doctor` and `status` have no
  machine-readable mode (both already build structured reports internally), and
  there is **no `--quiet`/`--verbose`** anywhere. Scripting "is a cage running / is
  the host healthy" means parsing human tables, and a CI `run` always emits
  progress (degraded but present) with no way to silence it.
- **S8 — shell completion is not surfaced.** Cobra's `completion` command already
  exists and works (verified: it is listed in `agent-creance help` and emits a
  valid script for `completion zsh`), but it is undocumented and buried —
  alphabetized between `clean` and `deny` with no install hint and no mention in
  the README. For a shell-native audience this high-value feature is effectively
  hidden.

## Desired Outcome

The everyday CLI experience gets sharper at the edges: `setup` ends by pointing
at the next step; the high-traffic `run`/config errors carry a remediation hint or
a corrected-form example; `doctor` and `status` can emit machine-readable JSON and
`run` can be silenced for CI; and the already-working shell completion is
documented and discoverable.

## User Stories / Use Cases

- As a first-time user who just ran `setup`, I want it to tell me to run
  `agent-creance init` next, so that I'm not left guessing the next step.
- As a user whose `run` fails mid-startup, I want the error to point me at
  `agent-creance doctor` (or the relevant fix), so that I can diagnose it instead
  of reading a raw wrap.
- As a user with an invalid `.agent-creance.yaml`, I want the validation error to
  show the corrected form, so that I can fix it without opening the design doc.
- As an engineer scripting around the tool, I want `doctor --json` and
  `status --json`, so that I can check health/running cages from a script without
  parsing tables.
- As an engineer running `run` in CI, I want `--quiet`, so that the logs aren't
  filled with progress output.
- As a shell user, I want documented shell completion, so that I can tab-complete
  commands and flags.

## Acceptance Criteria

- [ ] `agent-creance setup` ends with a clear next-step line pointing at
      `agent-creance init` (and the overall happy path).
- [ ] The high-traffic `run` startup errors carry a remediation pointer where one
      applies — at minimum the config-load and proxy-start/readiness paths point
      at `agent-creance doctor` (or `init`, as appropriate).
- [ ] The most common config validation errors (e.g. passthrough-with-paths,
      unknown mode, malformed host) include a short corrected-form example or a
      pointer to valid syntax.
- [ ] `agent-creance doctor --json` emits the diagnostic report as machine-readable
      JSON; exit code semantics are preserved.
- [ ] `agent-creance status --json` emits the running-cages report as
      machine-readable JSON.
- [ ] `agent-creance run --quiet` suppresses the startup progress output (the
      agent's own output is unaffected; errors are still shown).
- [ ] Shell completion is documented (how to enable it for at least bash/zsh) in
      the README and/or root help, and surfaced rather than buried.

## Out of Scope

- A global `--json` / `--quiet` on *every* command — only the commands named
  above.
- `--verbose` (no current need identified; can follow if a use case appears).
- Re-architecting the error types broadly — this is targeted hint-threading on
  the high-traffic paths, not an error-handling overhaul.
- The in-CLI `--help` content overhaul (root `Long:`, per-command examples,
  grouping) — tracked in AC-0064 (this ticket may place `completion` sensibly but
  the grouping work itself is AC-0064).

## Open Questions

None — well-understood from the audit; the completion command's existing presence
was verified in the built binary.

## Questions for Research/Planning

- [ ] Should this stay one ticket or split — most likely S7 (JSON/`--quiet`) as
      its own ticket if it grows beyond a thin serialization of existing reports?
- [ ] What is the JSON shape for `doctor`/`status` — derive from the existing
      report structs, or define a stable public schema? (Reuse the structs that
      already back the human renderers in `internal/doctor` / `internal/status`.)
- [ ] For S6, which `run` wraps genuinely have an actionable remediation vs. which
      are truly internal (where a `doctor` pointer would be noise)?
- [ ] For completion docs (S8): just document `agent-creance completion <shell>`,
      or also note how to persist it per shell?

## References

- `thoughts/shared/research/2026-06-25-ux-audit.md` — UX audit, findings S5–S8
  (and the resolved Open Question confirming `completion` already works).
- `internal/cli/setup.go` — `setup` output (missing final "Next:" pointer).
- `internal/cli/run.go:90-183` — the bare step-error wraps.
- `internal/config/validate.go:26-193`, `internal/config/load.go:154,212,281` —
  fix-less validation and loader errors.
- `internal/cli/policy.go:59,89,126` — the only `--json` surfaces today.
- `internal/doctor/report.go`, `internal/status/report.go` — structured reports
  to serialize for `--json`.
- `internal/progress/printer.go` — the `run` progress output `--quiet` would
  suppress.

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-27
Created from UX audit findings S5–S8, combined per request. Complexity Medium and
deliberately heterogeneous: S5/S8 are tiny (a pointer line; docs for an
already-working command), S6 is targeted hint-threading, and S7 (JSON + quiet) is
the heaviest sub-item and the likeliest split candidate if it grows. The audit's
follow-up verification confirmed the `completion` command already exists, so S8 is
"document + surface," not "enable."
