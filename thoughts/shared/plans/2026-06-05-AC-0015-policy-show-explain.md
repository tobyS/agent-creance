---
date: 2026-06-05
ticket: AC-0015
title: "Plan: `policy show` / `policy explain` commands (WP-2.6)"
status: ready
branch: main
research: thoughts/shared/research/2026-06-05-AC-0015-policy-show-explain.md
tags: [plan, cli, policy, matcher, visibility]
---

# Plan: `policy show` / `policy explain` commands (WP-2.6)

## Overview

Add `agent-creance policy show` and `agent-creance policy explain URL`, the policy
**visibility** layer over the existing matcher (AC-0010) and compiler (AC-0013). Both
commands **compile-on-demand** (the cached, idempotent compiler) and then render the
resulting `policy.Compiled`:

- `policy show` dumps every allow/deny rule with its `[source]` annotation, its mode,
  and distinct flags for `passthrough` (audit blind spot) and `lower_trust` rules.
- `policy explain URL` parses the URL into a `policy.Request`, runs the **shared**
  matcher (`RuleSet.Decide` — cross-cutting **C1**, never reimplemented), and reports
  the decision + carried mode + the matched rule and its source.

Both support a `--json` mode; `explain` takes the HTTP method from a `--method` flag
(default `GET`). All text output is golden-tested (**C3**) and the commands are covered
end-to-end by hermetic `testscript` scenarios.

### Decisions locked at the research checkpoint

1. **Compile-on-demand** (not read-only): the command runs `compile.Compiler.Compile`
   (cached) and renders the artifact, so it is self-sufficient before `run`/`policy
   refresh` exist. `App` grows the FS/path/clock/HTTP seams.
2. **`--json` included now** on both commands.
3. **`explain` input** = one URL positional arg + `--method` flag (default `GET`).

## Current state

- Matcher `internal/policy`: `RuleSet.Decide(Request) Result`; `Rule` already carries
  `Source`/`LowerTrust` for rendering; decision/mode constants fixed
  (`policy.go:50-62`). `MatchedRule{List, Index}` indexes back into the rule lists;
  nil for soft-deny.
- Compiler `internal/policy/compile`: `New(fs, paths, clock, getter)` →
  `Compile(ctx, projectDir) (Result, error)`; writes the source-annotated
  `policy.Compiled` to `state.Layout.PolicyJSON()`; cached by input hash. `Result` has
  `PolicyPath` but not the `Compiled` itself.
- CLI `internal/cli`: `App{Commander, Stdout, Stderr, Tested}` composition root
  (`cli.go:20-26`); commands registered in `newRootCmd`; **only `Main()` constructs an
  `App`** (safe to grow). No `policy` command and no parent-with-subcommands pattern
  exists yet.
- Render/golden idiom: `prereq.Report` (manual `%-*s` columns into a `strings.Builder`,
  glyphs as constants) + `report_test.go` `-update` golden pattern.
- testscript: `script_test.go` maps `agent-creance`→`Main()`, runs
  `testdata/script/*.txtar`, exposes `$CREANCE_BIN`.
- sysdep real impls: `OSFileSystem{}`, `OSPathResolver{}`, `OSClock{}`,
  `OSHTTPGetter{}`.

## Desired end state

```
$ agent-creance policy show
Resolved egress policy (6 allow, 2 deny)

ALLOW
  [global]                        passthrough  api.anthropic.com   (any path)  ⚠ passthrough (audit blind spot)
  [explicit]                      intercept    api.github.com      /repos/tobyS/x/  (GET, POST)
  ...
  [generated:package_json:react]  intercept    objects.githubusercontent.com  (any path)  ⚠ lower-trust

DENY
  [global]    w3schools.com  (any path)  (reason: low quality)
  [explicit]  *              **/.env     (reason: secrets)

$ agent-creance policy explain https://github.com/facebook/react/pulls
Request:   github.com GET /facebook/react/pulls
Decision:  allow (intercept)
Matched:   allow[3]  [generated:package_json:react]  github.com /facebook/react/

$ agent-creance policy explain https://evil.test/
Request:   evil.test GET /
Decision:  soft-deny
Matched:   (none — not in the allowlist)

$ agent-creance policy show --json     # emits policy.Compiled
$ agent-creance policy explain --method POST https://api.github.com/repos/tobyS/x/ --json
```

