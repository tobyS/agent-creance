---
date: 2026-06-05
ticket: AC-0015
title: "Research: `policy show` / `policy explain` commands (WP-2.6)"
status: complete
branch: main
commit: f440b4e578314d62da087ce390cf5016cd23ee83
tags: [research, cli, policy, matcher, visibility]
---

# Research: `policy show` / `policy explain` commands (WP-2.6)

## Research question

How do we add `agent-creance policy show` (dump the fully-resolved egress policy
with per-rule source annotations + a distinct passthrough flag) and `agent-creance
policy explain URL` (report the decision + matching rule + source for a URL), built
on the existing matcher (AC-0010) and compiler (AC-0013), golden-tested and
hermetically `testscript`-tested?

## Summary

Everything `policy show`/`explain` needs already exists as a library:

- **The matcher** (`internal/policy`) is a pure `RuleSet.Decide(Request) Result`. Its
  `Rule` struct *already* carries `Source` and `LowerTrust` fields that "exist so
  `policy show` can render an annotated artifact" (`policy.go:69-71`). `explain` must
  call this matcher directly — cross-cutting **C1** forbids reimplementing the
  decision logic, because the same logic runs in Python in the proxy enforcer
  (AC-0017) and the two must never disagree.
- **The compiler** (`internal/policy/compile`) unions global + explicit + generated +
  session-overlay rules into a versioned, source-annotated `policy.Compiled` written
  to the project's out-of-tree `policy.json`. It is idempotent and input-hash-cached.
- **The CLI** (`internal/cli`) is a cobra tree with a small `App` composition root.
  There is **no `policy` parent command yet** and **no parent-with-subcommands
  pattern** anywhere — AC-0015 introduces the first one.

The work is therefore almost entirely a **rendering layer** plus wiring: a pure
renderer (golden-tested like `prereq.Report`), a `policy` parent command with `show`
and `explain` children, and a decision about whether the commands read an existing
`policy.json` or compile-on-demand (the one real architectural choice — see Open
Questions).

## The data the commands render

### Decision / mode / source vocabularies (fixed, already in the code)

Decision constants — `internal/policy/policy.go:50-54` (exhaustive):

```go
DecisionAllow    = "allow"
DecisionSoftDeny = "soft-deny"
DecisionHardDeny = "hard-deny"
```

Mode constants — `internal/policy/policy.go:59-62`:

```go
ModeIntercept   = "intercept"
ModePassthrough = "passthrough"
```

Source strings the compiler stamps — `internal/policy/compile/compile.go:38-42`:

- `explicit` — a project YAML rule (project file or its includes)
- `global` — the implicit `~/.config/agent-creance.yaml` baseline
- `once` — a session-overlay `allow --once` rule
- `generated:<gen>:<pkg>` — free-form, built by `generator.source`
  (`internal/generator/generator.go:111-115`), e.g. `generated:package_json:react`,
  `generated:composer_json:laravel/framework`

Plus a boolean `lower_trust` flag (e.g. `objects.githubusercontent.com`), set by
generators, present in the artifact.

> **Grounding note:** the design.md sample (L192-199) shows decorated tags like
> `[global:claude-defaults]`, but the **real compiler emits the bare strings** above
> (`explicit` / `global` / `once` / `generated:…`). The golden artifact
> `internal/policy/compile/testdata/policy.golden` is authoritative. Render the
> compiler's actual `source` string verbatim — do **not** invent `:claude-defaults`.

### The on-disk artifact shape

`policy.Compiled` (`internal/policy/policy.go:98-102`) serializes flat as
`{version, input_hash, allow, deny_always}`, where each rule is:

