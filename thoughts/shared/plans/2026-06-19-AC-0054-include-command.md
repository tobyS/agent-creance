---
date: 2026-06-19
ticket: AC-0054
title: "Plan — `include` command: add config include-list entries"
status: ready
research: thoughts/shared/research/2026-06-19-AC-0054-include-command.md
---

# Implementation Plan: `include` command (AC-0054)

## Overview

Add an `agent-creance include PATH` command that appends an entry to the project
config's top-level `include:` list, preserving existing comments and formatting,
and recompiles the policy so a running cage picks it up — mirroring `allow`/`deny`.
The command is idempotent (an entry already present is a reported no-op) and
**pre-checks** that the include target resolves and parses before writing, so a
bad path produces a clear error naming the path and leaves the config untouched.

## Decisions (resolved at the question checkpoint)

1. **Validation:** pre-check before write. Resolve + read + parse the target; on
   failure, error naming the path and write nothing.
2. **Targets:** project config only (no `--global`/`--once`). A relative include
   resolves against the declaring file's directory; restricting to the project
   file avoids the footgun of relative paths resolving from `~/.config/` or the
   out-of-tree session overlay.
3. **Dedup:** exact string match — consistent with how the loader treats the raw
   include string (no normalization).

## Current state

- `config.AppendRule` (`internal/config/edit.go:52`) and `config.AppendHostService`
  (`internal/config/edit_hostservice.go:32`) are the only comment-preserving
  appenders; both target keys nested under `network`. **No appender writes the
  top-level `include:` key.**
- The CLI mutation orchestrator `mutateAndRecompile` (`internal/cli/mutate.go:85`)
  is rule-typed (takes `config.RuleList` + `config.Rule`); `mutationTarget` and
  `recompile` are rule-agnostic and reusable verbatim.
- `Config.Include []string` exists (`config.go:34`), with the raw decode tag
  `yaml:"include"` (`config.go:109`); `Parse` copies it (`config.go:159`).
- The loader resolves include paths via the **unexported** `resolveIncludePath`
  (`load.go:251`); no exported single-entry validator exists.

## Desired end state

- `agent-creance include PATH` appends `PATH` to `.agent-creance.yaml`'s
  `include:` list (comments/formatting preserved), recompiles, and prints a
  confirmation; a duplicate is a reported no-op; a missing/unparseable target
  errors naming the path with the config left unchanged.
- New `config.AppendInclude` (comment-preserving, with golden tests) and a new
  exported `(*config.Loader).ValidateInclude` (with unit tests over fakes).
- The rule-typed `mutateAndRecompile` is refactored to delegate to a generic
  `applyAndRecompile` so the include command reuses the read→write→recompile
  skeleton without duplicating it; `allow`/`deny` behavior and output are
  byte-identical (their tests pass unchanged).
- CLI unit tests + a hermetic testscript cover the new command.

---

## Phase 1 — config package: `AppendInclude` + `ValidateInclude`

### 1a. `AppendInclude` (new file `internal/config/edit_include.go`)

Mirror `edit_hostservice.go` exactly, adapted for a **top-level scalar list**:

```go
// AppendInclude appends a single include path to the top-level `include:` list,
// preserving the file's existing comments and formatting. An entry already
// present (exact string match) is a no-op (changed=false). Like AppendRule it
// parses only to locate the insertion point and re-parses the result to verify
// only the include list changed.
func AppendInclude(src []byte, inc string) (out []byte, changed bool, err error)
```

Structure (reusing the shared helpers in `edit.go` — `rootMapping`, `mappingChild`,
`collectAnchors`, `endOfRegion`, `endOfFile`, `backOverBlanks`, `scalar`):

1. `before, err := Parse(src)` — must parse (refuses to edit a broken config,
   like `AppendRule`).
2. `if containsInclude(before.Include, inc) { return src, false, nil }` —
   `containsInclude` is a new exact-string-match helper over the slice (the
   `[]string` analogue of `containsPort`).