Exact bytes (spacing, markers) are locked in golden files and reviewed via
`make golden`; the structure above is the contract.

## What we are NOT doing

- No `policy refresh` (AC-0016) — `show`/`explain` compile via the cache; they do not
  force a rebuild.
- No mutation (`allow`/`deny`, AC-0030).
- No new matcher behavior or decision vectors — WP-2.6 is pure rendering; `explain`
  calls the existing `Decide` unchanged (C1).
- No `network.sb`/host-service info in `policy show` (different concern; AC-0014).

---

## Phase 1 — Renderer package (`internal/policy/render`)

A pure, side-effect-free rendering package (mirrors the `internal/policy/compile`
sibling layout and the `prereq.Report` "pure string renderer" idiom). It imports
`internal/policy` for the types and `Decide`; no FS/clock/OS.

### Files

- `internal/policy/render/render.go`
- `internal/policy/render/render_test.go`
- `internal/policy/render/testdata/*.golden`

### `render.go` — API

```go
package render

// Show renders the resolved policy as annotated aligned text.
func Show(c policy.Compiled) string

// ShowJSON re-emits the compiled artifact as indented JSON (+ trailing newline).
func ShowJSON(c policy.Compiled) (string, error)

// Explain runs the shared matcher on req against c and renders the decision,
// carried mode, and the matched rule + its source (or the soft-deny "none" case).
func Explain(c policy.Compiled, req policy.Request) string

// ExplainJSON returns the structured explanation: request, decision, mode, and the
// resolved matched rule (nil for soft-deny).
func ExplainJSON(c policy.Compiled, req policy.Request) (string, error)
```

Implementation notes:

- **`Show`**: header line with allow/deny counts; an `ALLOW` block then a `DENY` block.
  Two-pass column alignment (compute max `[source]`-tag width and host width, then
  `%-*s`), exactly like `prereq.Report`. For each rule render: `[source]`, mode (allow
  block only), host, path-or-`(any path)`, methods `(GET, POST)` if present, and
  trailing markers. **Passthrough marker** (`Rule.Mode == policy.ModePassthrough`) and
  **lower-trust marker** (`Rule.LowerTrust`) are package-constant glyph+label strings
  so code and golden agree byte-for-byte (the design L245 "flag passthrough distinctly"
  requirement). Deny rules append `(reason: …)`.
- **`Explain`**: build the `Matched`-resolution helper that, given `*MatchedRule`,
  indexes `c.Allow`/`c.DenyAlways` and returns the rule for display. Render
  `Request`, `Decision (mode)`, and `Matched: list[idx] [source] host path-or-(any
  path) [(reason: …)]`. Soft-deny → `Decision: soft-deny` + `Matched: (none — not in
  the allowlist)`. When the decision is `allow` and mode is `passthrough`, append a
  `Note:` line explaining the blind spot (path/method not inspected; host-level audit
  only) — faithful to `Decide`'s real behavior.
- **`ExplainJSON`**: marshal a struct `{request, decision, mode, matched}` where
  `matched` is the resolved rule (host/paths/methods/mode/reason/source/lower_trust +
  list/index) or `null` for soft-deny.
- Keep all glyphs/labels/headers as `const`s.

### `render_test.go` — golden tests (`-update` flag)

- A shared fixture `policy.Compiled` mirroring
  `internal/policy/compile/testdata/policy.golden` (passthrough global, explicit
  host+path+method, two generated react rules, a lower-trust generated rule, a `once`
  rule, host-wide global deny, `**/.env` explicit deny). Decode that very file or
  inline an equivalent literal.
- `TestShow` → `testdata/show.golden`; `TestShowJSON` → `testdata/show.json.golden`.
- `TestExplain` table: cases for **allow** (path match), **allow-passthrough**
  (note line), **hard-deny by host** (reason), **soft-deny** (none) → one golden per
  case (`testdata/explain_<case>.golden`) or a single combined golden.
