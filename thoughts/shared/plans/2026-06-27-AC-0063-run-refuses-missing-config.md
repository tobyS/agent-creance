---
date: 2026-06-27
ticket: AC-0063
topic: "run refuses with an init pointer when the project has no config"
status: implemented
research: thoughts/shared/research/2026-06-27-AC-0063-run-refuses-missing-config.md
git_commit: 6e8f452
branch: main
repository: github.com/tobyS/agent-creance
---

# Implementation Plan: AC-0063 — `run` refuses with an `init` pointer when the project has no config

## Overview

Add a fourth up-front precondition to `agent-creance run`: if the project has no
`.agent-creance.yaml`, refuse early with a model-grade, command-bearing message
that names `agent-creance init`, instead of letting the failure surface as the
cryptic triple-wrapped `error: compile policy: compile: load project: config:
file not found: .agent-creance.yaml`. The check is inline in `runRun`, placed as
step 4 (after the credential gate, before `state.Resolve`), using the injected
`app.FS.Stat` seam. An initialized project is completely unaffected.

## Current state

`runRun` (`internal/cli/run.go:55-85`) runs three preconditions up front
(prerequisites, setup, credential), each printing an actionable message to
`app.Stdout` and returning a terse generic error. A **missing project config is
not a precondition**: it is loaded as *required* by the policy compiler
(`internal/policy/compile/compile.go:224`, `optional=false`) at step 5
(`run.go:104-117`), which prints `StepStart("Compiling egress policy")` first
and then fails with the triple-wrapped error. The progress printer is built at
`run.go:97`, so any refusal after that prints a step line first.

There is no config-presence helper and no `ErrNotFound` sentinel
(`internal/config/errors.go`); the only presence signal is the wrapped
`fs.ErrNotExist` from the loader. `configFile = ".agent-creance.yaml"` is already
a constant (`run.go:30`). The `FileSystem.Stat` seam
(`internal/sysdep/filesystem.go:28-30`) returns `fs.ErrNotExist` semantics by
contract.

## Desired end state

`agent-creance run` in a directory with no `.agent-creance.yaml`:

- exits non-zero with a stdout message that names `agent-creance init` and does
  NOT show the `compile policy: compile: load project: …` wrap;
- refuses before the progress printer is built — no progress/step lines first;
- matches the style of the existing precondition refusals.

An initialized project runs exactly as before.

### Decisions locked at the planning checkpoint

- **Structure: inline in `run.go`** (not a new package). ~6 lines using
  `app.FS.Stat` + `errors.Is(fs.ErrNotExist)`, message as a named `const`.
- **Order: new step 4**, after the credential gate (`run.go:85`) and before
  `state.New(app.Paths).Resolve(dir)` (`run.go:88`). The three existing
  preconditions are unchanged. Because config is checked after the prereq gate,
  the hermetic testscript must stub `agent-safehouse`/`mitmproxy` on PATH so the
  prereq gate passes and execution reaches the config check.

## What we're NOT doing

- Not auto-creating a config or running `init` implicitly from `run`.
- Not changing whether the *global* config is optional (stays optional).
- Not changing any of the three existing preconditions.
- Not adding a `config.Exists` helper or a `configcheck` package (inline chosen).
- Not changing the compiler/loader error text (the precondition pre-empts it).

## Implementation phases

### Phase 1: Add the precondition and tests

#### Changes

**1. `internal/cli/run.go` — message constant.**
Add a named `const` near `configFile` (`run.go:29-30`) with the audit-approved
wording:

```go
// msgNoProjectConfig is the run refusal when the working directory has no
// project config. It names `init` so a first-time user is not left with the
// low-level "compile policy: ... file not found" wrap (AC-0063).
const msgNoProjectConfig = "No .agent-creance.yaml in this project. Run `agent-creance init` to create one."
```

**2. `internal/cli/run.go` — the precondition block.**
Insert a new step 4 between the credential gate (after `run.go:85`) and the state
resolve (before the `// 4. Resolve …` comment at `run.go:87`). Renumber the
existing trailing comments (current 4→5, 5→6, …) or, more simply, label the new
block "4." and bump the subsequent step-comment numbers so the
`run.go` narration stays sequential. The block:

```go
	// 4. Project config precondition: refuse a config-less project up front with
	//    a pointer to `agent-creance init`, rather than letting it surface as the
	//    cryptic "compile policy: ... file not found" wrap at the compile step
	//    (AC-0063). Checked via the FS seam before the progress printer exists, so
	//    the refusal prints cleanly to stdout with no step line first.
	if _, err := app.FS.Stat(filepath.Join(dir, configFile)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(app.Stdout, msgNoProjectConfig)
			return fmt.Errorf("project not initialized")
		}
		return fmt.Errorf("stat config: %w", err)
	}
```

Add imports as needed: `errors` and `io/fs` (verify against the existing import
block; `filepath` and `fmt` are already imported since step 6 uses
`filepath.Join(dir, configFile)` and `fmt.Errorf`).

**3. `internal/cli/run_test.go` — unit test.**
Add `TestRunMissingConfig` modeled on `TestRunSetupMissing` /
`TestRunCredentialMissing` (`run_test.go:313-362`). It must:
- arrange the `*App` fakes so prereq/setup/credential all PASS (so execution
  reaches the new step 4) but the project config is absent in the fake FS;
