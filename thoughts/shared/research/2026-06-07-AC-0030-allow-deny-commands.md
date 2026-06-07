---
date: 2026-06-07
ticket: AC-0030
title: "Research: allow / deny commands (WP-6.1)"
status: complete
commit: a4ce8c24ea55fccfebfade551e3c4e09165a94df
branch: main
---

# Research: AC-0030 `allow` / `deny` commands (WP-6.1)

## Research question

How do we implement `agent-creance allow URL` (`--global`, `--once`) and
`agent-creance deny URL [--reason]` so they append rules to the correct config
target, recompile `policy.json`, and trigger the running proxy's hot-reload —
following the project's existing CLI, config, compile, and overlay patterns?

## Summary

Everything the commands depend on already exists; AC-0030 is purely additive glue:

1. **Write target resolution** — three targets, all already locatable:
   - project file `.agent-creance.yaml` (`configFile` const, `internal/cli/run.go:23`),
   - global file `~/.config/agent-creance.yaml` (`config.Loader.GlobalPath()`, `internal/config/load.go:77-83`),
   - session-overlay `~/.cache/agent-creance/projects/<hash>/session-overlay.yaml` (`state.Layout.SessionOverlay()`, `internal/state/state.go:204-206`).
2. **Rule shape** — `config.Rule{Host, Paths *[]string, Methods *[]string, Mode, Reason}` (`internal/config/config.go:77-83`), placed under `network.egress.allow` or `network.egress.deny_always`.
3. **Recompile + hot-reload** — call `compile.New(...).Compile(ctx, dir)`. Because the changed rule alters the compiler's input hash, the compile is **not** skipped, so it atomically rewrites `policy.json` (temp+rename), advancing its mtime. The Python enforcer polls mtime every 1s (`internal/proxy/enforcer/enforcer.py:56,138-147`) and reloads. **No explicit `touch`/`Chtimes` helper exists or is needed** — the rewrite *is* the touch.
4. **`--once` lifecycle** — the overlay is already a first-class compile layer (source `"once"`) and is **already purged** by AC-0020's `Manager.Detach` on last-agent-exit (`internal/proxy/lifecycle.go:165-178`). AC-0030 only *writes* the overlay; it does not implement purge.

