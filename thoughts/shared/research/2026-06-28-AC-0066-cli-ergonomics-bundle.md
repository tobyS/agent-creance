---
date: 2026-06-28
ticket: AC-0066
title: "CLI ergonomics bundle (S5-S8) — research"
status: complete
git_commit: 21edde3e0399c04a714fe93de241cb06a54f8223
branch: main
---

# Research: AC-0066 — CLI ergonomics bundle (S5–S8)

## Research question

How are the four UX-audit ergonomics gaps (S5 setup→init orientation, S6
error remediation, S7 machine-readable output, S8 completion docs) implemented
today, what existing patterns should each fix mirror, and what concrete code
seams exist for the changes? Source: `thoughts/shared/research/2026-06-25-ux-audit.md`,
findings S5–S8.

## Summary

All four are small, additive changes that slot into existing, well-tested
rendering/error discipline — no re-architecture needed.

- **S5** — `setup` ends with no "Next:" pointer. `init` already prints `Next:`
  hints (`init.go:140,142`); the fix mirrors that style. The catch: `runSetup`
  is *shared* between the `setup` command and `init`'s inline gate, so the new
  line must go in the `setup` command's `RunE` closure (which `init` never
  calls), not inside `runSetup`.
- **S6** — Two existing hint patterns exist to mirror: (a) up-front refusals
  print a full remediation message to stdout and return a terse sentinel error;
  (b) the proxy-crash path embeds the hint *inline in the error string*
  (`… (try \`agent-creance doctor\`)`, `lifecycle.go:209`). The actionable run
  wraps are `compile policy`, `load config` (→ config/`init`) and `start proxy`
  + its sub-errors (→ `doctor`); the rest are internal. Config validation
  errors are aggregated in a golden-tested `*ValidationError` (`Issues []string`);
  appending a "valid form:" line means extending those message strings.
- **S7** — The `--json` pattern on `policy` (BoolVar + `if asJSON` branch +
  `json.MarshalIndent(x,"","  ")` + trailing newline) is the template. The
  `doctor.Report` / `status.Report` structs back the human renderers but have
  **no JSON tags** and use an untagged `int` `Status` enum, so direct marshaling
  is ugly; `render.go` already establishes the convention of dedicated
  lowercase-tagged *output structs* (ExplainJSON/RefreshJSON). `run --quiet`
  has no ready-made silent printer (the `Nop` type lacks the step methods); the
  clean lever is the printer's `io.Writer` (pass `io.Discard`).
- **S8** — The `completion` command is auto-registered and works; the work is
  *document + surface*, not enable. README has no mention; root has no `Long:`.
  Command **grouping** (placing `completion` sensibly) is explicitly AC-0064's
  job — this ticket is docs.

## Detailed findings

### S5 — `setup`→`init` orientation (`internal/cli/setup.go`, `init.go`)

- Both commands write human output to `app.Stdout` via `fmt.Fprintln`/`Fprintf`,
  marking success with `app.OutStyle.OK("✓")`. There is **no shared "next-step
  hint" helper** in the package.
- The `setup` command is a thin wrapper: `newSetupCmd`'s `RunE`
  (`setup.go:31-33`) calls `runSetup(ctx, app, noSkill, noCAInstall, noGlobalConfig)`.
  `runSetup` (`setup.go:48-99`) runs CA → skill → global-config steps; its last
  printed line is the global-config "Wrote …/left untouched" line from
  `scaffoldGlobalConfig` (`setup.go:105-147`, terminal returns at `:96`/`:98`).
  **No "Next:" pointer anywhere.**
- **Critical seam:** `runSetup` is *also* invoked by `init`'s host-setup gate —
  `ensureHostSetup` → `runSetup(ctx, app, false, false, false)` (`init.go:193`).
  A "Next: run `agent-creance init`" line added *inside* `runSetup` would
  wrongly print during `init`. The only seam that fires for the standalone
  command is the `setup` `RunE` closure (`setup.go:31-33`), after `runSetup`
  returns nil — `init` never goes through that closure.
