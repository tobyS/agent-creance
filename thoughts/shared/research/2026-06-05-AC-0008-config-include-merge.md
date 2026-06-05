---
date: 2026-06-05
ticket: AC-0008
topic: "Include resolution & merge semantics (WP-1.3) — internal/config"
status: complete
branch: main
git_commit: 1f66fb50202f3669b29718abce725ddd575ecb48
researcher: Claude (tce:work)
---

# AC-0008 — Include resolution & merge (WP-1.3): Research

## Research question

Extend `internal/config` so it resolves the implicit global
(`~/.config/agent-creance.yaml`) plus recursive `include:` directives into one
effective `Config`, with cycle detection, a depth limit, scalar-override semantics,
and additive union of `allow`/`deny_always`. What patterns/seams already exist, what
exactly must the merge do, where does the file-reading boundary go, and what
decisions need resolving before planning?

## Summary of findings

- **AC-0007 built the pure parser; AC-0008 builds the layered loader on top.**
  `config.Parse(data []byte) (*Config, error)` already strict-decodes + validates one
  document and is deliberately filesystem-free (`internal/config/config.go:1-16`,
  `:128`). AC-0008 adds a `Loader` that *reads* files, resolves `include:`, merges,
  and returns one effective `Config`. `Parse` stays unchanged and pure.
- **The merge rules are fixed by `docs/design.md:151`** (verbatim): *"`include:`
  resolution is recursive with cycle detection and a depth limit; later files override
  earlier ones for scalar fields, while `allow:` and `deny_always:` lists union
  additively."* The implicit global is the outermost baseline; per-project configs are
  deltas over it (`docs/design.md:28,151`).
- **There is no file-content I/O seam yet — AC-0008 must introduce a minimal one.**
  `internal/sysdep/pathresolver.go:8-19` explicitly defers "a future `FileSystem`
  interface" and says PathResolver only *locates* paths, never reads them. The broad
  seam set (Clock/FileSystem/Keychain/Flock/ProcessGroup) is nominally WP-1.4
  (AC-0009), but **WP-1.3 (AC-0008) is sequenced before WP-1.4 and is the first
  consumer of file reads**, and AC-0008's `Depends on:` lists only AC-0007. So AC-0008
  adds a narrow `sysdep.FileSystem{ ReadFile }` now (real impl + fake), which AC-0009
  later *grows* (write/stat/mkdir/remove/rename). This is consistent with the project's
  "small interfaces, grown at the point of need" idiom (`internal/sysdep/commander.go:20-23`).
- **Home-dir + canonicalisation seams already exist.** `sysdep.PathResolver` provides
  `UserHomeDir()` (locate the implicit global), and `Abs` + `EvalSymlinks` (canonical
  identity for cycle detection so symlink-aliased includes collapse). Both have fakes
  in `internal/sysdep/sysdeptest/pathresolver.go`.
- **No `cli.App` wiring is required by this ticket.** Nothing invokes the loader yet
  (the `run` command is AC-0025), exactly as AC-0007 shipped `Parse` unwired. AC-0008
  delivers the `Loader` + seam + hermetic unit tests.
- **Two ticket questions + one larger gap need decisions:** the depth-limit value &
  configurability; whether duplicate identical rules collapse or are kept; and — the
  big one — **the design only pins `allow`/`deny_always` as union and "scalars" as
  override, leaving the merge rule for the *other* collections (`safehouse.add_dirs_*`,
  `enable`, `generators`, `host_services`, `env`, `agent.command`) unspecified.** See
  "Open decisions".

## What exists today (with code references)

### The parser AC-0008 builds on
- `internal/config/config.go:29-100` — the typed `Config` tree (`Agent`, `Safehouse`,
  `Include []string`, `Network`/`Egress`/`Rule`, `Env map[string]string`) and its
  strict-decode mirror `rawConfig`. Every top-level section is optional (a delta config
  or an include-only baseline both parse).
- `internal/config/config.go:128-157` — `Parse`: strict decode (`KnownFields(true)`) →
  `applyDefaults` (rule `mode` → `intercept`, host_services string→typed) → `validate`.
  Returns `*Config` or a stable `*ValidationError`. **Filesystem-free by design.**
- `internal/config/validate.go:12-43` — per-rule validation (host present, mode ∈
  {intercept, passthrough}, passthrough⊕paths/methods). Rules are validated
  *per file*; this matters because merge concatenates already-valid rules — **the merged
  result needs no re-validation** (concat preserves rule validity; scalar override picks
  an already-valid scalar).