- call `runRun(ctx, f.app, ".")` (or the fixture's standard dir) and assert it
  returns a non-nil error;
- assert `f.out.String()` contains `agent-creance init`;
- assert nothing downstream ran (`f.proc.Spawned` / `f.pg.Started()` empty), and
  that the compiler/proxy were not invoked — i.e. no progress step printed.

  Inspect the existing fixture in `run_test.go` to match how the passing-prereq /
  passing-setup / passing-credential scenario is set up (the happy-path test in
  that file shows how to make all three gates pass); the only delta for this test
  is an FS with no `.agent-creance.yaml`. Confirm whether the fixture's fake FS
  needs an explicit "absent" arrangement or is empty by default.

**4. `internal/cli/testdata/script/run_missing_config.txtar` — testscript.**
New hermetic case modeled on `run_missing_prereq.txtar` and
`policy_no_config.txtar`. Because the config check is step 4 (after prereq), the
script must put stubbed `agent-safehouse` and `mitmproxy` on PATH so the prereq
gate passes and execution reaches the config check — but it must NOT depend on
the real keychain. Note: `run_missing_prereq.txtar`'s header explains that the
setup/credential gates touch the real login Keychain (`cli.Main` wires
`OSKeychain`), so a full testscript that passes setup+credential is not hermetic.

**Resolve this during implementation (testscript hermeticity boundary):** the new
config precondition sits at step 4, *after* setup (2) and credential (3). If a
testscript cannot hermetically pass the setup/credential gates (because they hit
the real Keychain), it cannot reach step 4, and the missing-config refusal is
only coverable by the `run_test.go` unit test (item 3), which uses keychain
fakes. In that case:
  - keep the unit test as the authoritative coverage for AC #5, and
  - either omit the testscript or add one only if a hermetic path to step 4
    exists (e.g. if the fake-keychain seam is reachable from testscript, which
    `run_missing_prereq.txtar` says it is not).

Decide by checking `script_test.go` / `run_missing_prereq.txtar` again at
implementation time. **AC #5 ("a hermetic testscript case") is the goal; if the
keychain seam genuinely blocks a testscript from reaching step 4, the
`run_test.go` unit test satisfies the intent and the deviation is recorded here.**
Prefer the testscript if a hermetic path exists; fall back to the unit test as
the binding coverage otherwise, and note which was used.

#### Success criteria

**Automated:**
- [x] `make test` passes (race; includes the new unit test). Testscript not added
      (see deviation below); unit test `TestRunMissingConfig` is the binding cover.
- [x] `make lint` passes (`go vet` + `golangci-lint`).
- [x] `go build ./...` succeeds (typecheck).
- [x] `gofmt` clean (verified via `make lint` / pre-commit gofmt check).

**Manual:**
- [x] In a temp dir with no `.agent-creance.yaml`, `bin/agent-creance run` printed
      the `agent-creance init` pointer and exited non-zero (`error: project not
      initialized`), with no `compile policy: …: file not found` wrap and no
      progress step line first. (Dev machine had prereqs/setup/credential
      satisfied, so step 4 was reached.)
- [x] Initialized-project happy path unchanged — full `make test` regression green,
      including `TestRunHappyPath`.

**Deviation:** the testscript (planned item 4 / AC #5) was not added. A testscript
cannot hermetically reach the step-4 config check because the setup/credential
gates (steps 2-3) touch the real login Keychain (`cli.Main` wires `OSKeychain`),
exactly as `run_missing_prereq.txtar`'s header documents. `TestRunMissingConfig`
(`run_test.go`) is the binding coverage and asserts the `init` pointer, the absence
of the compile-policy wrap, no downstream spawn, and no progress step printed.

### Phase 2: Build the binary

Per project convention, run `make build` at the end so `bin/agent-creance`
reflects the final commit (the user tests with this binary).

#### Success criteria

**Automated:**
- [ ] `make build` succeeds and `bin/agent-creance` is updated.

## Testing strategy

- **Unit (`run_test.go`):** authoritative coverage — drives `runRun` with
  `*App` fakes, all three prior gates passing and the project config absent,
  asserting the `init` pointer on stdout, a non-nil error, and that nothing
  downstream ran. This is the test that cannot be defeated by keychain
  hermeticity concerns.
- **Testscript (if hermetic):** `run_missing_config.txtar` asserting `!
  agent-creance run` exits non-zero and `stdout 'agent-creance init'`, with
  stubbed tools on PATH. Used only if a hermetic path to step 4 exists.
- **Regression:** existing `run` tests and the full `make test` suite confirm the
  happy path and the other three preconditions are unchanged.

## Acceptance-criteria mapping (from the ticket)

- AC1 (config-less `run` exits non-zero naming `init`, no `compile policy` wrap)
  → Phase 1 items 1-2 + unit test item 3.
- AC2 (refusal up front, no progress lines first) → placement before
  `progress.NewPrinter` (`run.go:97`); asserted by unit test (no step printed).
- AC3 (style consistent with existing refusals) → message const + stdout print +
  terse returned error, mirroring `setupcheck`/`cred`.
- AC4 (initialized project unaffected) → check is a pure `Stat`; happy path
  untouched; full `make test` regression.
- AC5 (hermetic testscript) → item 4, with the documented unit-test fallback if
  keychain hermeticity blocks reaching step 4.

## References

- Research: `thoughts/shared/research/2026-06-27-AC-0063-run-refuses-missing-config.md`
- Ticket: `thoughts/shared/tickets/AC-0063-run-refuses-missing-config.md`
- `internal/cli/run.go:30,55-91` — constant + precondition cluster + insertion point.
- `internal/cli/run_test.go:313-362` — refusal unit-test pattern.
- `internal/cli/testdata/script/run_missing_prereq.txtar` — testscript model + hermeticity note.
- `internal/sysdep/filesystem.go:28-30` — `Stat` / `fs.ErrNotExist` contract.
