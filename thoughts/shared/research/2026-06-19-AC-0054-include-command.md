---
date: 2026-06-19
ticket: AC-0054
title: "Research — `include` command: add config include-list entries"
status: complete
commit: ae8f0352b4bef68b513d5f23b1196dff3c86e2cf
branch: main
---

# Research: `include` command (AC-0054)

## Research question

How do we add an `agent-creance include PATH` command that appends an entry to a
config's top-level `include:` list — preserving comments/formatting and
recompiling the policy — mirroring the existing `allow`/`deny` commands? What
machinery can be reused, what must be newly built (an `AppendInclude` analogue to
`AppendRule`), how should include paths be validated, and which targets
(`--global`/`--once`) make sense?

## Summary

The mutation flow `allow`/`deny` use is almost entirely reusable: command
wrappers delegate to a shared `mutateAndRecompile` helper that reads → appends
(comment-preserving) → atomically writes → recompiles, with idempotency handled
inside the append function. **The one genuinely new piece is a comment-preserving
appender for the top-level `include:` list** — `config.AppendRule` and its sibling
`config.AppendHostService` only target keys *nested* under `network`, so neither
can write a top-level scalar list. `internal/config/edit_hostservice.go` is the
exact template for the new `AppendInclude`: it's a parallel, self-contained editor
sharing the same node-navigation/splice/validate infrastructure in `edit.go`.

Key facts that shape the plan:

- `include:` entries are **filesystem paths only** — three forms: `~/`-prefixed
  (home-relative), absolute, or relative (resolved against the **declaring
  file's directory**, not cwd). No named-baseline form, no URL form.
- Include resolution (read + parse + cycle/depth checks) happens **only at load
  time**, inside the loader's recursive `resolve`. There is **no exported helper**
  that validates a single include entry today.
- `Include []string` already exists on `Config` (field at `config.go:34`, raw tag
  `yaml:"include"` at `config.go:109`). The loader resolves it and then clears it
  from the effective config — it's a load-time directive, not a carried value.
- Test patterns are well-established at three layers and directly mirrorable:
  testscript (`allow_deny.txtar`), unit tests over a shared `mutateFixture`
  (`allow_test.go`/`deny_test.go`), and golden tests for the appender
  (`config/edit_test.go`).

## Detailed findings

### The shared CLI mutation machinery (reuse as-is)

`internal/cli/mutate.go`:

- **`mutateAndRecompile`** (`mutate.go:85-116`): the orchestrator. Reads the
  target file (`app.FS.ReadFile`, tolerating `fs.ErrNotExist` → `data = nil` so a
  missing global/overlay is created), calls the appender, branches on `changed`,
  writes atomically (`writeFileAtomic`, `init.go:349-359`, perm `configFilePerm =
  0o644` `init.go:256`) under `MkdirAll(dir, 0o755)`, then `recompile`. Success
  line at `mutate.go:114`: `"%s %s %s in %s; policy recompiled"` with a colored
  `app.OutStyle.OK("✓")`. **This helper is rule-typed today** (takes a
  `config.RuleList` + `config.Rule`), so the include command needs either a
  parallel helper or a refactor (see Open Questions / plan).
- **No-op / idempotency** (`mutate.go:98-101`): when the appender returns
  `changed == false`, it prints `"%s is already %s in %s; nothing to do"` and
  skips the write + recompile entirely. The dedup *decision* lives inside the
  appender, not here.
- **`mutationTarget`** (`mutate.go:62-79`): resolves the target file + a label
  from `(once, global)` flags. Three variants:
  - `--once` → session overlay: `state.New(app.Paths).Resolve(dir)` →
    `layout.SessionOverlay()` (`state.go:265`,
    `<cache>/agent-creance/projects/<hash>/session-overlay.yaml`); label `"the
    session overlay"`.
  - `--global` → `config.NewLoader(app.FS, app.Paths).GlobalPath()`
    (`~/.config/agent-creance.yaml`, `load.go:77-83`); label is the path.
  - default → `filepath.Join(dir, configFile)` where `configFile =
    ".agent-creance.yaml"` (`run.go:28`); label `configFile`.
  **This helper is rule-agnostic and reusable verbatim.**
- **`recompile`** (`mutate.go:121-128`): `compile.New(app.FS, app.Paths,
  app.Clock, app.HTTP, nil)` then `compiler.Compile(ctx, dir)`. Idempotent;
  rewrites `policy.json` (mtime advances → enforcer reloads within ~1s).
  Rule-agnostic, reusable verbatim.

### The thin-wrapper command pattern (mirror this)

`internal/cli/allow.go`:

- `newAllowCmd(app)` (`allow.go:17-32`): `Use: "allow URL"`, `Args:
  cobra.ExactArgs(1)`, binds `--once`/`--global` BoolVars, `RunE` calls
  `runAllow(cmd.Context(), app, ".", args[0], once, global)`.
- `runAllow(ctx, app, dir, rawURL, once, global)` (`allow.go:37-50`): rejects
  `--once`+`--global` together; builds the rule; `mutationTarget(...)`;
  `mutateAndRecompile(..., config.AllowList, rule, "allowed")`.

`internal/cli/deny.go`: same shape but only a `--reason` flag and **no
`--once`/`--global`** — a hard deny always lands in the project file (doc comment
`deny.go:11-14`); `runDeny` hard-codes `mutationTarget(app, dir, false, false)`.

The testable `run*` body takes `dir` + flags as **parameters** (not globals) —
this is what lets unit tests drive every target against fakes.

**Registration**: `internal/cli/cli.go` `newRootCmd` adds commands via
`root.AddCommand(...)` (block at `cli.go:119-130`; allow at `126`, deny at `127`).
A new `include` command adds one line: `root.AddCommand(newIncludeCmd(app))`.

### The appender — what's new: `config.AppendInclude`

`internal/config/edit.go` holds the comment-preserving splice infrastructure:

- **`AppendRule(src, list, rule)`** (`edit.go:52-81`): parses to *locate* an
  insertion point (never re-serializes), splices rendered text into the raw
  line array, and re-parses + diffs the candidate (`validateAppend`,
  `edit.go:232-257`) to guarantee only the intended change landed. The file-level
  doc (`edit.go:3-19`) explains the rationale: the config is hand-authored and
  comment-rich, so a decode→re-encode round-trip is rejected.
- Shared node helpers usable by a new appender: `rootMapping`, `mappingChild`
  (`edit.go:353-363`), `isMapping`, `indentOf`, `leadingSpaces`, `collectAnchors`
  (`edit.go:324-340`), `endOfRegion` (`edit.go:136-148`), `endOfFile`,
  `backOverBlanks` (`edit.go:157-162`), `renderNested`, and the scalar
  quoting helpers `scalar`/`isPlainSafe` (`edit.go:206-226`) — the latter already
  quote `~/`-prefixed paths correctly.

**`AppendHostService`** (`internal/config/edit_hostservice.go:32-59`) is the exact
precedent for a new sibling-list editor — it mirrors `AppendRule` for a different
key:

- `planInsertHostService` (`edit_hostservice.go:64-92`) walks `network` →
  `host_services`.
- `renderNestedHostService` / `renderHostServiceItem` (`edit_hostservice.go:96-109`).
- `validateAppendHostService` (`edit_hostservice.go:114-130`) asserts both egress
  lists are unchanged and `host_services` equals before + the new entry.
- Dedup by `containsPort` (`edit_hostservice.go:132-139`).

**What `AppendInclude(src []byte, path string)` must do differently:** `include:`
is a **top-level** key (off `rootMapping(doc)` directly via `mappingChild(root,
"include")`, with **no `network` parent** — one level shallower than
host_services), and its items are **plain string scalars** (`- ~/foo.yaml`), not
mappings. So it needs:

- Navigation locating the top-level `include:` key; synthesizing it at indent 0
  if absent (a `renderNested`-style helper for a string list).
- An item renderer emitting `- <scalar>` via the existing `scalar` helper.
- A dedup identity on the include string (analogous to `containsPort`).
- A `validateAppend`-style gate that re-parses and asserts **both egress lists and
  host_services are unchanged** while `Include` equals before + exactly the new
  entry.

There is **no `AppendInclude` today** — `import.go`'s `ignoredSectionsNote`
(`import.go:142-164`) explicitly lists `include` among sections it does *not*
merge (`import.go:156-158`), confirming no include-editing path exists yet.

### Include path forms & resolution (drives validation)

`internal/config/load.go`:

- `resolveIncludePath(declaringAbs, home, inc)` (`load.go:251-263`) — the only
  form logic:
  - `~/` prefix → `filepath.Join(home, inc[2:])` (home-relative).
  - `filepath.IsAbs(inc)` → used verbatim.
  - else → `filepath.Join(filepath.Dir(declaringAbs), inc)` — **relative to the
    declaring file's directory**, not cwd, not project root.
  - **No named-baseline form, no URL form.** Anything else is treated as a
    relative path.
- Resolution happens recursively in `resolve(path, home, optional, stack, depth)`
  (`load.go:193-249`), invoked from `Load` (`load.go:48-71`). Included files are
  **always required** (`load.go:241` passes `optional=false`) — a declared include
  that's missing is an error.
- Error surfaces, **all at load time** (`Parse` does no resolution):
  - missing required include → `config: file not found: <path>: <err>`
    (`load.go:209`).
  - other read error → `config: read <path>: <err>` (`load.go:211`).
  - included file fails strict parse → `config: <path>: <err>` (`load.go:229-231`).
  - cycle → `ErrIncludeCycle` (`load.go:221-225`, sentinel `errors.go:19`); identity
    is the symlink-resolved canonical path (`EvalSymlinks`, `load.go:217`).
  - depth > `maxIncludeDepth = 10` (`load.go:13-17`) → `ErrMaxIncludeDepth`
    (`load.go:194-196`, sentinel `errors.go:22`).
- **No string normalization** of the include entry beyond what `filepath.Join`'s
  implicit `Clean` does in the *resolved* path; the stored entry string is not
  rewritten. So idempotency on the raw string needs a deliberate choice (see
  Open Questions).
- **No exported single-entry validator.** Building blocks to compose one:
  `resolveIncludePath` (currently unexported), `fs.ReadFile` + `config.Parse`, or
  running `Loader.ResolveFiles(projectPath)` / `ResolveLayer(path, optional)`
  *after* the write to confirm the whole graph still resolves.

### `Include` on the config type

`internal/config/config.go`: typed field `Config.Include []string`
(`config.go:34`, no tag — `Config` is the converted public type); raw decode
mirror `rawConfig.Include []string` with `yaml:"include"` (`config.go:109`); `Parse`
copies it across (`config.go:159`). `merge.go` deliberately does **not** merge
`Include` (`merge.go:16`); the loader clears it after resolution (`load.go:69`,
`load.go:246`). The field and tag the new appender's validation needs already
exist.

### Test patterns to mirror (all three layers)

1. **testscript** — `internal/cli/testdata/script/allow_deny.txtar`: one file
   covers both commands. Sets `HOME`/`XDG_CACHE_HOME` under `$WORK` and
   `PATH=$CREANCE_BIN` for hermeticity; seeds config via a `-- .agent-creance.yaml
   --` archive section; asserts success line, comment preservation (`grep '#
   keep this comment'`), idempotency (`stdout 'already allowed'`), error/arg
   cases (`! agent-creance allow` / `stderr 'accepts 1 arg'`), `--global`/`--once`
   targeting, and that `policy show` reflects the change. The harness
   (`script_test.go`) auto-runs every `*.txtar` and exposes `$CREANCE_BIN` — a new
   `include.txtar` (or additions to `allow_deny.txtar`) needs no registration.
2. **unit** — `internal/cli/allow_test.go` defines `mutateFixture` (constructs an
   `App` from `sysdeptest` fakes: `FakeFileSystem` map, `FakePathResolver.HomeDir`,
   `FakeClock`, `FakeHTTPGetter`) with helpers `projectConfig`/`policyJSON`.
   `allow_test.go`/`deny_test.go` drive `runAllow`/`runDeny` directly and assert:
   parsed result, `policy.json` contains the change + `"policy recompiled"`
   stdout, `--global`/`--once` isolation (project untouched), flag conflict, and
   duplicate no-op (`"already allowed"`/`"already denied"`). The include command
   reuses this fixture.
3. **golden** — `internal/config/edit_test.go` table `editGoldenCases` +
   `TestAppendRuleGolden` reads `testdata/edit/<case>.in.yaml`, runs the appender,
   asserts `changed`, re-parses, and compares to `<case>.golden.yaml`
   (regenerate via `make golden`). Plus `TestAppendRuleDuplicate` (changed==false,
   bytes unchanged), `TestAppendRuleSemantics`, `TestAppendRuleRejectsBrokenInput`.
   The new `AppendInclude` gets a parallel golden test + fixtures under
   `internal/config/testdata/edit/`.

## Code references

- `internal/cli/mutate.go:85-116` — `mutateAndRecompile` (orchestrator; rule-typed today)
- `internal/cli/mutate.go:62-79` — `mutationTarget` (project/`--global`/`--once`; reusable)
- `internal/cli/mutate.go:121-128` — `recompile` (reusable)
- `internal/cli/allow.go:17-50` — command wrapper + `runAllow` (mirror)
- `internal/cli/deny.go:15-42` — `--reason`-only variant (project-only target)
- `internal/cli/cli.go:119-130` — `root.AddCommand(...)` registration block
- `internal/cli/run.go:28` — `const configFile = ".agent-creance.yaml"`
- `internal/cli/init.go:349-359,256` — `writeFileAtomic`, `configFilePerm`
- `internal/config/edit.go:52-81` — `AppendRule` (template)
- `internal/config/edit.go:206-226,232-257,324-363` — scalar/quoting, `validateAppend`, node helpers
- `internal/config/edit_hostservice.go:32-139` — `AppendHostService` (closest sibling precedent)
- `internal/config/load.go:251-263` — `resolveIncludePath` (the form logic)
- `internal/config/load.go:193-249` — `resolve` (recursive, cycle/depth, error surfaces)
- `internal/config/load.go:13-17` — `maxIncludeDepth = 10`
- `internal/config/errors.go:19,22` — `ErrIncludeCycle`, `ErrMaxIncludeDepth`
- `internal/config/config.go:34,109,159` — `Include` field, raw tag, `Parse` copy
- `internal/config/merge.go:16` — Include not merged
- `internal/cli/testdata/script/allow_deny.txtar` — testscript to mirror
- `internal/cli/script_test.go` — testscript harness / `$CREANCE_BIN`
- `internal/cli/allow_test.go`, `internal/cli/deny_test.go` — `mutateFixture` + unit tests
- `internal/config/edit_test.go` — golden test pattern; fixtures in `internal/config/testdata/edit/`

## Open questions (for the checkpoint)

1. **Validate-before-write vs. write-then-recompile.** The ticket's Q3. allow/deny
   write then recompile and report a recompile failure (mutation kept, recompile
   error surfaced). For an include, a broken/missing target only fails at the
   *next* load — recompile may or may not exercise it. Options: (a) pre-check the
   path resolves (read+parse) before writing for a clear, early "names the path"
   error per AC #4; (b) follow the allow/deny write-then-recompile pattern and
   rely on recompile to surface it; (c) write, recompile, and on failure roll back
   the write. AC #4 ("clear error naming the path; user left able to recover")
   leans toward (a) or (c).
2. **Targets: project-only or also `--global`/`--once`?** The ticket's Q2 / AC #5.
   `mutationTarget` supports all three for free. But include *paths* are resolved
   relative to the **declaring file's directory** — a relative include written to
   the global file (`~/.config/`) or the out-of-tree session overlay resolves
   relative to *that* file's dir, not the project, which is surprising. Options:
   (a) project-only, like `deny` (simplest, avoids the relative-path footgun);
   (b) project + `--global` + `--once` mirroring `allow` (max parallelism).
3. **Dedup/normalization of the include string.** The ticket's Q5. There's no
   normalization in the loader. Options: (a) exact string match only (simplest,
   predictable, matches "as authored"); (b) normalize (strip `./`, trailing slash,
   `filepath.Clean`) before compare so `./x.yaml` and `x.yaml` dedupe. (a) is
   simplest and consistent with how the loader treats the raw string; (b) is more
   forgiving but can mask genuinely distinct entries.

## tce config drift

None detected. The profile's code map and the tmt ticket backend both match the
repository as researched.