3. `yaml.Unmarshal(src, &doc)` into a `yaml.Node`; `lines := strings.Split(...)`.
4. `insertAt, block := planInsertInclude(&doc, lines, inc)`:
   - `root := rootMapping(&doc)`. If `root == nil` (empty/no-mapping doc) →
     synthesize the whole `include:` block at end of file via a
     `renderNestedInclude(inc)` helper; `insertAt = endOfFile(lines)`.
   - Else `inc Node := mappingChild(root, "include")`:
     - missing → synthesize `include:` at indent 0 at end of file
       (`renderNestedInclude`).
     - present and a block sequence → append `- <scalar>` at the end of its
       region (`endOfRegion`), item indent matching the first existing item's
       leading spaces (default 2 when the list is empty).
     - present but an **empty/flow** node (`include: []`): handle by rewriting to
       block form — see Risk note below; add a golden fixture to pin behavior.
5. Splice: `out = strings.Join(append(append(lines[:insertAt:insertAt], block...), lines[insertAt:]...), "\n")` — mirror the exact join `AppendRule` uses.
6. `validateAppendInclude(src, out, inc)` — re-`Parse(out)` and assert: both
   egress lists unchanged (`sameIdentities` for Allow + DenyAlways), `host_services`
   unchanged (`sameHostServices`), and `Include` equals `before.Include` + exactly
   `inc`. Any unexpected diff → error, never a partial result.

Item rendering: `renderIncludeItem(indent, inc)` emits `<indent>- <scalar(inc)>`.
The existing `scalar`/`isPlainSafe` helpers quote `~/`-prefixed and otherwise
unsafe values (research confirmed `~/foo.yaml` is quoted correctly).

### 1b. `ValidateInclude` (add to `internal/config/load.go`)

Exported method on `*Loader` reusing the unexported `resolveIncludePath` + the
`fs`/`paths` seams:

```go
// ValidateInclude checks that include entry `inc`, as declared by the config file
// at `declaringPath`, resolves to a readable, parseable config file. The returned
// error names the resolved path on failure.
func (l *Loader) ValidateInclude(declaringPath, inc string) error
```

1. `home, _ := l.paths.UserHomeDir()` (propagate error).
2. `abs, _ := l.paths.Abs(declaringPath)` (propagate error). `filepath.Dir(abs)`
   is the resolution base — works even when the project config does not exist yet.
3. `incPath := l.resolveIncludePath(abs, home, inc)`.
4. `data, err := l.fs.ReadFile(incPath)` — wrap not-found and other read errors
   naming `incPath` (reuse the `config: file not found: %s` / `config: read %s`
   wording from `resolve`, or a dedicated `include:`-prefixed message).
5. `_, err := Parse(data)` — wrap parse errors naming `incPath`.
6. Return nil on success.

This deliberately validates only the single new entry (read + parse), not the
whole graph, keeping the pre-check fast and the error pinpointed to the path the
user just gave.

### 1c. Tests (Phase 1)

- **Golden** (`internal/config/edit_include_test.go`, mirror `edit_test.go`):
  table `appendIncludeGoldenCases` + `TestAppendIncludeGolden` reading
  `testdata/edit/<case>.in.yaml` → `<case>.golden.yaml`. Cases:
  - `include_existing` — config with an existing `include:` block + a comment;
    append a second entry (assert comment survives).
  - `include_from_scratch` — config with `network:` only, no `include:` key →
    synthesizes the block.
  - `include_empty_flow` — `include: []` → appended entry (pins the flow-node
    behavior).
  - `include_home` — append `~/baseline.yaml` (assert it is quoted).
  Plus `TestAppendIncludeDuplicate` (changed==false, bytes unchanged),
  `TestAppendIncludeRejectsBrokenInput` (refuses a non-parsing config).
  Generate goldens with `make golden`; review the diff.
- **ValidateInclude** (`internal/config/load_test.go` or a new
  `validate_include_test.go`, table-driven over `sysdeptest` fakes): ok (relative
  path resolves against the declaring file's dir), not-found (error names the
  resolved path), unparseable target (error names the path), `~/`-relative and
  absolute forms resolve correctly.

