---
date: 2026-06-07
ticket: AC-0030
title: "Plan: allow / deny commands (WP-6.1)"
status: complete
research: thoughts/shared/research/2026-06-07-AC-0030-allow-deny-commands.md
commit: a4ce8c24ea55fccfebfade551e3c4e09165a94df
---

# Plan: AC-0030 `allow` / `deny` commands (WP-6.1)

## Overview

Add `agent-creance allow URL` (`--once`, `--global`) and `agent-creance deny URL
[--reason]`. Each appends a rule to the right config target, then recompiles
`policy.json` so the running proxy hot-reloads. The work is additive glue over
existing infrastructure (config schema, compiler, overlay layering, AC-0020 purge);
the only non-trivial piece is a comment-preserving YAML append.

## Decisions (from the checkpoint)

1. **YAML mutation = parse-for-location text splice + validation gate.** Locate the
   target sequence via `yaml.Node` positions, splice the rendered rule as text
   (rest of the file untouched byte-for-byte), then validate by re-parsing with
   `config.Parse` and asserting the rule set equals original + exactly the new rule
   before the atomic write. Same path for all three targets.
2. **Single rule per invocation.** No forge content-host expansion for
   `allow <repo-url>` (deferred to a follow-up).
3. **URL→rule:** bare host → host-only rule (`Paths` nil); host+path → `Paths:[path]`.
   No `--method` flag, so `Methods` nil (all methods). Share only a tiny `splitURL`
   helper with `policy explain`'s `requestFromURL`.
4. **Exact duplicate → no-op:** if the identical rule already exists in the target,
   report it and skip the rewrite + recompile.
5. **`deny` targets the project file only**, with `--reason` (matches design.md:369;
   `--global`/`--once` are allow-only). Noted as a possible future extension.

## Current state

- `config.Config.Network.Egress` holds `Allow []Rule` / `DenyAlways []Rule`
  (`internal/config/config.go:52-83`). `config.Parse` is strict, read-only
  (`config.go:128-157`); **no YAML writer exists yet**.
- Write targets are all locatable: project `configFile` (`internal/cli/run.go:23`),
  `config.Loader.GlobalPath()` (`internal/config/load.go:77-83`),
  `state.Layout.SessionOverlay()` (`internal/state/state.go:204-206`).
- Recompile + hot-reload: `compile.New(...).Compile(ctx, dir)` rewrites `policy.json`
  on a hash change (`internal/policy/compile/compile.go:232,517-537`); the enforcer
  polls mtime (`internal/proxy/enforcer/enforcer.py:138-152`). Overlay is a first-class
  `once` layer and is purged by AC-0020's `Manager.Detach` (`internal/proxy/lifecycle.go:165-178`).
- Command pattern: `newXxxCmd(app)` + thin `RunE` → testable `runXxx`; file writes via
  `app.FS` + `writeFileAtomic` (`internal/cli/init.go:42-111`); flags via cobra.

## Desired end state

`allow`/`deny` work from any terminal, append to the chosen target preserving
comments, recompile so a running proxy reloads within ~1s, and are covered by
golden + unit + testscript tests matching the ticket's verification steps.

---

## Phase 1 — Comment-preserving config edit core (`internal/config`)

Pure, I/O-free YAML mutation in the package that already owns the schema and `Parse`.

### Changes

**New file `internal/config/edit.go` (package `config`):**

```go
// RuleList selects which egress list AppendRule targets.
type RuleList int
const (
	AllowList RuleList = iota
	DenyList
)

// AppendRule returns src with rule appended under network.egress.<list>,
// preserving every other byte (comments, blank lines, quoting, indentation).
// It locates the insertion point from yaml.Node positions and splices text;
// when the key or its parent sections are absent/commented-out, it synthesizes
// minimal structure. changed is false (out == src) when an identical rule already
// exists. The candidate is validated via config.Parse + a rule-set diff (original
// + exactly rule); a mismatch or parse error returns a non-nil error and never a
// partial result.
func AppendRule(src []byte, list RuleList, rule Rule) (out []byte, changed bool, err error)
```

Internals:
- `renderRuleItem(rule Rule, indent int) string` — emits the YAML list item
  (`- host: …`, optional `paths: [...]` flow seq, optional `methods: [...]`, optional
  `reason: "…"`). Omits empty `mode`/`reason`/`paths`/`methods`. Quotes `reason` and
  any host/path needing it (reuse a minimal scalar-quote check; flow style matches the
  design example `paths: ["/repos/foo/"]`).