- `init` already orients forward (`init.go:139-143`):
  ```go
  if noSetup {
      fmt.Fprintln(app.Stdout, "Next: run `agent-creance setup`, then `agent-creance run`.")
  } else {
      fmt.Fprintln(app.Stdout, "Next: run `agent-creance run`.")
  }
  ```
  This is the style/wording to mirror.
- **Tests that pin current output** (will need updating): `init_test.go:219,417,439,510`
  and `testdata/script/init.txtar:29`. `setup` has its own testscript coverage to
  extend. Audit-suggested wording: `Next: \`agent-creance init\` in your project`.

### S6a — `run` startup error wraps (`internal/cli/run.go`, `internal/proxy/lifecycle.go`)

- **Central printer:** `Main` (`cli.go:169-173`) renders every `RunE` error as
  `error: <err.Error()>` on stderr. Root sets `SilenceUsage`/`SilenceErrors`
  (`cli.go:93-94`). There is **no error type, no `errors.As` switch, no
  hint-aware renderer** — whatever text is in the error is what the user sees.
- **Existing pattern A (up-front refusals):** print the full remediation message
  to `app.Stdout`, return a terse sentinel error. E.g. `run.go:79-93`
  (`setup incomplete`, `credential unavailable`) and `run.go:106-110`
  (`project not initialized` + `msgNoProjectConfig`). The hint lives in
  `Result.Message()` constants (`setupcheck.go:83-90`, `cred.go:87-95`,
  `run.go:38`), not in any error type.
- **Existing pattern B (inline hint):** the proxy crash path embeds the pointer
  in the error string — `lifecycle.go:208-210`:
  ```go
  return fmt.Errorf("proxy: mitmproxy (pid %d) exited during startup; check the compiled policy and CA setup (try `agent-creance doctor`)", pid)
  ```
  Format to mirror: `<what happened>; <what to check> (try \`agent-creance doctor\`)`.
- **Enumeration of the eight named wraps** (all bare `fmt.Errorf("<label>: %w", err)`):

  | Step | file:line | Actionable? |
  |------|-----------|-------------|
  | resolve project | `run.go:117` | Internal (state identity) |
  | init compiler | `run.go:134` | Internal (cache dir) |
  | compile policy | `run.go:138` | **Yes** → config/`init` (missing-config gated up front; remaining = malformed config / generator fetch) |
  | load config | `run.go:149` | **Yes** → config/`init` |
  | compile profile | `run.go:156` | Internal (SBPL baseline) |
  | extract enforcer | `run.go:163` | Internal (embed write) |
  | start proxy | `run.go:194` | **Yes** → `doctor` (wraps `mgr.Attach`) |
  | resolve cage inputs | `run.go:210` | Internal (home/path) |

- The un-hinted proxy sub-errors that reach `start proxy: …`: spawn failure
  (`lifecycle.go:165-168`), **readiness timeout** (`lifecycle.go:215`, the one
  S6 calls out). The crash path (`:209`) already has the hint.

### S6b — config validation & loader errors (`internal/config/`)

- **All schema errors** go through `verr.add(fmt, …)` on a single
  `*ValidationError` (`errors.go:39-54`): `Issues []string`, rendered as a
  bulleted list under `invalid .agent-creance.yaml:`. Validation does **not**
  stop at the first issue. `add` runs `Sprintf` immediately, so there is no
  per-issue struct (field/value/suggestion) — the message string is the only
  structure. Doc comment notes messages are intentionally **stable for golden
  tests**.
- The named common errors (`validate.go`):
  - passthrough-with-paths — `:37-39` (`"… uses mode: passthrough, which cannot carry paths"`)
  - passthrough-with-methods — `:40-42`
  - unknown mode — `:43-44` (already names valid options: `want "intercept" or "passthrough"`)
  - invalid host — `:197-200` (+ `ValidateHost` reasons `:151-177`)
  - invalid methods — `:201-205` (+ `ValidateMethods` reasons `:181-194`)