- `TestExplainJSON` for at least an allow and a soft-deny → `*.json.golden`.
- Follow `report_test.go`: `var update = flag.Bool("update", …)`; `if *update {
  WriteFile; return }` else `ReadFile` + `require.Equal`.

### `render_vectors_test.go` — C1 consistency (ticket verification step 3)

Guard that `Explain`'s rendered decision never diverges from the matcher. Load every
JSON file under `../../testdata/decision-vectors/` (the same corpus AC-0010 uses),
wrap each vector's `RuleSet` in a `policy.Compiled`, call `Explain`, and assert the
rendered decision token equals `vector.Ruleset.Decide(req).Decision`. (Parse the
decision out of the rendered text, or — cleaner — have `Explain` delegate to a small
unexported `decide`+`render` split and assert on the structured `ExplainJSON` decision
field against `Decide`.) Fail on an empty corpus, mirroring `vectors_test.go`.

### Success criteria

- [ ] `go test ./internal/policy/render/...` green; goldens created via `-update` and
  reviewed.
- [ ] C1 vector test passes for the full corpus.
- [ ] `go vet ./internal/policy/render/...` clean.

---

## Phase 2 — Grow the `App` composition root

### Changes — `internal/cli/cli.go`

- Add to `App`: `FS sysdep.FileSystem`, `Paths sysdep.PathResolver`,
  `Clock sysdep.Clock`, `HTTP sysdep.HTTPGetter` (doc each briefly, matching the
  existing field comments).
- In `Main()`, wire the real impls: `OSFileSystem{}`, `OSPathResolver{}`, `OSClock{}`,
  `OSHTTPGetter{}`.
- Register the new command: `root.AddCommand(newPolicyCmd(app))`.

No other construction site exists, so nothing else needs updating. Existing
testscript/`Main`-based tests keep working.

### Success criteria

- [ ] `go build ./...` compiles.
- [ ] `make test` still green (no behavior change yet).

---

## Phase 3 — The `policy` command tree (`internal/cli/policy.go`)

### Files

- `internal/cli/policy.go`

### Structure

```go
func newPolicyCmd(app *App) *cobra.Command   // Use: "policy"; no RunE (prints help); adds children
func newPolicyShowCmd(app *App) *cobra.Command    // Args: NoArgs; flag --json
func newPolicyExplainCmd(app *App) *cobra.Command  // Args: ExactArgs(1); flags --json, --method GET
```

- `newPolicyCmd` builds the parent and `cmd.AddCommand(newPolicyShowCmd(app),
  newPolicyExplainCmd(app))`.
- **Shared compile-on-demand helper** (in this file):

  ```go
  func resolvePolicy(ctx context.Context, app *App, dir string) (policy.Compiled, error)
  ```

  It constructs `compile.New(app.FS, app.Paths, app.Clock, app.HTTP)`, calls
  `Compile(ctx, dir)`, then `app.FS.ReadFile(result.PolicyPath)` and
  `json.Unmarshal` into `policy.Compiled`. (Re-reading the artifact covers both the
  cache-hit and freshly-written paths uniformly.) `dir` is `"."` — the process cwd is
  the project, and `state.Resolve` makes it absolute+canonical.

- **`show` RunE**: `c, err := resolvePolicy(cmd.Context(), app, ".")`; on `--json`
  write `render.ShowJSON(c)` else `render.Show(c)` via `app.Stdout`.
- **`explain` RunE**: parse `args[0]` with `net/url.Parse` (if it has no scheme,
  prepend `https://` so `Host`/`Path` populate; treat empty path as `/`); build
  `policy.Request{Host: u.Hostname(), Path: u.Path or "/", Method: methodFlag}`;
  `c, err := resolvePolicy(...)`; write `render.Explain`/`render.ExplainJSON`.
