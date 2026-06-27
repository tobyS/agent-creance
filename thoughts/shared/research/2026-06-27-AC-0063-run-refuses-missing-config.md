---
date: 2026-06-27
ticket: AC-0063
topic: "run refuses with an init pointer when the project has no config"
status: complete
git_commit: e15af9b7deafd40c9d6cd1b7812ea5ccf4084e9b
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0063 — `run` refuses with an `init` pointer when the project has no config

**Ticket:** `thoughts/shared/tickets/AC-0063-run-refuses-missing-config.md`
**UX audit (origin):** `thoughts/shared/research/2026-06-25-ux-audit.md`, finding S1

## Research question

`agent-creance run` in a project with no `.agent-creance.yaml` dead-ends with a
cryptic, triple-wrapped `error: compile policy: compile: load project: config:
file not found: .agent-creance.yaml` and no pointer to `init`. We want a fourth
up-front precondition that refuses early with a message naming `agent-creance
init`, consistent with the three existing precondition refusals, leaving the
initialized-project happy path untouched. This document establishes exactly
where and how to add it.

## Summary

The fix is small and well-bounded:

1. **Add a fourth precondition block** to `runRun`
   (`internal/cli/run.go:55-85`), placed in the existing precondition cluster
   **before** `state.New(...).Resolve` (`run.go:88`) and before the progress
   printer is constructed (`run.go:97`). This guarantees the refusal prints to
   stdout with no progress lines first — satisfying acceptance criteria 1 and 2.
2. **Detect presence with `app.FS.Stat(filepath.Join(dir, configFile))`** and
   `errors.Is(err, fs.ErrNotExist)`. `configFile` is the existing constant
   `".agent-creance.yaml"` (`run.go:30`), and this exactly mirrors how the
   later config load resolves the path (`run.go:120`), so there is no new path
   resolution and no risk of disagreeing with the compiler. The `FileSystem`
   seam (`internal/sysdep/filesystem.go:28-30`) returns `fs.ErrNotExist`
   semantics by contract, and there is a test pinning that
   (`internal/sysdep/filesystem_test.go:72-77`).
3. **The refusal follows the established shape:** print a model-grade,
   command-bearing message to `app.Stdout`, then return a short generic error so
   the centralized exit path (`cli.Main`, `internal/cli/cli.go`) prints
   `error: <generic>` to stderr and exits 1. The audit proposed the exact
   wording: *"No .agent-creance.yaml in this project. Run `agent-creance init`
   to create one."*
4. **Add a hermetic testscript** under `internal/cli/testdata/script/` modeled
   on `run_missing_prereq.txtar` (which is hermetic precisely because the
   precondition fires before any keychain/proxy work) — assert non-zero exit and
   that stdout names `agent-creance init`.

**Key correction over the ticket prose:** the current failure actually surfaces
at **step 5 (compile policy)**, not step 6 (load config). The compiler's
`resolve` loads the project config as *required* (`compile.go:224`,
`optional=false`) and errors first, before `run.go`'s own `config.Load` at step
6 is ever reached. The new precondition pre-empts both. (Verified in the audit's
resolved Open Question, `2026-06-25-ux-audit.md:258-264`.)

**One open design decision for the checkpoint:** whether the presence check is
*inline* in `run.go` (a few lines via `app.FS.Stat`) or extracted into a small
new package mirroring `setupcheck`/`cred`. See "Design decision" below.

## Detailed findings

### The current `runRun` flow and the three precondition siblings

`runRun(ctx, app, dir)` (`internal/cli/run.go:55`) is the testable body of `run`
(production passes `dir = "."`, `run.go:47`). The order of operations is the
design's load-bearing contract. The three existing preconditions all sit at the
very top, **before** the progress printer exists:

- **1. Prerequisites** (`run.go:58-62`): `prereq.Check(ctx, app.Commander,
  prereq.DefaultTools(app.Tested))` returns data (never errors); a non-empty
  `prereq.MissingInstructions(results)` is printed to `app.Stdout` and the
  function returns `fmt.Errorf("%d prerequisite(s) missing", …)`.
- **2. Setup** (`run.go:67-74`): `setupcheck.Verify(app.Keychain, app.FS,
  app.Paths)`; on `!res.OK()`, prints `res.Message()` to `app.Stdout` and
  returns `fmt.Errorf("setup incomplete")`.
- **3. Credential** (`run.go:78-85`): `cred.Detect(app.Keychain, app.FS,
  app.Paths)`; on `!res.OK()`, prints `res.Message()` to `app.Stdout` and
  returns `fmt.Errorf("credential unavailable")`.

Then: **4. state resolve** (`run.go:88`), **progress printer constructed**
(`run.go:97`, `defer prog.Close()`), **5. compile policy** (`run.go:104-117`,
which prints `StepStart("Compiling egress policy")` first), **6. load config**
(`run.go:120-123`), profile compile, enforcer extract, proxy, cage, run.

**The refusal mechanism (important — there is no sentinel/refusal type):**
1. The actionable human text is produced by each package's own formatter
   (`prereq.MissingInstructions`, `setupcheck.Result.Message()`,
   `cred.Result.Message()`) and printed straight to `app.Stdout`.