- **Bare loader errors:** `load.go:154,212,281` (`config: file not found: …`,
  `config: include not found: …`). Partial good example to extend:
  include-out-of-scope names allowed scopes (`load.go:320`).
- **No existing hint mechanism.** Adding a "valid form:" example means either
  appending to the message string in `verr.add(...)` calls, or extending
  `*ValidationError` to optionally carry a hint per issue (more invasive, breaks
  golden simplicity).
- **Corrected forms** (for the "valid form:" examples), from `config.go:89-101`
  and `docs/design.md:250-284`:
  - `intercept` (default): may carry `paths`/`methods`. Passthrough may **not**.
    Valid passthrough form: `- host: api.anthropic.com` `  mode: passthrough`.
  - Modes: `intercept` / `passthrough` (default `intercept`).
  - Host: bare hostname, `*`, or `*.suffix`; labels `[a-zA-Z0-9_-]`.
  - Methods: uppercase verbs from `GET HEAD POST PUT PATCH DELETE OPTIONS CONNECT TRACE` (case-sensitive).

### S7 — machine-readable output (`--json`, `--quiet`)

- **Pattern to mirror** (`policy.go:32-61,89,126`): local `var asJSON bool`;
  `cmd.Flags().BoolVar(&asJSON, "json", false, "…")`; `if asJSON { out, err :=
  render.XJSON(...); fmt.Fprint(app.Stdout, out) }`. JSON renderers in
  `render.go` use `json.MarshalIndent(x, "", "  ")`, wrap marshal failures as
  `render: marshal …: %w`, append `"\n"`. `RefreshJSON`/`ExplainJSON` build
  **dedicated lowercase-tagged output structs** (`render.go:188-192,238-249`)
  rather than marshaling internal types; `ShowJSON` marshals the already-tagged
  `policy.Compiled` directly.
- **`doctor --json`:** `doctor.Report` (`report.go:42-50`) + sub-sections
  (`:53-92`) have exported fields but **no JSON tags**; `Status` is an untagged
  `int` enum (`:28-38`, would serialize as `0/1/2/3`); `ProxySection` embeds the
  tag-less `proxy.Diagnosis` (`lifecycle.go:255-280`). Branch goes in `runDoctor`
  (`doctor.go:40-67`) between `chk.Run` (`:57`) and `Render` (`:61`).
  **Exit-code semantics must be preserved:** `runDoctor` returns non-nil when
  `rep.Actionable()` non-empty (`:63-65`, → exit 1). So `--json` must still emit
  JSON *and* return the same error / exit code.
- **`status --json`:** `status.Report`/`ProjectStatus` (`report.go:14-26`) — no
  JSON tags, embeds tag-less `proxy.Diagnosis`. Human render special-cases empty
  (`"No active cages.\n"`). `status` always exits 0 (except scan error). Branch
  in `runStatus` (`status.go:30-42`) between `Scan()` and `Render`.
- **`run --quiet`:** progress printer built at `run.go:124-125` —
  `prog := progress.NewPrinter(app.Stderr, app.Clock, app.Terminal.IsStderrTerminal(), app.ErrStyle)`.
  `prog` is a *concrete* `*Printer`; `run` calls step methods on it directly
  (`StepStart`/`StepDone`/`Line`/`Close`, `run.go:131,141,143,154,158,186,196,265`)
  and also passes it as the compiler `Reporter` (`run.go:132`). The existing
  `progress.Nop` (`progress.go:45-61`) implements only the `Reporter` interface,
  **not** the step methods — it cannot be swapped in. Cleanest lever: progress
  goes through the printer's `io.Writer` (`printer.go:66`, every step writes via
  `p.w`), so constructing with `io.Discard` when `--quiet` silences all step
  output with no other change. No `--quiet` flag exists on `newRunCmd` today
  (`run.go:49-58`).

### S8 — shell completion docs (`internal/cli/cli.go`, `README.md`)