The single non-trivial design decision is **how to append a rule to a hand-authored
YAML file while preserving its comments** — flagged as an open question for the
checkpoint (see [Open Questions](#open-questions-for-the-checkpoint)).

## Detailed findings

### Config schema & the Rule type (`internal/config/`)

`config.Config.Network.Egress` (`internal/config/config.go:52-71`) holds the two
rule lists this ticket appends to:

```go
type Egress struct {
	Generators []string `yaml:"generators"`
	Allow      []Rule   `yaml:"allow"`
	DenyAlways []Rule   `yaml:"deny_always"`
}
```

`Rule` (`internal/config/config.go:77-83`) is the **shared shape** for both an
allow and a deny entry:

```go
type Rule struct {
	Host    string    `yaml:"host"`
	Paths   *[]string `yaml:"paths"`
	Methods *[]string `yaml:"methods"`
	Mode    string    `yaml:"mode"`
	Reason  string    `yaml:"reason"`
}
```

- `Paths`/`Methods` are **pointers** so an omitted key (`nil`) is distinguishable
  from an explicit empty list. A bare-host allow → `Paths: nil`; a host+path allow
  → `Paths: &[]string{"/repos/foo/"}`.
- `Mode` defaults to `"intercept"` when empty (`defaultRuleModes`, `config.go:176-182`).
  An allow/deny command can leave `Mode` empty and let the compiler default it.
- A deny carries `Reason` (set by `--reason`); an allow leaves it empty.

**The config package is read/parse-only.** `config.Parse([]byte) (*Config, error)`
(`config.go:128-157`) strict-decodes (`KnownFields(true)`) — an unknown key fails
the compile. There is **no YAML serialization anywhere** in the tree, and
`yaml.Node`/`yaml.Marshal`/`yaml.Encoder` are **not used** — only `yaml.NewDecoder`
in `config.go` and `errors.go`. So AC-0030 introduces the first YAML *writer*.

### Config file locations & layering

- Project path: `filepath.Join(dir, ".agent-creance.yaml")`, const `configFile` at `internal/cli/run.go:23`.
- Global path: `Loader.GlobalPath()` = `filepath.Join(home, ".config", "agent-creance.yaml")` (`internal/config/load.go:77-83`); home comes from the injected `sysdep.PathResolver`.
- Overlay path: `state.New(app.Paths).Resolve(dir)` → `Layout.SessionOverlay()` (`internal/state/state.go:204-206`).

The compiler resolves the three layers **separately** for provenance
(`Compiler.resolve`, `internal/policy/compile/compile.go:178-227`) and annotates
rule sources `explicit` (project), `global`, `once` (overlay) (`compile.go:38-42`).
Union order in `buildRuleSet` (`compile.go:417-438`): global → project → generated
→ overlay, then `dedupe` (`compile.go:486-501`). Matching is order-independent
(most-specific-wins), so list order only affects the stable artifact.

### Compile + hot-reload mechanism

- **Compiler** `internal/policy/compile/compile.go`. Constructor `New(fs, paths, clock, getter)` (`compile.go:108`); entry `Compile(ctx, projectDir) (Result, error)` (`compile.go:232`).
- **Cache gate**: skips rewrite when `existing.Version == policy.CompiledVersion && existing.InputHash == in.hash` (`compile.go:240-241`). The **input hash** (`inputHash`, `compile.go:381-398`) is SHA-256 over the canonicalised global + project + **overlay** configs + manifest bytes. Any allow/deny/once mutation changes a hashed layer → cache miss → rewrite.
- **Write** (`write`, `compile.go:517-537`): `MkdirAll(Root)`, `WriteFile(dest+".tmp")`, `Rename(tmp, dest)` to `layout.PolicyJSON()` = `<cache>/agent-creance/projects/<hash>/policy.json`. The rename advances mtime.
- **Reload**: `enforcer.py` polls `os.path.getmtime` every `_POLL_INTERVAL_SECONDS = 1.0` (`enforcer.py:56,138-152`); on change it re-reads `policy.json`. Purely mtime-driven; no Go-side watcher.
- **Existing invocation sites**: `run` (`internal/cli/run.go:88-95`), `policy show`/`explain` via `resolvePolicy` (`internal/cli/policy.go:137-155`), `policy refresh` via `Compiler.Refresh` (`policy.go:41-45`). `resolvePolicy` already triggers a compile as a side effect — so `policy show` after a mutation recompiles anyway.

`internal/sysdep/filesystem.go:19-39` exposes `ReadFile, WriteFile, Stat, MkdirAll,
Remove, Rename` — sufficient; **no `Chtimes`/`Touch`**, and none is required.

### Session-overlay & AC-0020 purge (dependency — done)

AC-0020 is **implemented and Done** (`thoughts/shared/tickets/AC-0020-proxy-lifecycle-manager.md:3`). The overlay:

- Same schema as `.agent-creance.yaml`; parsed via `config.Parse` in `Compiler.loadOverlay` (`compile.go:325-338`); absent file → empty `Config`, not an error.
- Purged in `Manager.Detach` (`internal/proxy/lifecycle.go:152-182`): when the last agent PID is removed (`len(agents) == 0`, line 165) it SIGTERMs the proxy and calls `sysdep.RemoveIfPresent(m.fs, layout.SessionOverlay())` (lines 172-174). A non-final exit leaves the overlay intact.
- Today **no production code writes the overlay** — only tests create placeholder files. AC-0030 is the first writer.

### CLI command pattern to model

Model on `init` (the existing pure-filesystem mutating command) and `policy explain`
(flags + URL arg):

- `newXxxCmd(app *App) *cobra.Command` → register in `newRootCmd` (`internal/cli/cli.go:61-80`).
- Thin `RunE` closure delegating to a testable `runXxx(ctx, app, ...flags)` body (`internal/cli/setup.go:17-37`, `init.go:23-66`).
- Flags via `cmd.Flags().BoolVar`/`StringVar` (`policy.go:98-130`, `init.go:23-36`).
- File mutation through `app.FS` with the atomic temp+rename idiom `writeFileAtomic` (`internal/cli/init.go:99-111`).
- URL parsing precedent: `requestFromURL` (`internal/cli/policy.go:160-178`) — prepends `https://`, `url.Parse`, `u.Hostname()`, normalises empty path to `/` for **matching**. (See URL→rule note below — allow/deny want different empty-path semantics.)

### Testing patterns

- **testscript** `.txtar` under `internal/cli/testdata/script/`; harness `script_test.go` registers the CLI in-process and exposes `$CREANCE_BIN`. Model on `init.txtar` (pure-fs: `exists`, `grep`/`! grep` against written files, inline `-- file --` fixtures, `--help` flag assertions). The Verification steps in the ticket map directly to `.txtar` assertions.
- **Unit tests** for the `runXxx` body against `internal/sysdep/sysdeptest` fakes (`init_test.go`, `setup_test.go`).
- **Golden** if a YAML-mutation renderer is extracted as a pure function (mirrors `init`'s template approach), per the project's "generated artifacts → golden" convention.

## Code references

- `internal/config/config.go:52-83` — `Egress` + `Rule` (write target shape).
- `internal/config/config.go:128-182` — `Parse`, defaults (strict decode; mode default).
- `internal/config/load.go:77-83` — `GlobalPath()`.
- `internal/cli/run.go:23` — `const configFile = ".agent-creance.yaml"`.
- `internal/state/state.go:204-206` — `SessionOverlay()` path.
- `internal/policy/compile/compile.go:232,381-398,517-537` — `Compile`, `inputHash`, `write`.
- `internal/proxy/enforcer/enforcer.py:56,138-152` — mtime poll + reload.
- `internal/proxy/lifecycle.go:152-182` — overlay purge on last-agent-exit (AC-0020).
- `internal/cli/init.go:42-111` — pure-fs mutating command + atomic write idiom.
- `internal/cli/policy.go:98-178` — flag/arg + URL-parse precedent.
- `internal/cli/cli.go:20-110` — `App` struct + `newRootCmd` + `Main`.
- `docs/design.md:301-305,350-385` — Session-scoped allows; Commands.

## Architecture insights

- **The hand-authored file is sacred.** `docs/design.md:299` stresses the committed
  config is version-controlled human input. A writer that destroys comments on every
  `allow` would be poor UX — hence the comment-preservation question matters for the
  *committed* files (project/global), but **not** for the machine-managed overlay
  (no comments to keep).
- **No new sysdep seam needed.** Writes go through `app.FS`; compile/reload through
  the existing `compile` package. Mutation is live purely because the input hash
  changes — no separate touch step.
- **Idempotent-compile means "recompile after mutation" is safe & cheap** even when
  no proxy is running (the next `run` reads the fresh `policy.json`).

## Open Questions for the checkpoint

1. **YAML mutation strategy (comment preservation).** The committed files carry
   comments (and the `init` template ships with `allow:`/`deny_always:` *commented
   out*, `internal/cli/init.go:141-160`), and `network.egress.allow` may be absent
   on a fresh project. Three options:
   - **(A) `yaml.v3` Node editing** *(recommended)* — decode into `yaml.Node`,
     navigate to / create `network.egress.allow`|`deny_always`, append a mapping
     node, re-encode with `SetIndent(2)`. Robustly handles nested/missing keys and
     **preserves comments attached to nodes**. Known cosmetic caveats: yaml.v3
     collapses blank lines between top-level keys and may normalise quoting/indent.
   - **(B) Decode→struct→re-encode whole file** — simplest & golden-testable, but
     **destroys all comments**. Bad fit for the "sacred hand-authored file" stance.
   - **(C) Naive textual append** — brittle: must locate the nested `allow:` sequence
     at the right indent and create the structure when the key is commented-out or
     absent.

   For the **overlay** (`--once`), comments don't exist, so a simple struct re-encode
   is fine — but using **one code path (A) for all three targets** keeps it simple.

2. **`allow <repo-url>` forge expansion.** `docs/design.md:175` says a manual repo
   allow "expands to the same companion set" of forge content hosts (raw/codeload/
   pages/CDN) as the generators. AC-0030's acceptance criteria and test steps treat
   `allow host/path` as producing a **single** rule. *Recommendation: defer forge
   expansion to a follow-up ticket; AC-0030 appends one rule per invocation.* Confirm.

3. **URL→rule mapping semantics (minor — proposing a default).** Mirror
   `requestFromURL`'s scheme-prepend + `url.Parse` + `Hostname()`, but: a **bare host**
   → `Paths: nil` (whole host); a **host+path** → `Paths: &[]string{path}`. No
   `--method` flag (design omits it), so `Methods: nil` (all methods). Share only a
   small `splitURL(raw) (host, path string)` helper with `requestFromURL` rather than
   reusing it wholesale (their empty-path handling differs: matcher wants `"/"`, rule
   wants nil). *Proposed as default unless you object.*

4. **Exact-duplicate handling (minor — proposing a default).** The compiler dedupes,
   so a duplicate rule is harmless. *Proposed: detect an exact duplicate in the target
   file and report "already allowed/denied" as a no-op (no rewrite, no recompile)
   rather than silently appending a dup.* Confirm or simplify to always-append.