2. The function then returns a *short, generic* `error` (e.g. `"setup
   incomplete"`) — deliberately not wrapping the human text, to avoid
   double-printing.
3. Non-zero exit is centralized in `cli.Main` (`internal/cli/cli.go`): any
   returned error → `error: <err>\n` to stderr → exit 1. Cobra has
   `SilenceUsage`/`SilenceErrors` set, so no usage spam and no double error.

So the contract a new precondition must follow: **human refusal → `app.Stdout`
(no `error:` prefix); terse generic error → returned (becomes the `error:` line
on stderr); exit 1; placed before the progress printer so no `\r` step line
prints first.**

### Why the missing config is not caught today

A missing project config is **required**, not optional. In the policy compiler's
`resolve` (`internal/policy/compile/compile.go:210-227`):

```go
project, err := c.loader.ResolveLayer(
    filepath.Join(layout.Canonical, projectConfigName), false /*required*/)
if err != nil {
    return compileInputs{}, fmt.Errorf("compile: load project: %w", err)
}
```

The global config above it uses `true /*optional*/` (`compile.go:220`). The
loader's not-exist branch (`internal/config/load.go:206-215`) returns
`fmt.Errorf("config: file not found: %s: %w", path, err)` wrapping
`fs.ErrNotExist`. Because the compiler (step 5) runs before `run.go`'s own
`config.Load` (step 6), the cryptic error originates at step 5:
`error: compile policy: compile: load project: config: file not found:
.agent-creance.yaml`.

### No existing presence helper; no `ErrNotFound` sentinel

There is **no** stat-based `Exists` helper and **no** exported
`ProjectConfigPath` builder in `internal/config` or `internal/state`. The
project-config filename is duplicated as an unexported `const` in three places
(`compile/compile.go:53`, `internal/profile/compile.go:19`,
`internal/cli/run.go:30`). The config package's only sentinels are
`ErrIncludeCycle`, `ErrMaxIncludeDepth`, `ErrIncludeOutOfScope`
(`internal/config/errors.go`) — none for a missing top-level file. The only
"is it present" signal is the wrapped `fs.ErrNotExist` from the loader.

Consequence: the precondition must do its own presence probe. The cheapest
correct probe that does not duplicate path-resolution logic is
`app.FS.Stat(filepath.Join(dir, configFile))` — identical to how the existing
step-6 load resolves the path (`run.go:120`), so the two always agree. (Note the
compiler joins onto `layout.Canonical`, the symlink-resolved dir; but `run.go`'s
own load uses `filepath.Join(dir, configFile)` directly, and matching *that* is
both simpler and pre-empts step 5 anyway, since `dir = "."`.)

### The `FileSystem` seam supports this directly

`internal/sysdep/filesystem.go:28-30`:

```go
// Stat returns file info for name, mirroring os.Stat. A non-existent path
// ... returns an error satisfying errors.Is(err, fs.ErrNotExist).
Stat(name string) (fs.FileInfo, error)
```

Pinned by `TestOSFileSystemStatMissingIsNotExist`
(`internal/sysdep/filesystem_test.go:72-77`). `app.FS` is the injected
`sysdep.FileSystem`; tests use the `sysdeptest` fake. So an inline stat is *not*
a violation of the "never call the OS directly from logic packages" rule — it
goes through the seam, exactly like `setupcheck`/`cred` do.

### Testscript pattern to model

`internal/cli/testdata/script/run_missing_prereq.txtar` is the one existing
`run`-refusal `.txtar`, and it is hermetic precisely because the prereq gate
fires before any keychain/proxy work:

```
env PATH=$CREANCE_BIN
! agent-creance run
stdout 'not installed'
stdout 'requires the following tools'
stdout 'brew install mitmproxy'
```

- `! agent-creance run` asserts non-zero exit.
- `stdout '…'` matches a regex substring of stdout (refusals print to stdout).
- `$CREANCE_BIN` is set by the `Setup` hook in `script_test.go:41-55` (first
  PATH entry = the dir holding only the `agent-creance` command).

The missing-config refusal is **even more hermetic**: it fires before the prereq
gate's tool lookups matter to the assertion and before any keychain access, so a
config-less `$WORK` with no `-- .agent-creance.yaml --` section is the whole
scenario.