- `internal/config/errors.go:15-30` — `ValidationError{ Issues []string }` with a
  stable, type-name-free `Error()` (golden-tested). New include/cycle/depth/IO errors
  are a *different* category (not schema issues) and should be their own wrapped errors
  / sentinels (see "Proposed shape").
- `Rule.Paths`/`Methods` are `*[]string` (`config.go:77-83`) — pointer-distinguished
  omitted-vs-empty. **Merge must preserve the pointers as-is** (concat copies the
  `Rule` value, including the pointer); it must not deref/normalise them.

### The seams to inject
- `internal/sysdep/pathresolver.go:20-33` — `PathResolver` with `Abs`,
  `EvalSymlinks`, `UserHomeDir`, `Getenv`. Real `OSPathResolver` (`:40-58`) + fake
  `FakePathResolver` (`sysdeptest/pathresolver.go:10-68`, scriptable `Symlinks`,
  `HomeDir`, `Env`, `*Err` fields). `EvalSymlinks` requires the target to exist — fine,
  we canonicalise a file *after* confirming we can read it.
- `internal/sysdep/commander.go:24-51` — the seam template to copy for `FileSystem`:
  tiny interface, empty-struct OS impl, compile-time `var _ Iface = (*Impl)(nil)`
  assertion. Fake template: `sysdeptest/fake.go:18-63` (exported maps, `New…`
  constructor, `Errs` map to force failures).

### Test conventions to mirror
- **Table-driven white-box** for merge logic: `internal/config/config_test.go` (package
  `config`, stdlib `testing`, `reflect.DeepEqual`). The loader's merge + cycle/depth
  paths are pure logic over fakes ⇒ table tests, the ticket's stated style.
- **Layered fixtures** read through the `FakeFileSystem` (in-memory map), *not* real
  files — keeps tests hermetic and lets cycle/symlink topologies be scripted. (Contrast
  AC-0007, which read static `testdata/*.yaml` via `os.ReadFile` because it had no FS
  seam; AC-0008 should drive the loader through the fake.)
- Golden errors (`validate_test.go:20-49`, the `-update` flag) remain for schema
  messages; cycle/depth messages can be asserted inline with `require.ErrorContains` /
  `errors.Is` against sentinels (they carry a path, so an inline contains-check is more
  robust than a golden).

## The merge, precisely (proposed — see "Open decisions" for the unspecified parts)

### Resolution algorithm

```
Load(projectPath):
    effective = empty Config
    globalPath = UserHomeDir() + "/.config/agent-creance.yaml"
    if exists(globalPath):                      # ReadFile != fs.ErrNotExist
        effective = merge(effective, resolve(globalPath, stack={}, depth=0))
    effective = merge(effective, resolve(projectPath, stack={}, depth=0))
    effective.Include = nil                      # resolved away
    return effective

resolve(path, stack, depth):                     # returns a fully-merged Config
    if depth > maxIncludeDepth: error (ErrMaxIncludeDepth, with path)
    canon = EvalSymlinks(Abs(path))              # canonical identity
    if canon in stack: error (ErrIncludeCycle, with the chain)
    push canon onto stack
    cfg = Parse(ReadFile(path))                  # per-file strict validate
    acc = empty Config
    for inc in cfg.Include:                       # in listed order
        incPath = resolveIncludePath(path, inc)   # relative to path's dir; ~ expands
        acc = merge(acc, resolve(incPath, stack, depth+1))
    acc = merge(acc, cfgWithoutInclude)           # the file's OWN values come LAST
    pop canon from stack
    return acc
```

- **Precedence (low → high):** global's includes → global's own → project's includes
  (in listed order) → project's own. An including file's own values are the *most
  specific* layer (applied last), so it overrides what it includes — the same way a
  project overrides the global. "Later files override earlier" (`design.md:151`) ⇒
  left-fold the include list, own-content last.
- **Cycle detection** uses the *current DFS path* (`stack`), not a global visited set,
  so a legitimate diamond (two branches include the same file) is **not** a false
  cycle — only a back-edge onto the active stack is. A diamond therefore contributes
  the file's rules **twice** (consistent with "union additively, no dedupe"); document
  this. Canonical keys (`EvalSymlinks∘Abs`) make `./a.yaml` and a symlink to it the
  same node, so symlink-disguised cycles are caught.
