---
date: 2026-06-11
ticket: AC-0039
commit: d72e1c3
branch: main
topic: "Accept both safehouse binary names (safehouse / agent-safehouse)"
status: complete
---

# Research: AC-0039 — Accept both safehouse binary names

**Ticket:** `thoughts/shared/tickets/AC-0039-safehouse-binary-names.md`

## Research question

The prereq check looks up `agent-safehouse` on PATH while the brew formula installs
`safehouse` (which `cage.Binary` already uses), so `run` refuses on a valid install.
How do the prereq check, doctor/version reporting, and the cage launch use the
binary name today, and what is the full change surface for accepting either name
(`safehouse` preferred), resolving once, and using the same resolved binary
everywhere?

## Summary

There are exactly **four independent literals** that must agree today and don't:

1. `prereq.DefaultTools` — `Name: "agent-safehouse"` (`internal/prereq/prereq.go:39`)
2. `cage.Binary = "safehouse"` (`internal/cage/cage.go:38`)
3. The version command's hardcoded key list `[]string{"agent-safehouse", "mitmproxy"}`
   (`internal/cli/version.go:23`)
4. The `buildinfo.TestedVersions` map key `"agent-safehouse"`
   (`internal/buildinfo/buildinfo.go:35`)

`Tool.Name` is used in **three roles**: PATH lookup key, argv[0] for the `--version`
query, and display label in every report. The cage launch performs **no explicit
LookPath** — `exec.CommandContext` resolves the bare name implicitly at `Start`
time, so today the prereq gate and the actual exec can disagree in both directions.