- `completion` is **auto-registered** (no `CompletionOptions`/`DisableDefaultCmd`
  anywhere); confirmed working (audit Open Question resolved 2026-06-27). Not in
  the explicit `AddCommand` list (`cli.go:119-134`).
- **No command grouping** (`AddGroup`/`GroupID` unused). `completion` sits
  alphabetized between `clean` and `deny`. Grouping/placement is **AC-0064's
  scope** (this ticket's Out of Scope confirms it) — S8 here is documentation.
- **README** (`README.md`, 112 lines): sections Requirements / Egress baseline /
  First-run config / Development / License. **No completion mention.** A
  "Shell completion" `##` section fits among the usage sections (e.g. after
  "First-run config").
- Root command has **no `Long:`** (`cli.go:90-112`, only `Short:`). The only
  `Long:` in the codebase is `include.go:23-26`; **no `Example:` anywhere**. So
  there's no in-repo precedent for a step-by-step help block — README is the
  natural home; a root `Long:` sentence is optional (and overlaps AC-0064).
- Audit guidance: "document + surface … with an install hint" (how to load the
  generated script per shell). No specific copy prescribed.

## Code references

- `internal/cli/setup.go:31-33,48-99,105-147` — `setup` RunE seam, `runSetup`, `scaffoldGlobalConfig`
- `internal/cli/init.go:139-143,193` — existing `Next:` hints; inline `runSetup` call
- `internal/cli/cli.go:90-112,119-134,169-173` — root cmd, AddCommand list, central error printer
- `internal/cli/run.go:117,134,138,149,156,163,194,210` — the eight step wraps; `124-132` printer/compiler wiring
- `internal/proxy/lifecycle.go:208-210,215` — hint pattern + bare readiness timeout
- `internal/config/validate.go:37-44,197-205,151-194` — common validation errors + reason helpers
- `internal/config/errors.go:39-54` — `*ValidationError` aggregator
- `internal/config/load.go:154,212,281,320` — bare loader errors + the partial good example
- `internal/config/config.go:89-101` — `Rule` struct + mode constants (corrected forms)
- `internal/cli/policy.go:32-61,89,126` — `--json` pattern to mirror
- `internal/policy/render/render.go:62-68,188-210,238-272` — JSON renderer convention (output structs)
- `internal/cli/doctor.go:40-67`, `internal/doctor/report.go:28-113` — doctor report + Actionable/exit code
- `internal/cli/status.go:30-42`, `internal/status/report.go:14-53` — status report + render
- `internal/progress/printer.go:66-109`, `internal/progress/progress.go:45-70` — Printer vs Nop
- `README.md` — sections; no completion mention
- `docs/design.md:250-284` — enforcement modes / config schema

## Open questions for planning

1. **JSON shape (S7).** Add JSON tags to the internal `doctor.Report` /
   `status.Report` (+ `proxy.Diagnosis`) structs, or build dedicated
   lowercase-tagged output structs in a `render`/serialize helper (the
   established `render.go` convention)? The internal structs use an untagged
   `int` `Status` enum that would serialize as integers; dedicated output
   structs give a clean, stable, string-valued schema. **Leaning: dedicated
   output structs.**
2. **Scope (S7 split).** Keep S5–S8 as one ticket/implementation, or split S7
   (JSON + `--quiet`) into its own ticket as the ticket's planning question
   raises? **Leaning: keep as one** (each part is small and independently
   shippable as a phase).
3. **Completion docs depth (S8).** README "Shell completion" section with
   per-shell *persistence* install hints (bash/zsh), or a minimal mention of
   `agent-creance completion <shell>`? **Leaning: per-shell hints** (audience is
   shell-native). And: add a root `Long:` sentence here, or leave that to
   AC-0064?

## Related research

- `thoughts/shared/research/2026-06-25-ux-audit.md` — source audit (S5–S8)
- `thoughts/shared/tickets/AC-0064-help-as-doc-surface.md` — owns command
  grouping & root `Long:` overhaul (adjacent to S5/S8)