**Verify Phase 1:** `make test` (config package green), `make golden` then review,
`make lint`.

---

## Phase 2 — CLI: refactor orchestrator, add the command

### 2a. Extract `applyAndRecompile` (`internal/cli/mutate.go`)

Refactor the read→append→write→recompile skeleton out of `mutateAndRecompile`
into a generic helper parameterized by the append closure + display strings:

```go
func applyAndRecompile(
    ctx context.Context, app *App, dir, path, fileLabel, subject, verb string,
    apply func(src []byte) (out []byte, changed bool, err error),
) error
```

Body is the current `mutateAndRecompile` body (lines ~86–115) with `ruleLabel(rule)`
→ `subject` and the `config.AppendRule(...)` call → `apply(data)`. The no-op and
success `Fprintf`s keep their exact format strings:
`"%s is already %s in %s; nothing to do\n"` and
`"%s %s %s in %s; policy recompiled\n"` (with `app.OutStyle.OK("✓")`).

Then `mutateAndRecompile` becomes a thin wrapper preserving its current signature
and behavior:

```go
func mutateAndRecompile(ctx context.Context, app *App, dir, path, label string,
    list config.RuleList, rule config.Rule, verb string) error {
    return applyAndRecompile(ctx, app, dir, path, label, ruleLabel(rule), verb,
        func(src []byte) ([]byte, bool, error) { return config.AppendRule(src, list, rule) })
}
```

`allow`/`deny` are untouched; their unit tests and `allow_deny.txtar` must pass
unchanged (regression guard for the refactor).

### 2b. The `include` command (new file `internal/cli/include.go`)

Mirror `deny.go` (project-only, single positional arg):

```go
func newIncludeCmd(app *App) *cobra.Command {
    return &cobra.Command{
        Use:   "include PATH",
        Short: "Add an include entry to the project config and recompile the policy",
        Long:  // brief: appends PATH to include:, recompiles; project config only.
        Args:  cobra.ExactArgs(1),
        RunE:  func(cmd *cobra.Command, args []string) error {
            return runInclude(cmd.Context(), app, ".", args[0])
        },
    }
}

func runInclude(ctx context.Context, app *App, dir, inc string) error {
    path, label, err := mutationTarget(app, dir, false, false) // project config
    if err != nil { return err }
    // Pre-check the target resolves and parses before touching the config.
    if err := config.NewLoader(app.FS, app.Paths).ValidateInclude(path, inc); err != nil {
        return err
    }
    return applyAndRecompile(ctx, app, dir, path, label, inc, "included",
        func(src []byte) ([]byte, bool, error) { return config.AppendInclude(src, inc) })
}
```

Notes:
- Subject in the messages is the raw `inc` string → "included ./baseline.yaml in
  .agent-creance.yaml; policy recompiled" and "./baseline.yaml is already included
  in .agent-creance.yaml; nothing to do".
- Pre-check runs before read/append, so a bad path never writes.

### 2c. Register the command (`internal/cli/cli.go`)

Add `root.AddCommand(newIncludeCmd(app))` in the `cli.go:119-130` block, next to
allow/deny.

### 2d. Tests (Phase 2)

`internal/cli/include_test.go`, reusing the shared `mutateFixture` from
`allow_test.go` (seed the include-target file into the fake FS at its resolved
absolute path, e.g. `/proj/baseline.yaml`):

- `TestIncludeAppendsToProjectFile` — seed a valid baseline fragment; `runInclude`;
  parse project config and assert `Include` contains the path; assert `policy.json`
  reflects the recompile (baseline's rule present) and stdout has "policy
  recompiled".
- `TestIncludeDuplicateIsNoOp` — run twice; second is "already included", project
  bytes unchanged.
- `TestIncludeMissingTargetErrors` — path absent from fake FS → error naming the
  path; project config unchanged.
- `TestIncludeUnparseableTargetErrors` — seed an invalid-YAML target → error naming
  the path; project config unchanged.

**Verify Phase 2:** `make test` (cli + config green, allow/deny unchanged), `make lint`.

---

## Phase 3 — testscript + final verification

### 3a. Hermetic testscript (`internal/cli/testdata/script/include.txtar`)

Mirror `allow_deny.txtar`'s hermetic setup (`HOME`/`XDG_CACHE_HOME` under `$WORK`,
`PATH=$CREANCE_BIN`). Provide a seed project config and a baseline fragment as
`-- ... --` archive sections. Assert:

- `agent-creance include --help` → shows usage / `PATH`.
- `! agent-creance include` → `stderr 'accepts 1 arg'`.
- `agent-creance include ./baseline.yaml` → stdout
  `'included ./baseline\.yaml in \.agent-creance\.yaml'` and `'policy recompiled'`;
  `grep 'include:' .agent-creance.yaml`; `grep '# keep this comment'` (comment
  preserved); `grep './baseline.yaml' .agent-creance.yaml`.
- `agent-creance policy show` → reflects a rule contributed by the included
  fragment (end-to-end recompile picks up the include).
- Re-run `agent-creance include ./baseline.yaml` → `stdout 'already included'`.
- `! agent-creance include ./missing.yaml` → `stderr` naming the path; the config
  still does not contain `missing.yaml`.

(The harness auto-discovers `*.txtar`; no registration needed.)

### 3b. Final verification

- `make test` — full hermetic suite green (race detector).
- `make lint` — `go vet` + `golangci-lint` clean.
- `make golden` — only the intended new include golden fixtures appear in the diff.
- `make build` — `bin/agent-creance` rebuilt (per CLAUDE.md, the binary the user
  tests with).
- Manual smoke (optional): in a scratch dir, `agent-creance include ./frag.yaml`
  against a real fragment, confirm the confirmation line and the appended entry.

---

## Success criteria

Maps to the ticket's Acceptance Criteria:

- [ ] `include PATH` appends to the project config's `include:` list, preserving
      comments/formatting, and recompiles (AC #1) — Phase 1a + 2b, golden +
      testscript.
- [ ] Re-adding is a no-op with an "already included" message (AC #2) — Phase 1a
      dedup, unit + testscript.
- [ ] Success line reports the change + recompile (AC #3) — Phase 2a message.
- [ ] Unresolvable/unparseable include → clear error naming the path, config
      recoverable/untouched (AC #4) — Phase 1b pre-check + Phase 2b ordering, unit +
      testscript.
- [ ] Target parallels allow/deny for the project config; `--global`/`--once`
      resolved as out-of-scope here (AC #5, decision 2) — Phase 2b.
- [ ] Unit-tested via `sysdep` fakes; CLI behavior via hermetic testscript
      (AC #6) — Phases 1c, 2d, 3a.
- [ ] `make test`, `make lint` pass; `make build` at the end (AC #7) — Phase 3b.

**Automated checks (from profile.md):** `make test`, `make lint`, `go build ./...`,
`make golden` (review diff), `make build`.

## Risks / notes

- **Empty/flow `include: []`:** the existing splice helpers are oriented to block
  sequences. The `include_empty_flow` golden case pins behavior; if appending into
  a flow node produces invalid output, `planInsertInclude` must rewrite that line
  to a block list (or synthesize a fresh block key). Resolve during Phase 1 — do
  not ship a case that fails the re-parse `validateAppendInclude` gate.
- **Refactor regression:** `applyAndRecompile` must keep the allow/deny output
  byte-identical. The existing `allow_test.go`/`deny_test.go`/`allow_deny.txtar`
  are the guard — run them after 2a before adding the include command.
- **Pre-check vs. graph:** `ValidateInclude` checks only the single new entry
  (read+parse), not cycles/depth across the whole graph. That is sufficient for
  AC #4 (a wrong/missing path); a pathological cycle introduced by the new include
  would still surface at the next full load, consistent with the loader's existing
  behavior. Out of scope to pre-detect here.