**Caveat — ordering vs. the prereq gate.** The new precondition's *placement
relative to the prereq check* matters for the testscript. The new check should
be early, but if it is placed **after** the prereq gate (keeping prereq as #1),
then a hermetic config-less testscript must still provide stubbed
`agent-safehouse`/`mitmproxy` on PATH so the prereq gate passes and execution
reaches the config check — otherwise the prereq refusal fires first and the test
asserts the wrong message. Alternatively, placing the config check **first**
(before prereq) makes the testscript trivially hermetic (no tool stubs needed),
but changes the precedence order. The plan must pick an order; see Design
decision Q below. `policy_no_config.txtar` shows the config-absent setup
(`env HOME=$WORK/home`, `env XDG_CACHE_HOME=$WORK/cache`, no config section,
`! agent-creance policy show`, `stderr 'agent-creance\.yaml'`) and is a second
useful model.

`run_test.go:313-362` (`TestRunSetupMissing`, `TestRunCredentialMissing`,
`TestRunMissingPrerequisite`) shows the `*App`+fakes unit-test pattern for these
refusals, asserting (a) error returned, (b) stdout contains the pointer, (c)
nothing downstream ran (`f.proc.Spawned`/`f.pg.Started()` empty). A unit test in
this style is the natural complement to the testscript and lets us assert the
config check fires *and* that compile/proxy/cage never ran.

### Audit-recommended wording and tone

From `2026-06-25-ux-audit.md:84-106` (finding S1), the exact proposed message:

> `No .agent-creance.yaml in this project. Run `agent-creance init` to create one.`

Tone convention (audit lines 53-58): "command-bearing messages" — state the
problem in one short sentence, then name the exact command. Pointing at `init`
is safe because interactive `init` itself gates/offers setup inline
(`2026-06-25-ux-audit.md:78-80`), so the nudge does not forward the user to a
command that will itself dead-end.

## Design decision (for the question checkpoint)

The ticket's two "Questions for Research/Planning" reduce to a single real
choice; the other is settled by the acceptance criteria.

- **Q (settled): pre-check vs. friendlier compiler error?** → **pre-check.**
  Acceptance criterion 2 requires the refusal *before* the compile step with no
  progress lines printed first. A friendlier error mapped from the compiler
  fires at step 5, after `StepStart("Compiling egress policy")` has already
  printed. So an up-front pre-check is required, not optional. No user input
  needed.

- **Q (open): where does the presence check live, and in what order?**
  - **Option A — inline in `run.go`.** ~6 lines using `app.FS.Stat` +
    `errors.Is(fs.ErrNotExist)`, with the message as a named `const` in
    `run.go`. Simplest; matches the binary nature of the check (present/absent,
    no multi-status `Result` like setup/cred have). Tested via a `run_test.go`
    unit test + the testscript.
  - **Option B — new `internal/configcheck` (or similar) package** mirroring
    `setupcheck`/`cred`: a `Verify`/`Check` returning a `Result` with
    `OK()`/`Message()`. More faithful to the precondition-sibling structure and
    independently unit-testable, but heavier for a single boolean with one
    message and one failure mode.
  - **Sub-choice — order relative to prereq (#1).** Place config check as the
    new #4 (after the three existing gates) to preserve current precedence, or
    as #1 (before prereq) so the config-less testscript needs no tool stubs.
    *Recommendation:* keep it after the existing three (new #4) for least
    surprise and to honor "no change to the three existing preconditions"
    (ticket Out of Scope), and stub the two tools on PATH in the testscript.

**Recommendation:** Option A (inline), config check as the new step 4 (after
credential, before `state.Resolve`). Rationale: complexity is genuinely Low (one
boolean, one message, one failure mode), `app.FS.Stat` is already the injected
seam so it respects the OS-isolation rule, and the existing `Result`-bearing
packages (`setupcheck`/`cred`) earn that structure by having multiple statuses
this check does not have. This is the decision to confirm at the checkpoint.

## Code references

- `internal/cli/run.go:30` — `configFile = ".agent-creance.yaml"` constant.
- `internal/cli/run.go:55-85` — `runRun` body; three existing preconditions.
- `internal/cli/run.go:88` — `state.New(app.Paths).Resolve(dir)` (after which is
  too late for a "no progress lines first" refusal once the printer is built).
- `internal/cli/run.go:97` — progress printer constructed.
- `internal/cli/run.go:104-117` — step 5, compile policy (where the cryptic
  error originates today).
- `internal/cli/run.go:120-123` — step 6, `config.Load` (the path-join to match).
- `internal/cli/cli.go` — `Main` centralizes `error:`+exit-1.
- `internal/policy/compile/compile.go:210-227` — required project (224) vs
  optional global (220) config layers.
- `internal/config/load.go:206-215` — `config: file not found` not-exist branch
  wrapping `fs.ErrNotExist`.
- `internal/config/errors.go` — sentinels (none for missing file).
- `internal/sysdep/filesystem.go:28-30, 63-64` — `FileSystem.Stat` contract.
- `internal/sysdep/filesystem_test.go:72-77` — `fs.ErrNotExist` pinned.
- `internal/cli/testdata/script/run_missing_prereq.txtar` — refusal testscript
  model.
- `internal/cli/testdata/script/policy_no_config.txtar` — config-absent model.
- `internal/cli/script_test.go:41-55` — `$CREANCE_BIN` `Setup` hook.
- `internal/cli/run_test.go:313-362` — `*App`+fakes refusal unit tests.

## Related research

- `thoughts/shared/research/2026-06-25-ux-audit.md` — finding S1 (origin) and
  the resolved Open Question (lines 258-264) confirming no implicit-default
  config path and that the failure surfaces at step 5, triple-wrapped.

## Open questions

- The single design decision above (inline vs. dedicated package; order vs.
  prereq) — to confirm at the planning checkpoint. Everything else is determined
  by the existing code and the acceptance criteria.
