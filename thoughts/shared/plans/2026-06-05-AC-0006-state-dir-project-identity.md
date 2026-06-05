---
date: 2026-06-05
ticket: AC-0006
topic: "State directory & project identity (WP-1.1) — implementation plan"
status: ready
branch: main
git_commit: cc8079f
research: thoughts/shared/research/2026-06-05-AC-0006-state-dir-project-identity.md
---

# AC-0006 — State directory & project identity (WP-1.1): Implementation Plan

## Overview

Build `internal/state`, a pure package that maps a project directory to a stable
identity hash (derived from the canonical `realpath` of the directory) and the
fully-resolved out-of-tree state-dir layout under
`~/.cache/agent-creance/projects/<hash>/`. Symlinked aliases of the same physical
directory must collapse to one identity. The package performs **no** artifact I/O —
it only computes paths and the hash. All OS access (path canonicalisation, cache-root
derivation) goes through a new narrow seam in `internal/sysdep`.

## Decisions (from the research checkpoint)

1. **Seam lives in `internal/sysdep`** — a narrow, point-of-use `PathResolver`
   interface (path canonicalisation + home/env), with a real impl and a fake in
   `sysdeptest`. This is a *distinct* concern from AC-0009's planned file-I/O
   `FileSystem`, so it does not collide; AC-0009 adds `FileSystem` alongside later.
2. **Hash = SHA-256 of the canonical path, truncated to the first 8 bytes rendered as
   16 hex chars** (64-bit). Stdlib-only, collision-safe, conventional.
3. **Session-overlay filename = `session-overlay.yaml`** (it is YAML, unioned by the
   compiler like an `include:`d file — `docs/design.md:304`).
4. **Cache root = XDG-style `~/.cache`**, honouring `XDG_CACHE_HOME` when set, else
   `$HOME/.cache` (the design writes `~/.cache/agent-creance`, not macOS
   `~/Library/Caches`, so `os.UserCacheDir()` is deliberately *not* used —
   `docs/design.md:291`). Final root: `<cache>/agent-creance/projects/<hash>/`.

## Current state

- `internal/sysdep` has only `Commander` (`commander.go`) + its fake
  (`sysdeptest/fake.go`). No `internal/state` package exists.
- AC-0009 (the broader seam) is unbuilt, but its `FileSystem` is file-I/O and does not
  cover path canonicalisation/cache-root — see research.
- Patterns to mirror: `internal/sysdep/commander.go` (interface + empty-struct real
  impl + `var _ Iface` assertion), `internal/sysdep/sysdeptest/fake.go` (map-backed
  scripted fake in the separate `sysdeptest` package), `internal/prereq/version_test.go`
  (table tests with `t.Run`).

## Desired end state

- `internal/sysdep.PathResolver` interface + `OSPathResolver` real impl (compile-time
  assertion) + `sysdeptest.FakePathResolver` scripted fake.
- `internal/state` with a `Resolver` (takes the seam), a `Layout` value type, a
  `Resolve(dir) (Layout, error)` method, and typed accessors for every artifact.
- Hermetic table tests for `internal/state` (fake only; no `"os"` import → grep guard
  passes). A real-impl smoke test for `OSPathResolver` lives in `internal/sysdep`
  (where `"os"` is permitted).