**Ticket correction found during research:** `agent-creance version` does *not*
probe installed tools at all — it only prints `buildinfo.TestedVersions` under a
"tested against:" heading, keyed by the literal at version.go:23. The ticket's AC
"version reports the resolved binary name" is based on a wrong assumption (carried
over from the debugging session); only `doctor` (and run's skew warning) render
install state. → Open question for the checkpoint.

## Detailed findings

### 1. prereq.Check / DefaultTools call flow

- `Tool` struct (`internal/prereq/prereq.go:22-31`); `Name` documented as "the
  executable name as it appears on PATH".
- `DefaultTools` (`prereq.go:36-51`) hardcodes the safehouse entry:
  `Name: "agent-safehouse"`, `VersionArgs: ["--version"]`,
  `Tested: tested["agent-safehouse"]`,
  `InstallHint: "brew install eugene1g/safehouse/agent-safehouse"`.
- `Check` (`prereq.go:65-90`):
  - `cmd.LookPath(t.Name)` at line 69 — error ⇒ `Installed=false`. The resolved
    absolute path is **discarded** (`_, err :=`).
  - `cmd.Output(ctx, t.Name, t.VersionArgs...)` at line 74 — bare name as argv[0].
  - Banner → `parseVersion` (`internal/prereq/version.go:62-75`, regex
    `(\d+)\.(\d+)(?:\.(\d+))?`) → `classify` against `Tested`. The real banner
    `Agent Safehouse 0.10.1` parses fine. Output failure ⇒ `SkewUnparseable`.
- `Missing` (`prereq.go:94-103`) and `MissingInstructions` (`prereq.go:108-132`)
  use `Tool.Name` as display label (the refusal block the user hit).

**Callers:**

- **run** — `internal/cli/run.go:51-56`: `prereq.Check(ctx, app.Commander,
  prereq.DefaultTools(app.Tested))`; non-empty `MissingInstructions` ⇒ print +
  abort `"%d prerequisite(s) missing"`. `warnVersionSkew` (`run.go:161-168`)
  prints `r.Tool.Name` on stderr for loud skews.
- **doctor** — `internal/doctor/doctor.go:41-42` (`Checker.Run` fills
  `Report.Version` / `Report.Missing`); checker built in
  `internal/cli/doctor.go:41-53`; `internal/doctor/report.go` embeds
  `prereq.Report(...)` (line 107) and `Actionable()` counts missing (line 90-92).
- **setup** does *not* call prereq. **version** does not either (see §3).
- `app.Tested` wired from `buildinfo.TestedVersions` at `internal/cli/cli.go:111`.

### 2. cage.Binary and the actual exec

- `const Binary = "safehouse"` (`internal/cage/cage.go:38`, comment: "resolved on
  PATH by the caller that execs it"). `Build` sets `Invocation.Path = Binary`
  (`cage.go:119`).
- Launch: `cage.Runner.Run` (`internal/cage/run.go:53-57`) →
  `sysdep.ProcessGroup.Start(ctx, inv.Env, inv.Path, inv.Args...)` →
  `OSProcessGroup.Start` (`internal/sysdep/processgroup.go:57-74`) →
  `exec.CommandContext` + `cmd.Start()`. **No prior LookPath**; a missing binary
  surfaces as `start "safehouse"` error chain only at launch time.
- CLI call site: `internal/cli/run.go:137-154` (`Resolve` → `Prepare` → `Build` →
  `NewRunner(app.ProcessGroup).Run`).
- `cage.Binary` also read by integration tests
  (`internal/cage/cage_integration_test.go:86-87`,
  `internal/verify/verification_integration_test.go:259`) as a skip-guard.

### 3. version command

`internal/cli/version.go:11-29` prints buildinfo version metadata plus a
"tested against:" list looping over the hardcoded slice
`[]string{"agent-safehouse", "mitmproxy"}` (line 23), printing
`app.Tested[name]`. The literal is a **map key + display label only** — no PATH
probe, no Commander use. A key mismatch with buildinfo would print an empty
version. Covered by `internal/cli/testdata/script/version.txtar:7`
(`stdout 'agent-safehouse'`).

### 4. Report rendering + goldens

- `prereq.Report` (`internal/prereq/report.go:20-48`): row label is
  `Tool.Name + ":"` (line 37; column width from name lengths, lines 27-30);
  `installedField` (lines 51-60) renders `"not installed"` or
  `"installed X, tested against Y"`; `statusField` (lines 63-76) renders
  `✗ missing` / `✓` / `✓ (version unparsed)` / `⚠ <skew>`.
- Goldens pinning the `agent-safehouse` row: `internal/prereq/testdata/doctor_report.golden:2`
  and all four `internal/doctor/testdata/render_*.golden` files (healthy /
  problems / fixed / stranded), driven by `internal/prereq/report_test.go:25-37`
  and `internal/doctor/report_test.go:21-26`.
- Golden pinning the exec name: `internal/cage/testdata/invocation.golden.json:2`
  (`"Path": "safehouse"`).

### 5. sysdep seams (test capability)

- `sysdep.Commander` (`internal/sysdep/commander.go:24-31`): `LookPath(name)`,
  `Output(ctx, name, args...)` (production uses `CombinedOutput` — some tools
  print the banner to stderr).
- `FakeCommander` (`internal/sysdep/sysdeptest/fake.go:18-63`): `Paths` map
  (absent key = not installed), `Outputs` map, `Errs` map, `WithTool(name, path,
  output)` builder. "Only agent-safehouse installed" / "both installed" scenarios
  are directly scriptable with `WithTool` calls — **no fake changes needed**.
- `FakeProcessGroup` (`sysdeptest/processgroup.go:20-90`): records
  `StartedCommand{Name, Args, Env}`; performs no PATH check — `run_test.go:128-134`
  asserts `started[0].Name == "safehouse"` independently of the FakeCommander.

### 6. Full test-artifact inventory (change surface)

Unit tests:

- `internal/prereq/prereq_test.go` — `Tool{Name: "agent-safehouse"}` tables,
  `WithTool("agent-safehouse", ...)`, asserts `Missing() == ["agent-safehouse"]`
  and the brew hint.
- `internal/prereq/report_test.go`, `internal/doctor/report_test.go`,
  `internal/doctor/doctor_test.go`, `internal/cli/doctor_test.go`,
  `internal/cli/run_test.go` (also asserts exec name `"safehouse"`),
  `internal/cage/run_test.go` (`Invocation{Path: "safehouse"}`).

Goldens: `internal/prereq/testdata/doctor_report.golden`, four
`internal/doctor/testdata/render_*.golden`, `internal/cage/testdata/invocation.golden.json`.

testscripts (`internal/cli/testdata/script/`):

- `doctor_healthy.txtar`, `doctor_fix_noop.txtar` — stub `-- bin/agent-safehouse --`
  shell script echoing `Agent Safehouse 0.10.1`, `chmod 0755`, prepend
  `$WORK/bin` to PATH; `doctor_healthy` asserts `stdout 'agent-safehouse'`.
- `doctor_missing.txtar`, `run_missing_prereq.txtar` — `PATH=$CREANCE_BIN` (no
  tools at all).
- `version.txtar` — asserts `stdout 'agent-safehouse'` (buildinfo list).

Integration tests (real tool, `//go:build integration`):
`internal/cage/cage_integration_test.go` (skip-guard via `exec.LookPath(cage.Binary)`),
`internal/verify/verification_integration_test.go`. These already use `safehouse`.

Non-exec occurrences to leave alone: YAML config key `safehouse:`
(`internal/config/config.go:108`, init scaffold `internal/cli/init.go:316`, init
goldens), `.sb` header comments (`internal/profile/profile.go:42,50` + goldens),
comment-only mentions.

### 7. Prior thoughts context

- `thoughts/shared/research/2026-06-06-AC-0023-safehouse-invocation.md` +
  plan — origin of `cage.Binary` and the "caller resolves PATH" stance.
- `thoughts/shared/research/2026-06-07-AC-0025-run-command.md` — run's
  prereq-gate-then-launch ordering.
- `thoughts/shared/research/2026-06-07-AC-0031-doctor-extension.md` — doctor's
  report composition over `prereq.Report`.
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` —
  original prereq design (single-name assumption dates from here).

## Design considerations for planning

1. **Single source of truth.** The candidate list (ordered: `safehouse`,
   `agent-safehouse`) needs one home. Dependency directions today: `prereq`
   imports only `sysdep`; `cage` does not import `prereq`; `cli` imports both.
   Anchoring the list on `cage.Binary` (first candidate) with the fallback name
   alongside avoids the current four-way drift; `prereq.DefaultTools` can import
   `cage` without a cycle, or `cli` can wire the list in — plan decides.
2. **Tool shape.** `Tool.Name` must split into ordered lookup candidates +
   display label; `Result` must carry the resolved name (and can carry the
   resolved path — `Check` currently discards LookPath's result and re-passes the
   bare name to `Output`).
3. **Threading to the launch.** `run.go` already holds the `prereq.Check` results
   before building the cage invocation; the resolved name must flow into
   `cage.Build` (new `Inputs` field or post-`Build` override) so
   `Invocation.Path` is the verified binary, not the constant.
4. **Reporting.** Doctor/prereq report rows and run's skew warning should show
   the resolved name when installed; the missing case keeps the canonical
   label + brew hint. Goldens regenerate via `make golden`.
5. **version command mismatch.** The ticket AC assumes `version` probes installs;
   it doesn't (tested-against list only). Either drop that AC leg (doctor-only
   reporting) or extend `version` to probe — user decision at the checkpoint.

## Open questions (for the Phase-2 checkpoint)

1. `agent-creance version` doesn't probe installed tools (ticket assumed it
   does). Keep it as a pure tested-against listing (canonical label, no resolved
   name — recommended, smallest surface) or extend it to probe PATH and show the
   resolved name like doctor?
2. Display label for the *missing* case: keep `agent-safehouse` (matches the brew
   formula name in the hint) — recommended — or switch to `safehouse`?
