---
date: 2026-06-05
ticket: AC-0008
topic: "Include resolution & merge semantics (WP-1.3) — internal/config"
status: ready
branch: main
researcher: Claude (tce:work)
research: thoughts/shared/research/2026-06-05-AC-0008-config-include-merge.md
---

# AC-0008 — Include resolution & merge (WP-1.3): Implementation Plan

## Overview

Add a layered `config.Loader` on top of the pure `config.Parse` (AC-0007). The loader
reads the implicit global (`~/.config/agent-creance.yaml`) plus recursively-`include:`d
files and merges them into one effective `Config`, with current-path cycle detection,
a depth limit, scalar-override + list-union (deduped) semantics. To read files
hermetically, introduce a narrow `sysdep.FileSystem{ReadFile}` seam (real impl + fake);
AC-0009 will later grow it. `Parse` is unchanged and stays filesystem-free.

## Current state

- `internal/config/config.go` — `Parse([]byte) (*Config, error)`: strict decode +
  defaults + per-rule validation. Filesystem-free. The typed `Config` tree
  (`Agent`, `Safehouse`, `Include []string`, `Network`/`Egress`/`Rule`, `Env map`).
- `internal/config/validate.go`, `errors.go` — per-rule validation + `ValidationError`.
- `internal/sysdep` — `Commander`, `PathResolver` (has `Abs`, `EvalSymlinks`,
  `UserHomeDir`, `Getenv`); fakes in `sysdeptest`. **No file-content I/O seam.**
- No layering, no include resolution, no merge, no `cli.App` consumer yet.

## Desired end state

- `internal/sysdep/filesystem.go` — `FileSystem{ ReadFile(name string) ([]byte, error) }`
  + `OSFileSystem` + compile-time assertion. Fake `FakeFileSystem` in `sysdeptest`.
- `internal/config/load.go` — `Loader{fs, paths}`, `NewLoader`, `Load(projectPath)`,
  the unexported `resolve` (recursion + cycle/depth) and `merge` machinery.
- New error sentinels `ErrIncludeCycle`, `ErrMaxIncludeDepth` (in `errors.go`).
- `Load` returns one effective `*Config` per the agreed merge semantics; fully
  hermetic unit tests over the fakes. `make test`, `make lint`, `go build ./...` green.

## Agreed semantics (from the checkpoint)

**Per-field merge** (low→high precedence: global's includes → global → project's
includes (listed order) → project's own; an including file's own values are applied
*last*, so it overrides its includes):

| Field | Rule |
|---|---|
| `agent.command` | replace: `over` wins if non-empty, else keep `base` |
| `agent.workdir` | override: `over` wins if non-empty |
| `safehouse.add_dirs_rw` / `add_dirs_ro` / `enable` | union (concat) + dedupe |
| `network.host_services` | union (concat) + dedupe |
| `network.egress.generators` | union (concat) + dedupe |
| `network.egress.allow` / `deny_always` | union (concat) + dedupe |
| `env` | map merge, `over` wins per key |
| `include` | resolved away (`nil` in the effective result) |

- **Dedupe = keep first occurrence**, applied uniformly to every unioned list. Rule
  equality uses `reflect.DeepEqual` (correctly compares the `*[]string` Paths/Methods,
  treating nil≠empty). String lists use a seen-set. Deterministic and order-stable.
- **Depth limit:** non-configurable `const maxIncludeDepth = 10`.
- **Cycle detection:** current DFS-path stack of canonical paths (`EvalSymlinks∘Abs`),
  so legitimate diamonds aren't false positives; a back-edge errors with the chain.
- **Missing file:** the *implicit global* absent ⇒ silent no-op; a declared *include*
  (or the project file) absent ⇒ error naming the path.

## What we're NOT doing

- No input hashing / compile cache (AC-0013). No session-overlay union (AC-0013).
- No `cli.App` wiring (no consumer until AC-0025); no `run` command.
- No re-validation of the merged result (concat/override preserve per-rule validity;
  each file is already validated by `Parse`).
- No `$XDG_CONFIG_HOME` support — the global is literally `~/.config/agent-creance.yaml`.
- Not extending `FileSystem` beyond `ReadFile` (write/stat/mkdir/etc. are AC-0009).

---

## Phase 1 — `sysdep.FileSystem` read seam

### Changes

**`internal/sysdep/filesystem.go`** (new) — mirror `commander.go`:
```go
// FileSystem abstracts reading file contents. It is the file *content* I/O seam the
// PathResolver doc anticipates; AC-0009 grows it (write/stat/mkdir/...). Kept narrow:
// AC-0008 (config include resolution) only needs ReadFile.
type FileSystem interface {
    // ReadFile returns the contents of the named file, mirroring os.ReadFile. A
    // not-exist error satisfies errors.Is(err, fs.ErrNotExist).
    ReadFile(name string) ([]byte, error)
}

type OSFileSystem struct{}
var _ FileSystem = (*OSFileSystem)(nil)
func (OSFileSystem) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
```