```go
type Rule struct {
	Host       string   `json:"host"`
	Paths      []string `json:"paths,omitempty"`
	Methods    []string `json:"methods,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Source     string   `json:"source,omitempty"`
	LowerTrust bool     `json:"lower_trust,omitempty"`
}
```

The real golden artifact (`internal/policy/compile/testdata/policy.golden`) is the
ideal fixture for `policy show`'s golden test — it exercises passthrough (`global`),
explicit host+path+method, two `generated:package_json:react` rules, a `lower_trust`
rule, an `once` rule, and two `deny_always` rules (one host-wide `global`, one
`**/.env` `explicit` with a reason).

### Where the artifact lives

`state.Layout.PolicyJSON()` → `<cache>/agent-creance/projects/<hash>/policy.json`
(`internal/state/state.go:155`). The hash is the first 8 bytes of SHA-256 of the
canonical (realpath-resolved) project dir; cache root honours `XDG_CACHE_HOME` else
`$HOME/.cache` (`state.go:96-99, 143-152`). All path/FS access goes through the
`sysdep` seams — a `testscript` test can point `XDG_CACHE_HOME` at `$WORK` and drop a
fixture `policy.json` at the resolved path, or run the compiler over a project config.

## The matcher contract for `explain`

`RuleSet.Decide(req Request) Result` (`internal/policy/match.go:15-42`):

- `Request{Host, Path, Method string}` in; `Result{Decision, Mode, Matched *MatchedRule}`
  out.
- `MatchedRule{List string, Index int}` — `List` is `"allow"` or `"deny_always"`,
  `Index` indexes back into `Compiled.Allow` / `Compiled.DenyAlways` to recover the
  responsible rule's `Host/Paths/Source/LowerTrust` for display.
- **`Matched` is nil for `soft-deny`** — there is no responsible rule; `explain` must
  handle "nothing matched" explicitly.
- **Passthrough blind spot** (`match.go:17-32`): a path-scoped `deny_always` is
  *suppressed* when the host's most-specific allow is `passthrough`. `explain` must
  report `Decide`'s real outcome faithfully (it may show `allow` where a naive reader
  expected `hard-deny`) — that is the whole point of being consistent with the proxy.

For `explain URL`, a URL has no method, so a `Request.Method` must come from somewhere
(a `--method` flag, default `GET`) — see Open Questions. Host/path come from
`net/url.Parse`.

## CLI patterns to follow

- **Command constructor + registration**: `newXxxCmd(app *App) *cobra.Command`,
  registered in `newRootCmd` via `root.AddCommand(...)` (`internal/cli/cli.go:35-48`).
  For `policy`, build a `policy` parent (`Use: "policy"`, no `RunE` / prints help) and
  `cmd.AddCommand(newPolicyShowCmd(app), newPolicyExplainCmd(app))`, then
  `root.AddCommand(newPolicyCmd(app))`. No existing parent-with-children to copy — this
  is the first; cobra handles it natively.
- **Output discipline**: write through `app.Stdout`/`app.Stderr` via `fmt.Fprint*`
  (never `fmt.Println`), so `testscript` captures it. Keep rendering in a pure logic
  function that returns a `string`; the command just calls it and `Fprint`s
  (`doctor.go:25` → `prereq.Report`).
- **Args**: `cobra.NoArgs` for `show`; `cobra.ExactArgs(1)` for `explain`
  (`args[0]` = URL).
- **Errors**: `RunE` returns `error`; `Main` prints it and exits 1
  (`cli.go:62-67`). Root sets `SilenceUsage`/`SilenceErrors`.

### Rendering idiom (aligned columns)

No `text/tabwriter` anywhere — alignment is manual: render into a `strings.Builder`,
compute max column widths in a first pass, format rows with `%-*s`
(`internal/prereq/report.go:20-48`). Keep marker/label glyphs as package constants so
code and golden agree byte-for-byte (`report.go:10-14`). The new renderer (e.g.
`policy.Render(Compiled) string` and `policy.Explain(...) string`, or a small
`internal/cli`-local renderer) should mirror this exactly.

### Golden test idiom

`var update = flag.Bool("update", false, "regenerate golden files")` at package scope;
`if *update { os.WriteFile(golden, got); return }` else `os.ReadFile` +
`require.Equal(string(want), got)`; goldens in `testdata/*.golden`; regen via
`make golden` (`internal/prereq/report_test.go:21-48`). **C3** explicitly names
`policy show` output as golden-tested.

### testscript idiom

`TestMain` maps `agent-creance` → `cli.Main()`; `TestScripts` runs
`testdata/script/*.txtar`; `Setup` exposes `$CREANCE_BIN`
(`internal/cli/script_test.go`). A scenario sets a minimal `PATH`/env, optionally
embeds files at the bottom (`-- path --`), and asserts with `stdout 'regexp'` /
`! stdout` / leading `!` for non-zero exit (`doctor_healthy.txtar`,
`doctor_missing.txtar`). For `policy`, set `env XDG_CACHE_HOME=$WORK/cache` and either
seed a `policy.json` fixture or run the compiler over an embedded `.agent-creance.yaml`.

## The composition-root impact

`App` currently holds only `{Commander, Stdout, Stderr, Tested}` (`cli.go:20-26`). To
read or compile a policy the commands need filesystem + path + (for compile)
clock/HTTP seams:

- **Read-only render** of an existing `policy.json` needs `sysdep.FileSystem` +
  `sysdep.PathResolver` (to resolve the project dir → `state.Layout.PolicyJSON()`).
- **Compile-on-demand** needs all of the above plus `sysdep.Clock` and
  `sysdep.HTTPGetter`, because `compile.New(fs, paths, clock, getter)` constructs the
  real generator runner (`compile.go:96-118`).

Either way `App` grows fields, wired with the real `sysdep` impls in `Main()` and
fakes from `internal/sysdep/sysdeptest` in unit tests. This mirrors how `Compiler`
already takes its seams.

## How AC-0014 (network.sb) relates — and doesn't

The Seatbelt profile compiler (`internal/profile`, AC-0014) does **not** consume the
egress policy model. It renders loopback-port allow lines from
`cfg.Network.HostServices`, a different concern (which ports the cage may reach, not
which hosts the proxy allows). `policy show`/`explain` operate purely on the egress
`policy.Compiled`. Mentioned only to avoid conflating the two — there is no shared code
to reuse here beyond the golden/testscript discipline both follow (C3).

## Open questions (for the planning checkpoint)

1. **Source of truth: read existing `policy.json`, or compile-on-demand?**
   - *Read-only render* is the simplest and most hermetic, but **today nothing
     produces a `policy.json`** in normal use — no `run` and no `policy refresh`
     (AC-0016, out of scope) exist yet, so `show` would usually have nothing to show
     and would need to error "no compiled policy; run …".
   - *Compile-on-demand* (call `compile.Compiler.Compile`, which is cached/idempotent,
     then render) makes the commands self-sufficient and matches M1's "produce correct
     annotated output from a real project config (with generators), fully offline
     against cached metadata." Cost: `App` gains clock+HTTP seams, and a cache *miss*
     would trigger generator network fetches during `show`. (AC-0016 `policy refresh`
     would then be the "force recompile / show compile stats" command, distinct from
     `show`'s cached compile.)

2. **`--json` output mode for v0.1, or defer?** The ticket flags this explicitly. The
   design sample is plain aligned text only; the artifact is already JSON on disk, so a
   `--json` flag could near-trivially re-emit `policy.Compiled`. Lean: defer unless
   wanted, to keep the golden surface to the annotated text the spec requires.

3. **`explain` input shape.** Proposal: accept a URL (`net/url.Parse`; tolerate a bare
   `host/path` by defaulting the scheme), take the HTTP method from a `--method` flag
   defaulting to `GET` (a URL carries no method, but the matcher matches on it). Confirm
   this UX, especially the default-GET-via-flag decision.

4. **Passthrough + lower_trust visual markers.** design.md L245 only requires
   passthrough rules be "flagged distinctly … the same way generated rules are
   annotated" — it gives no exact glyph/text. We design the markers (e.g. a
   `passthrough`/`lower-trust` tag column) and lock them in the golden. Not a
   human-judgment blocker; noted so the golden choice is deliberate.

## Key references

- Matcher: `internal/policy/policy.go`, `internal/policy/match.go`, `glob.go`
- Compiler + source vocab: `internal/policy/compile/compile.go`,
  `internal/policy/compile/testdata/policy.golden`
- Generator source format / lower_trust: `internal/generator/generator.go:111-115`,
  `internal/generator/rule.go`
- CLI tree / composition root: `internal/cli/cli.go`, `doctor.go`, `version.go`
- Rendering + golden idiom: `internal/prereq/report.go`, `internal/prereq/report_test.go`
- testscript harness: `internal/cli/script_test.go`,
  `internal/cli/testdata/script/doctor_*.txtar`
- State layout: `internal/state/state.go`
- sysdep seams: `internal/sysdep/{filesystem,pathresolver,clock,http}.go`,
  fakes in `internal/sysdep/sysdeptest`
- Design: `docs/design.md` L154-247 (generators/modes Visibility), L366-373 (commands)
- Spec: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`
  L94-107 (C1/C3), L201-211 (WP-2.6, M1)
</content>
</invoke>