- `make test`, `go build ./...`, `make lint` all green; the ticket's grep guard exits 0.
- **No `cli.App` wiring** — `internal/state` has no command consumer yet (per AC-0009's
  out-of-scope note: don't wire unused deps; it is wired when `run`/`policy` arrive).

---

## Phase 1 — `PathResolver` seam in `internal/sysdep`

### Changes

**New file `internal/sysdep/pathresolver.go`:**

```go
// PathResolver abstracts the path-canonicalisation and environment primitives
// needed to resolve a project's stable identity path and the out-of-tree cache
// root. It is deliberately separate from the file *content* I/O seam
// (FileSystem, AC-0009): this package only locates directories, it never reads
// or writes their contents.
type PathResolver interface {
    // Abs returns an absolute representation of path (mirrors filepath.Abs;
    // resolves relative paths against the current working directory).
    Abs(path string) (string, error)
    // EvalSymlinks returns path with all symlinks resolved (mirrors
    // filepath.EvalSymlinks). A non-nil error means the path does not exist.
    EvalSymlinks(path string) (string, error)
    // UserHomeDir returns the current user's home directory (os.UserHomeDir).
    UserHomeDir() (string, error)
    // Getenv returns the value of the environment variable key (os.Getenv).
    Getenv(key string) string
}

type OSPathResolver struct{}

var _ PathResolver = (*OSPathResolver)(nil)

// methods delegate to filepath.Abs / filepath.EvalSymlinks / os.UserHomeDir / os.Getenv
```

Follow the `commander.go` doc-comment style (explain to a PHP/TS reader why the seam
exists).

**New file `internal/sysdep/sysdeptest/pathresolver.go`:**

```go
// FakePathResolver is a scripted PathResolver. Symlinks maps an input path to its
// canonical resolved path (absent → resolves to itself). HomeDir/Env back
// UserHomeDir/Getenv. Optional *Err fields simulate failures.
type FakePathResolver struct {
    Cwd      string            // base for Abs on relative paths (default "/")
    Symlinks map[string]string // input path -> resolved path
    HomeDir  string
    HomeErr  error
    Env      map[string]string
    AbsErr   error
    EvalErr  error
}
func NewFakePathResolver() *FakePathResolver { ... } // empty maps, Cwd="/"
```

- `Abs`: if `AbsErr` set → return it; absolute path → `filepath.Clean`; relative →
  `filepath.Join(f.Cwd, path)`.
- `EvalSymlinks`: if `EvalErr` set → return it; return `Symlinks[path]` if present,
  else `path` unchanged (already canonical).
- `UserHomeDir`: return `HomeDir, HomeErr`.
- `Getenv`: return `Env[key]` (zero value "" if absent).

**New file `internal/sysdep/pathresolver_test.go`:** smoke-test the real
`OSPathResolver` — create a temp dir + a symlink to it (`t.TempDir`, `os.Symlink`),
assert `EvalSymlinks(link) == EvalSymlinks(target)`; assert `Abs` of a relative path is
absolute; assert `UserHomeDir`/`Getenv` delegate. (`"os"` is allowed here, unlike in
`internal/state`.)

### Success criteria

**Automated:**
- [x] `go build ./...` compiles (incl. the `var _ PathResolver` assertion).
- [x] `go vet ./...` clean.
- [x] `go test -race ./internal/sysdep/...` passes.
- [x] `make lint` reports no new findings.

**Manual:**
- [x] `Commander` and its existing tests remain untouched/green (additive only).

---

## Phase 2 — `internal/state` package

### Changes

**New file `internal/state/state.go`:**

```go
package state

const (
    appCacheSubdir   = "agent-creance"
    projectsSubdir   = "projects"
    policyJSONName   = "policy.json"
    networkSBName    = "network.sb"
    proxyLockName    = "proxy.lock"
    egressJSONLName  = "egress.jsonl"
    claudeDirName    = "claude"
    sessionOverlayName = "session-overlay.yaml"
)

// Resolver turns a project directory into its identity + state-dir layout using
// the injected path/environment seam.
type Resolver struct{ paths sysdep.PathResolver }

func New(paths sysdep.PathResolver) *Resolver { return &Resolver{paths: paths} }

// Layout is the fully-resolved state-dir layout for one project.
type Layout struct {
    Canonical string // realpath-resolved absolute project dir
    Hash      string // deterministic identity derived from Canonical
    Root      string // <cache>/agent-creance/projects/<hash>
}

// Resolve canonicalises dir (Abs -> EvalSymlinks), derives the identity hash, and
// builds the layout. Errors if dir cannot be resolved (e.g. it does not exist) or
// the cache root cannot be determined.
func (r *Resolver) Resolve(dir string) (Layout, error) {
    abs, err := r.paths.Abs(dir)            // handle "." / relative
    if err != nil { return Layout{}, fmt.Errorf("state: abs %q: %w", dir, err) }
    canon, err := r.paths.EvalSymlinks(abs) // collapse symlink aliases
    if err != nil { return Layout{}, fmt.Errorf("state: resolve %q: %w", dir, err) }
    root, err := r.projectRoot(hashPath(canon))
    if err != nil { return Layout{}, err }
    return Layout{Canonical: canon, Hash: hashPath(canon), Root: root}, nil
}

// hashPath = hex(sha256(canonical)[:8]) -> 16 hex chars.
func hashPath(canonical string) string { ... }

// projectRoot = <XDG_CACHE_HOME or $HOME/.cache>/agent-creance/projects/<hash>.
func (r *Resolver) cacheRoot() (string, error) { ... }   // XDG then HOME fallback

// Accessors (pure filepath.Join on Root):
func (l Layout) PolicyJSON() string     { return filepath.Join(l.Root, policyJSONName) }
func (l Layout) NetworkSB() string      { return filepath.Join(l.Root, networkSBName) }
func (l Layout) ProxyLock() string      { return filepath.Join(l.Root, proxyLockName) }
func (l Layout) EgressJSONL() string    { return filepath.Join(l.Root, egressJSONLName) }
func (l Layout) ClaudeConfigDir() string{ return filepath.Join(l.Root, claudeDirName) }
func (l Layout) SessionOverlay() string { return filepath.Join(l.Root, sessionOverlayName) }
```

Imports: `crypto/sha256`, `encoding/hex`, `fmt`, `path/filepath`, and
`internal/sysdep`. **No `"os"` import** (grep-guard requirement).

`cacheRoot()`: `if x := r.paths.Getenv("XDG_CACHE_HOME"); x != "" { base = x } else {
home, err := r.paths.UserHomeDir(); base = filepath.Join(home, ".cache") }`, then
`filepath.Join(base, appCacheSubdir, projectsSubdir)`. `projectRoot(hash)` joins that
with `hash`.

**New file `internal/state/state_test.go`:** table-driven, `FakePathResolver` only.
Cases:
- **Symlink collapse:** fake `Symlinks{"/work/link":"/real/proj","/real/proj":"/real/proj"}`,
  `HomeDir="/home/u"`; `Resolve("/work/link")` and `Resolve("/real/proj")` yield equal
  `Hash` and equal `Root`.
- **Distinct dirs → distinct hashes:** `/a` vs `/b` give different `Hash`.
- **Relative path:** `Cwd="/work"`, `Resolve("proj")` → `Canonical` resolves via
  `Abs` then `EvalSymlinks`.
- **Hash shape:** `Hash` is 16 lowercase hex chars; deterministic across two calls.
- **Cache root XDG set:** `Env{"XDG_CACHE_HOME":"/xdg"}` → `Root` under
  `/xdg/agent-creance/projects/<hash>`.
- **Cache root fallback:** no XDG, `HomeDir="/home/u"` → `Root` under
  `/home/u/.cache/agent-creance/projects/<hash>`.
- **Accessor rooting:** for a resolved `Layout`, every accessor returns
  `filepath.Join(Root, <expected name>)` and is prefixed by
  `.../projects/<hash>/` (assert prefix + suffix per artifact).
- **Resolve errors:** `EvalErr` set → `Resolve` returns error; `HomeErr` set with no
  XDG → error.

### Success criteria

**Automated:**
- [x] `go build ./...` compiles.
- [x] `go test -race ./internal/state/...` passes (all table cases).
- [x] Grep guard exits 0:
      `! grep -rnE '"os"|os\.(Open|Stat|MkdirAll|ReadFile|WriteFile)' internal/state/*.go`
- [x] `make lint` reports no new findings.
- [x] `make test` green overall.

**Manual:**
- [x] Every Acceptance-Criteria accessor exists and is rooted at `projects/<hash>/`:
      `policy.json`, `network.sb`, `proxy.lock`, `egress.jsonl`, `claude/`,
      session-overlay.
- [x] Symlink-collapse and distinct-dir properties verified by the table tests.

---

## Verification (final, whole-ticket)

Run from repo root:
1. `go build ./...` → compiles.
2. `make test` → green (race).
3. `go test -race ./internal/state/...` and `./internal/sysdep/...` → pass.
4. `make lint` → no new findings.
5. Grep guard (no direct OS in `internal/state`):
   `! grep -rnE '"os"|os\.(Open|Stat|MkdirAll|ReadFile|WriteFile)' internal/state/*.go` → exit 0.

## Out of scope (per ticket)

- Creating/writing any artifact (compiler/proxy/audit own those).
- Lock-file semantics (AC-0020).
- iCloud/SMB reliability warning (deferred to `doctor`, AC-0031).
- `cli.App` wiring (added when a consumer command lands).

## Notes / risks

- The accessor names + session-overlay filename become the contract for downstream
  consumers (AC-0013/0014/0020/0021, WP-4.2/6.1) — chosen deliberately here.
- The `PathResolver` seam intentionally does not pre-empt AC-0009's `FileSystem`
  (file I/O). If AC-0009 later chooses one fat interface, `OSPathResolver` can be
  folded in then; nothing here blocks that.