- Locate via `yaml.Unmarshal(src, &node)` → walk document→mapping for
  `network`→`egress`→`<list>`. `yaml.Node.Line/Column` drive the byte offset and indent.
  Cases:
  1. **Sequence exists, non-empty** → insert after the last item's line at the item indent.
  2. **Key exists, null/empty value** → replace the value with a rendered block.
  3. **`egress` exists, key absent** → append `<list>:` block at end of the `egress` mapping.
  4. **`network`/`egress` absent (incl. fully-commented file)** → append a full
     `network:\n  egress:\n    <list>:\n      - …` block at EOF.
- **Duplicate check** (before splicing): parse src, compare the target list for a rule
  with equal host + paths + methods (order-insensitive); if found → `return src, false, nil`.
- **Validation gate**: `cfg, err := Parse(out)`; on error → wrap+return. Diff
  `cfg.Network.Egress.<list>` against the source list: must be source + exactly `rule`
  (compare via the same normalization as the dup check). On mismatch → error (defensive;
  catches any splice bug before the caller writes).

### Tests (`internal/config/edit_test.go`)

- **Golden** (`testdata/edit/*`): input YAML → output YAML for each case — append to
  existing list, create missing key under `egress`, create full section from a
  comment-only file, append to global-style file, deny-with-reason, host+path vs
  bare host. `-update` flag per project convention; assert comments/blank lines survive.
- **Table**: duplicate → `changed == false`, bytes unchanged; validation rejects a
  deliberately corrupt render; bare host → no `paths`; path → `paths:[...]`.

### Success criteria

#### Automated
- [x] `go build ./...`
- [x] `go test -race ./internal/config/...` green
- [x] `make golden` produces the intended `testdata/edit/*` and re-running shows no drift
- [x] `make lint` clean (config package)

#### Manual
- [x] Golden outputs visually preserve comments and blank lines outside the insertion point

---

## Phase 2 — `allow` / `deny` CLI commands (`internal/cli`)

### Changes

**`internal/cli/allow.go`:**
- `newAllowCmd(app *App) *cobra.Command` — `Use: "allow URL"`, `ExactArgs(1)`, flags
  `--once` and `--global` (BoolVar). `RunE` → `runAllow(ctx, app, args[0], once, global)`.
- `runAllow(ctx, app, rawURL string, once, global bool) error`:
  1. Reject `once && global` ("cannot combine --once and --global").
  2. `rule, err := ruleFromURL(rawURL, "")`.
  3. `path := allowTarget(app, ".", once, global)` (see helper below).
  4. `mutateAndRecompile(ctx, app, ".", path, AllowList, rule, "allowed")`.

**`internal/cli/deny.go`:**
- `newDenyCmd(app *App) *cobra.Command` — `Use: "deny URL"`, `ExactArgs(1)`, flag
  `--reason` (StringVar). `RunE` → `runDeny(ctx, app, args[0], reason)`.
- `runDeny` → `rule, _ := ruleFromURL(rawURL, reason)`; target = project file;
  `mutateAndRecompile(..., DenyList, rule, "denied")`.

**Shared helpers (in `allow.go` or a small `mutate.go`):**
- `ruleFromURL(raw, reason string) (config.Rule, error)`:
  - `host, path := splitURL(raw)` — prepend `https://` if no scheme, `url.Parse`,
    `u.Hostname()`, `u.Path`. Empty/`"/"` path → no path (whole host).
  - Build `config.Rule{Host: host, Paths: pathsPtr(path), Reason: reason}`.
  - Error on empty host.
- `splitURL` extracted as the shared core; refactor `requestFromURL`
  (`internal/cli/policy.go:160-178`) to call it (it then maps empty path → `"/"` for
  matching, which differs from rule semantics).
- `allowTarget(app, dir string, once, global bool) string`:
  - `once` → `state.New(app.Paths).Resolve(dir)` → `Layout.SessionOverlay()`.
  - `global` → `config.NewLoader(app.FS, app.Paths).GlobalPath()` (or expose the path
    builder; reuse existing `GlobalPath()`).
  - else → `filepath.Join(dir, configFile)`.
- `mutateAndRecompile(ctx, app, dir, path string, list config.RuleList, rule config.Rule, verb string) error`:
  1. `data := app.FS.ReadFile(path)`; `fs.ErrNotExist` → `data = nil` (create on write).
  2. `out, changed, err := config.AppendRule(data, list, rule)`.
  3. `if !changed` → print `"already <verb> in <path> (no change)"`, return nil.
  4. `app.FS.MkdirAll(filepath.Dir(path), 0o755)` then `writeFileAtomic(app.FS, path, out, 0o644)`.
  5. Recompile: `c, _ := compile.New(app.FS, app.Paths, app.Clock, app.HTTP); c.Compile(ctx, dir)`.
     A non-nil compile error is surfaced (the write already landed) with a clear message.
  6. Print `"✓ <verb> <host[/path]> in <path>; policy recompiled"`.