- **Depth limit** is a secondary guard (cycles already stop infinite loops): bounds
  pathological-but-acyclic deep chains. Counts include-nesting levels. Errors name the
  offending path.
- **Missing-file distinction:** the *implicit global* missing is a silent no-op
  (`ReadFile` → `fs.ErrNotExist` ⇒ skip). A *declared `include:`* pointing at a missing
  file is an **error** (a dangling include is a config bug, not a delta).

### Per-field merge table

| Field | Type | Proposed semantics | Source of truth |
|---|---|---|---|
| `agent.command` | `[]string` | **override-replace** (over wins if non-empty) | not pinned — concat would yield a nonsensical argv |
| `agent.workdir` | `string` | **override** (over wins if non-empty) | "scalars override" (design:151) |
| `safehouse.add_dirs_rw` | `[]string` | **union (concat)** | not pinned — see open decision |
| `safehouse.add_dirs_ro` | `[]string` | **union (concat)** | not pinned |
| `safehouse.enable` | `[]string` | **union (concat)** | not pinned |
| `include` | `[]string` | resolved away (nil in result) | — |
| `network.host_services` | `[]HostService` | **union (concat)** | not pinned |
| `network.egress.generators` | `[]string` | **union (concat)** | not pinned |
| `network.egress.allow` | `[]Rule` | **union (concat)** | "lists union additively" (design:151) ✅ |
| `network.egress.deny_always` | `[]Rule` | **union (concat)** | "lists union additively" (design:151) ✅ |
| `env` | `map[string]string` | **map merge, over wins per key** | not pinned (only sane map rule) |

Only `allow`/`deny_always` (union) and the true scalars (override) are fixed by the
design. The shaded "not pinned" rows are the **first open decision**.

### Determinism (ticket verification step 3)
Concatenation in a fixed traversal order + map merge with a fixed override direction is
deterministic by construction (no map-iteration order leaks into slices; the only map
is `env`, whose *result* is a map, compared by value). Running `Load` twice on the same
fake FS yields a `reflect.DeepEqual`-identical `Config`. A test asserts this directly.

## Proposed package shape (for the plan)

New `internal/config/load.go`:
```go
type Loader struct {
    fs    sysdep.FileSystem
    paths sysdep.PathResolver
}
func NewLoader(fs sysdep.FileSystem, paths sysdep.PathResolver) *Loader

// Load resolves the implicit global + the project config into one effective Config.
func (l *Loader) Load(projectPath string) (*Config, error)

const maxIncludeDepth = 32   // value TBD — see open decisions
```
New sentinels in `internal/config/errors.go`:
```go
var ErrIncludeCycle    = errors.New("config: include cycle")
var ErrMaxIncludeDepth = errors.New("config: include depth limit exceeded")
```
(wrapped with the offending path/chain for the message; `errors.Is`-checkable in tests).

New `internal/sysdep/filesystem.go`:
```go
type FileSystem interface { ReadFile(name string) ([]byte, error) }
type OSFileSystem struct{}
var _ FileSystem = (*OSFileSystem)(nil)
func (OSFileSystem) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
```
New `internal/sysdep/sysdeptest/filesystem.go`:
```go
type FakeFileSystem struct { Files map[string][]byte; Errs map[string]error }
// ReadFile returns Errs[name] if set, else Files[name], else fs.ErrNotExist.
```
`merge(a, b Config) Config` is an unexported pure function in `load.go` (or
`merge.go`), unit-tested directly via table tests in addition to the end-to-end
loader tests.

**No `cli.App` change** (no consumer yet). **No new third-party deps** (`yaml.v3`
already direct from AC-0007; `testify` already used; stdlib `io/fs`, `path/filepath`).

## Open decisions for the planning checkpoint

1. **Merge semantics for the *unspecified* collections** (the "not pinned" rows above:
   `safehouse.add_dirs_rw/ro`, `enable`, `generators`, `host_services`, `env`,
   `agent.command`). The design only fixes `allow`/`deny_always`=union and
   scalars=override. **Recommendation:** all list fields **union (concat)** (they are
   additive capabilities — a shared include grants a baseline, the project adds to it,
   matching the "team-shared.yaml" story at design:152); `env` **map-merges** with the
   later file winning per key; `agent.command` is **override-replace** (concatenating an
   argv is nonsensical), `agent.workdir` overrides. This is the largest judgment call —
   confirm the table.