- Errors (no project config, parse failure, compile failure) are returned from `RunE`
  so `Main` prints `error: …` and exits 1. Give the "no project config" path a clear
  message (it surfaces from the compiler's required-project-layer load).

### Success criteria

- [ ] `go build ./...` compiles; `agent-creance policy show`/`explain` run.
- [ ] `make lint` clean.

---

## Phase 4 — testscript coverage + final verification

### Files

- `internal/cli/testdata/script/policy_show.txtar`
- `internal/cli/testdata/script/policy_explain.txtar`

### Scenarios (hermetic — no generators, so no network)

Each script:

- `env HOME=$WORK/home` and `env XDG_CACHE_HOME=$WORK/cache` so global config is absent
  (optional) and the compiled artifact lands under `$WORK`.
- Embed a project `.agent-creance.yaml` at the bottom (`-- .agent-creance.yaml --`)
  with **no `generators:`** — explicit allow/deny rules and a passthrough host — so the
  compiler runs fully offline.

`policy_show.txtar`:

- `agent-creance policy show`; assert `stdout` contains `[explicit]`, the passthrough
  host + passthrough marker, a deny `(reason: …)`. `! stderr .`.
- `agent-creance policy show --json`; assert `stdout` contains `"version": 1` and
  `"source": "explicit"`.

`policy_explain.txtar`:

- `agent-creance policy explain https://<allowed-host>/<path>`; assert `Decision:
  allow` and the matched `[explicit]` line.
- `agent-creance policy explain https://evil.test/`; assert `Decision:  soft-deny` and
  `(none`.
- `agent-creance policy explain --method POST https://<host>/<path>` to exercise the
  flag.
- A deny URL → `Decision:  hard-deny` with the reason.
- Optionally `--json` assertion (`"decision"`).
- An invocation in a dir **without** `.agent-creance.yaml` (e.g. a subdir, or a script
  with no embedded config) → leading `!` (non-zero exit) + `stderr 'error'`.

### Final verification (run all)

1. `go build ./...` → compiles.
2. `make golden` → review the render goldens diff; confirm intended bytes.
3. `make test` → green (includes the render goldens, C1 vector test, and both txtar
   scenarios).
4. `make lint` → clean.

### Success criteria

- [ ] `policy_show.txtar` + `policy_explain.txtar` pass.
- [ ] `make test` green; `make lint` clean; `make golden` diff reviewed.
- [ ] Ticket acceptance criteria all satisfied (see below).

---

## Acceptance-criteria traceability

- *“`policy show` lists every rule with `[explicit]`/`[generated:…]`/`[global:…]`
  annotation; passthrough rules visibly flagged”* → Phase 1 `Show` + passthrough marker;
  Phase 4 show scenario. (Sources render as the compiler's real strings:
  `explicit`/`global`/`once`/`generated:…` + `lower_trust` — per the research grounding
  note.)
- *“`policy explain URL` prints the decision + mode and the matching rule + source,
  consistent with AC-0010's matcher (C1)”* → Phase 1 `Explain` via `Decide` + Phase 1
  C1 vector test; Phase 3 URL/method parsing.
- *“Output is stable and golden-tested (C3)”* → Phase 1 goldens; Phase 4 `make golden`.
- Verification steps 1–5 in the ticket → Phase 4 final verification.

## Testing strategy summary

- **Pure rendering** → golden-file tests with `-update` (`internal/policy/render`).
- **C1 parity** → decision-vector corpus replay asserting `Explain` == `Decide`.
- **CLI behavior** → hermetic `testscript` `.txtar` (no generators → offline), using
  `XDG_CACHE_HOME`/`HOME` under `$WORK`.
- No unit tests construct `App` directly (only `Main` does), so growing it is safe.
- External tools never invoked; the compile path uses only FS/path/clock seams with a
  generator-free config, so no HTTP occurs in tests.

## Risks / notes

- **Byte-exact markers** for passthrough/lower-trust are a free design choice (design
  L245 mandates only "distinct"); lock them in golden and review via `make golden`.
- **`explain` URL without scheme**: prepend `https://` before parsing so a bare
  `host/path` still yields a host; document this in the flag/command help.
- **`resolvePolicy` re-reads the artifact** rather than having the compiler return the
  `Compiled`; if a future refactor adds `Compile`→`Compiled` it can swap in, but
  re-reading keeps this change scoped to `internal/cli`.
</content>