**Register** both in `newRootCmd` (`internal/cli/cli.go:61-80`):
`root.AddCommand(newAllowCmd(app))`, `root.AddCommand(newDenyCmd(app))`.

### Tests (`internal/cli/allow_test.go`, `deny_test.go`)

Against `sysdeptest` fakes (model on `init_test.go`):
- `allow host/path` writes to the project file; the file now parses and contains the rule.
- `allow --global` writes the global path, leaves the project file untouched.
- `allow --once` writes the overlay path, NOT the project file.
- `allow --once --global` → error.
- `deny --reason "x"` writes a `deny_always` rule carrying the reason.
- Duplicate invocation → no-op message, second write not performed (assert FS write count
  or unchanged bytes), no recompile.
- Recompile invoked: after a mutation, the compiled `policy.json` on the fake FS contains
  the new rule (stronger than an mtime check; satisfies AC "recompiles policy.json").
- Use generator-free configs so `Compile` makes no network calls (HTTP fake unused).

### Success criteria

#### Automated
- [x] `go build ./...`
- [x] `go test -race ./internal/cli/...` green
- [x] `make lint` clean

#### Manual
- [x] Smoke test in a scratch dir: `allow api.github.com/repos/foo/` appends the rule
      (comment preserved) and reports recompile; `deny --reason` + `policy explain`
      shows hard-deny; `allow --once` writes the overlay, not the project config

---

## Phase 3 — End-to-end testscript + `--once` lifecycle

### Changes

**`internal/cli/testdata/script/allow_deny.txtar`** (hermetic; `PATH=$CREANCE_BIN`,
generator-free config so compile needs no host tools/network). Mirrors the ticket's
Verification & Test Steps:
- `--help` advertises flags; `ExactArgs(1)`/flag rejection.
- `allow api.github.com/repos/foo/` → `grep` the rule in `.agent-creance.yaml`; then
  `agent-creance policy show` shows it.
- `allow --global …` → rule lands in the global file (point `$HOME`/`XDG_*` at `$WORK`),
  `! grep` it in `.agent-creance.yaml`.
- `allow --once …` → rule in the overlay file, `! grep` it in `.agent-creance.yaml`,
  shows in `policy show`.
- `deny w3schools.com --reason "x"` → `deny_always` + reason present; `policy explain
  https://w3schools.com/` reports a hard-deny.
- Re-run an identical `allow` → "already allowed" no-op message.
- Assert comments in the starter config survive the edits (`grep` a known comment line).

**`--once` purge (AC criterion "verified end to end")** — implemented as a focused
*hermetic* test (`internal/cli/once_lifecycle_test.go`, no integration tag so it runs in
`make test`) wiring `proxy.NewManager` over the command's filesystem: `runAllow(--once)`
→ overlay present + rule in compiled policy → `Manager.Detach` as last agent → overlay
purged and a recompile drops the `once` rule. (AC-0020 already unit-tests the purge; this
ties the AC-0030 writer to it.)

### Success criteria

#### Automated
- [x] `go test -race ./internal/cli/...` (testscript + once-lifecycle) green
- [x] `go test -race -tags=integration ./internal/cli/...` green
- [x] `make test` and `make lint` clean repo-wide; `make golden` shows no drift

#### Manual
- [x] Walk the ticket's Verification steps 1–5 against the testscript output; mtime
      advance is satisfied structurally (the recompile rewrites `policy.json` via rename)
      and content-verified in Phase 2 + the once-lifecycle test

---

## Testing strategy

- **Pure splice/render** → golden (`testdata/edit/*`) + table tests in `internal/config`.
- **Command bodies** → unit tests against `sysdeptest` fakes (target resolution, flags,
  dup no-op, recompile-writes-policy).
- **CLI surface + acceptance steps** → hermetic testscript.
- **`--once` purge** → integration test crossing into `proxy.Manager.Detach`.
- No external tools in unit tests; compile fixtures stay generator-free to avoid network.

## Risks & mitigations

- **Splice edge cases** (commented-out key, missing sections, odd indentation) — mitigated
  by the validation gate (parse-back + rule-set diff) that refuses to write a bad edit, plus
  golden coverage of each structural case.
- **Recompile failure after a successful write** — surface the error clearly; the YAML
  change is still persisted and the next `run`/`policy show` will recompile. Acceptable.
- **Global/overlay parent dir missing** — `MkdirAll(dir(path))` before write.

## References

- Research: `thoughts/shared/research/2026-06-07-AC-0030-allow-deny-commands.md`
- Ticket: `thoughts/shared/tickets/AC-0030-allow-deny-commands.md`
- Design: `docs/design.md:301-305` (session-scoped allows), `:350-385` (commands)