2. **Duplicate-rule handling (ticket Q).** Do two identical `allow`/`deny_always` rules
   from different files collapse, or are both kept? **Recommendation: keep both (no
   dedupe).** It is deterministic, order-preserving, and preserves *provenance* — AC-0013
   annotates each compiled rule with its source file (e.g. `[global:claude-defaults]`,
   design:198), which a dedupe would erase. Document "union = pure concatenation, no
   dedupe" (also governs the diamond-include case above).

3. **Depth-limit value & configurability (ticket Q).** **Recommendation: a
   non-configurable constant, `maxIncludeDepth = 32`.** Deep enough that no legitimate
   config hits it, small enough to bound work; non-configurable because a security
   tool's safety limit shouldn't be adjustable *from the very config it guards*. Confirm
   the value (and that non-configurable is acceptable).

### Decided (not user calls; stated for completeness)
- **Introduce a narrow `sysdep.FileSystem{ReadFile}` now**, grown later by AC-0009
  (forced by sequencing: AC-0008 precedes AC-0009 and is the first file-read consumer;
  AC-0008 depends only on AC-0007). Flagged so AC-0009's plan extends rather than
  redefines it.
- **Per-file validation; no re-validation after merge** (concat/override preserve
  validity).
- **Implicit global is `~/.config/agent-creance.yaml` literally** (design:28,151), via
  `UserHomeDir()`. No `$XDG_CONFIG_HOME` handling in v0.1 (not in the design text).
- **Include paths resolve relative to the declaring file's directory**; absolute paths
  used as-is; a leading `~/` expands via `UserHomeDir()` (cheap, matches the design's
  `~/`-path convention for safehouse dirs).
- **Loader is hermetic in tests** — driven through `FakeFileSystem` + `FakePathResolver`,
  never real files; no external tools (unit, not integration).

## Acceptance-criteria → test mapping

- *Implicit global merged when present, skipped when absent* → two loader tests over the
  fake FS (global present → its rules appear; global absent (`fs.ErrNotExist`) → no-op,
  project-only result).
- *Recursive `include:`, scalar override, list union (documented dedupe rule)* → layered
  fixtures in the fake FS: global+project scalar override (project wins); allow rules
  from global+project both present in order; the per-field merge table exercised by a
  direct `merge()` table test.
- *Cycle A→B→A errors (not infinite loop); over-depth errors with the path* →
  fake-FS cycle topology asserting `errors.Is(err, ErrIncludeCycle)` + the chain in the
  message; a chain exceeding `maxIncludeDepth` asserting `ErrMaxIncludeDepth` + the
  offending path. A symlink-disguised cycle (via `FakePathResolver.Symlinks`) also caught.
- *Deterministic & documented merge order* → run `Load` twice on identical fakes, assert
  `reflect.DeepEqual`; precedence documented in the package doc + this research.
- *Missing include = error; missing global = no-op* → two targeted tests.

## Verification commands (from `.claude/tce/profile.md`)

- `go build ./...` (typecheck; includes the `var _ FileSystem` compile assertion)
- `go test -race ./internal/config/...` and `go test -race ./internal/sysdep/...`
- `go test -race ./...` (`make test`)
- `make lint` (`go vet` + `golangci-lint`)
- `make golden` only if any schema golden changes (none expected — no new schema)

## Related documents

- `thoughts/shared/tickets/AC-0008-config-include-merge.md` — this ticket.
- `thoughts/shared/research/2026-06-05-AC-0007-config-schema-loader.md` +
  `thoughts/shared/plans/2026-06-05-AC-0007-config-schema-loader.md` — the parser this
  builds on (template for these artifacts).
- `thoughts/shared/tickets/AC-0009-sysdep-seam-extensions.md` — formalises/extends the
  `FileSystem` seam AC-0008 introduces; its plan should build on, not redefine, it.
- `thoughts/shared/tickets/AC-0013-policy-compiler.md` — downstream consumer that unions
  the session-overlay and annotates rule provenance (informs the no-dedupe decision).
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — WP-1.3
  (lines 149-153), WP-1.4 (154-159), C2 seam-growth (102-104).
- `docs/design.md` — "The configuration" (28, 76-153, esp. **151** for merge rules),
  rule provenance annotations (198).
</content>
</invoke>