**`internal/sysdep/sysdeptest/filesystem.go`** (new):
```go
// FakeFileSystem is a scripted, in-memory FileSystem: pre-load Files (path→bytes) and
// optional Errs (path→error). An unknown path yields fs.ErrNotExist so callers can
// distinguish "absent" (errors.Is(err, fs.ErrNotExist)) from a forced read failure.
type FakeFileSystem struct {
    Files map[string][]byte
    Errs  map[string]error
}
func NewFakeFileSystem() *FakeFileSystem { /* init maps */ }
func (f *FakeFileSystem) ReadFile(name string) ([]byte, error) {
    if err := f.Errs[name]; err != nil { return nil, err }
    if b, ok := f.Files[name]; ok { return b, nil }
    return nil, fs.ErrNotExist
}
```

**`internal/sysdep/filesystem_test.go`** (new) — smoke test: `OSFileSystem.ReadFile`
on a `t.TempDir()` file round-trips; missing path satisfies `errors.Is(err,
fs.ErrNotExist)`. (Fake exercised thoroughly in Phase 3.)

### Success criteria

**Automated**
- [x] `go build ./...` compiles (incl. the `var _ FileSystem` assertion)
- [x] `go vet ./...` clean
- [x] `go test -race ./internal/sysdep/...` passes
- [x] Grep guard: `grep -l FileSystem internal/sysdep/sysdeptest/*.go` returns the fake

**Manual**
- [x] Interface carries only `ReadFile` (no scope creep into AC-0009's surface)

---

## Phase 2 — Merge semantics (`merge.go`)

### Changes

**`internal/config/merge.go`** (new) — pure, no I/O:
```go
// merge layers over onto base and returns the combined Config. Scalars (workdir) and
// the command argv: over wins when set. List fields union (concat) then dedupe,
// keeping first occurrence. env merges key-wise with over winning. Include is not
// merged (it is resolved away by the loader).
func merge(base, over Config) Config
```
Helpers:
- `dedupeStrings([]string) []string` — seen-set, first occurrence, returns `nil` when
  empty (so DeepEqual of two empty results matches).
- `dedupeRules([]Rule) []Rule` — O(n²) `reflect.DeepEqual` (small lists; exact pointer
  semantics for Paths/Methods).
- `dedupeHostServices([]HostService) []HostService` — `HostService` is comparable, use
  a `map[HostService]bool` seen-set.
- `mergeEnv(base, over map[string]string) map[string]string` — copy base, overlay over;
  return `nil` if empty.
- `firstNonEmptyString(over, base)` and `firstNonEmptySlice` for the scalar/command
  override.

Per-field wiring exactly per the "Agreed semantics" table.

### Tests — `internal/config/merge_test.go` (white-box, table-driven)

- scalar override: `over.Agent.Workdir` wins; empty `over` keeps `base`.
- command replace: non-empty `over.Agent.Command` replaces; empty keeps base (no concat).
- list union + dedupe for each of add_dirs_rw/ro, enable, generators, host_services:
  `base ++ over` minus exact duplicates, first occurrence kept, order stable.
- rule union + dedupe: identical allow rules (incl. equal `*[]string` paths) collapse;
  rules differing only by a nil-vs-`[]` Paths do **not** collapse (pointer-aware).
- env merge: union of keys; on key collision `over` wins; both-empty ⇒ nil.
- determinism: `merge(a,b)` twice is `reflect.DeepEqual`-identical.

### Success criteria

**Automated**
- [x] `go test -race ./internal/config/...` passes (new merge tests green)
- [x] `go build ./...`, `go vet ./...` clean

**Manual**
- [x] Dedupe keeps first occurrence and is order-stable across all unioned lists

---

## Phase 3 — Loader: include resolution, cycle detection, depth limit

### Changes

**`internal/config/errors.go`** — add:
```go
var ErrIncludeCycle    = errors.New("config: include cycle")
var ErrMaxIncludeDepth = errors.New("config: include depth limit exceeded")
```
(wrapped with the path/chain for the human-readable message; `errors.Is`-checkable).

**`internal/config/load.go`** (new):
```go
const maxIncludeDepth = 10

type Loader struct {
    fs    sysdep.FileSystem
    paths sysdep.PathResolver
}
func NewLoader(fs sysdep.FileSystem, paths sysdep.PathResolver) *Loader

// Load resolves the implicit global (~/.config/agent-creance.yaml, skipped if absent)
// plus the project config at projectPath — recursively resolving include:, detecting
// cycles, enforcing the depth limit — into one effective Config.
func (l *Loader) Load(projectPath string) (*Config, error)
```

`Load`:
1. `home, _ := l.paths.UserHomeDir()`; `globalPath := filepath.Join(home, ".config",
   "agent-creance.yaml")`.
2. `eff := Config{}`; resolve global with `optional=true` (absent ⇒ empty, no error),
   `eff = merge(eff, *g)`.
3. resolve project with `optional=false`; `eff = merge(eff, *p)`.
4. `eff.Include = nil`; return `&eff`.

`resolve(path string, optional bool, stack []string, depth int) (*Config, error)`:
1. `if depth > maxIncludeDepth` → `fmt.Errorf("%w: %s", ErrMaxIncludeDepth, path)`.
2. `abs, _ := l.paths.Abs(path)`; `data, err := l.fs.ReadFile(abs)`.
   - `errors.Is(err, fs.ErrNotExist)`: if `optional` → return `&Config{}, nil`; else
     `fmt.Errorf("config: include not found: %s: %w", path, err)`.
   - other err → `fmt.Errorf("config: read %s: %w", path, err)`.
3. `canon, err := l.paths.EvalSymlinks(abs)`; on err fall back to `abs`.
4. cycle check: if `canon` ∈ `stack` → `fmt.Errorf("%w: %s", ErrIncludeCycle,
   chain(stack, canon))` (render `a -> b -> a`).
5. `cfg, err := Parse(data)` (per-file strict validate; propagate the
   `*ValidationError`, prefixed with the file path for locality).
6. `stack2 := append(stack, canon)`; `acc := Config{}`; for each `inc` in
   `cfg.Include`: `acc = merge(acc, *resolve(l.resolveIncludePath(abs, inc), false,
   stack2, depth+1))` (own values applied after the includes).
7. `cfg.Include = nil`; `acc = merge(acc, cfg)`; return `&acc`.

`resolveIncludePath(declaringAbs, inc string) string`:
- `~/`-prefixed → `filepath.Join(home, inc[2:])`;
- absolute → `inc`;
- else → `filepath.Join(filepath.Dir(declaringAbs), inc)`.

Note: the cycle check runs *after* the read, so a 2-cycle A→B→A reads A twice (bounded)
then trips on the stack before recursing again — no infinite loop. Depth limit is the
backstop for deep acyclic chains.

### Tests — `internal/config/load_test.go` (white-box, fakes only)

Drive `NewLoader(FakeFileSystem, FakePathResolver)` with in-memory layered fixtures.
`FakePathResolver.HomeDir` set; FS keyed by the `Abs` paths the loader computes
(`FakePathResolver.Cwd` controls relative resolution).

- **global present:** global allow rule + project allow rule both appear, project
  scalars win.
- **global absent:** `Load` returns the project-only config (no error) — global path
  not in `Files`.
- **recursive include:** project `include: [a.yaml]`, `a.yaml include: [b.yaml]`;
  rules from all three present; scalar from the project (outermost own) wins over
  included files.
- **scalar override + list union + dedupe** end-to-end (a rule duplicated across global
  and an include collapses to one).
- **env merge** end-to-end (project key overrides global key).
- **cycle A→B→A:** `errors.Is(err, ErrIncludeCycle)` and the message contains the chain.
- **symlink-disguised cycle:** `FakePathResolver.Symlinks` aliases `b.yaml`→`a.yaml`'s
  canonical path; still caught.
- **depth exceeded:** a chain longer than `maxIncludeDepth` ⇒ `errors.Is(err,
  ErrMaxIncludeDepth)` + offending path in the message.
- **missing include:** declared `include:` not in `Files` ⇒ error naming the path,
  `errors.Is(err, fs.ErrNotExist)`.
- **invalid included file:** an include with `mode: passthrough` + `paths:` ⇒ the
  `*ValidationError` surfaces (per-file validation), path-prefixed.
- **determinism:** `Load` twice over the same fakes ⇒ `reflect.DeepEqual` configs.

### Success criteria

**Automated**
- [x] `go test -race ./internal/config/...` passes (all loader cases green)
- [x] `go test -race ./...` (`make test`) green — no regressions in cli/prereq/state/sysdep
- [x] `go build ./...` clean
- [x] `make lint` (`go vet` + `golangci-lint`) clean

**Manual**
- [x] Re-read `Load`/`resolve` for the cycle-before-infinite-recursion property and the
      optional-global vs required-include error split
- [x] Merge order documented in the `load.go` package/Load doc comment (precedence
      low→high) so the determinism guarantee is reviewable

---

## Testing strategy

- Pure merge logic → table-driven white-box tests (Phase 2), matching
  `config_test.go`/`version_test.go`.
- Loader behavior → hermetic white-box tests over `FakeFileSystem` + `FakePathResolver`
  (Phase 3); **no real files, no external tools** (unit, not integration).
- Cycle/depth/missing errors → `errors.Is` against sentinels + `require.ErrorContains`
  for the path/chain (more robust than goldens, which would bake absolute paths).
- No new golden files (no schema change). `make golden` should produce no diff.

## References

- Ticket: `thoughts/shared/tickets/AC-0008-config-include-merge.md`
- Research: `thoughts/shared/research/2026-06-05-AC-0008-config-include-merge.md`
- Builds on: `internal/config/config.go` (`Parse`), `internal/sysdep/pathresolver.go`,
  `internal/sysdep/commander.go` (seam template), `internal/sysdep/sysdeptest/`
- Design: `docs/design.md:151` (merge rules), `:28` (implicit global)
- Spec: WP-1.3 (`…/discussions/2026-06-04-v0.1-technical-specification.md:149-153`);
  FileSystem seam overlap with WP-1.4/AC-0009 (`:154-159`)
</content>
