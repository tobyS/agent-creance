---
date: 2026-06-06
ticket: AC-0019
title: "Plan: Embed & extract the enforcer addon (WP-3.3)"
status: ready
branch: main
research: thoughts/shared/research/2026-06-06-AC-0019-embed-extract-enforcer.md
---

# Plan: Embed & extract the enforcer addon (AC-0019, WP-3.3)

## Overview

Ship the mitmproxy enforcer addon inside the Go binary via `go:embed` and extract
it to a **constant, cross-project** location in the out-of-tree state dir on
first run — idempotently, refreshing when the embedded copy changes (binary
upgrade). The addon is **four** Python modules (`enforcer.py` + its sibling
imports `policy.py`, `audit.py`, `responses.py`), which must land together in one
directory so mitmproxy's sibling imports resolve.

This ticket only embeds + extracts. Starting mitmproxy with the extracted path is
AC-0020 (out of scope); the extractor is provided as a tested package AC-0020 will
call.

## Current State

- `internal/proxy/enforcer/` holds the Python addon (4 runtime modules + pytest
  suite + `requirements.txt` + golden testdata). There is **no Go code** under
  `internal/proxy/` and **no `go:embed`** anywhere in the repo.
- `internal/state` resolves the out-of-tree cache layout and already has the
  cross-project accessor precedent: `RegistriesRoot()` / `GeneratorsRoot()`
  (`state.go:110-138`), constants block at `state.go:32-49`, `cacheRoot()` at
  `state.go:140-152`.
- `internal/sysdep.FileSystem` (`filesystem.go:19-39`) is the I/O seam; its doc
  comment already names "the extracted enforcer.py" as an intended consumer.
  Fake at `sysdeptest/filesystem.go` (note: `ModTime` is always zero — change
  detection must be content-based, not mtime-based).
- Atomic-write idiom to mirror: `internal/generator/cache.go:59-81`,
  `internal/profile/compile.go:75-89`.
- `docs/design.md:449` incorrectly says the addon extracts to `projects/<hash>/`;
  line 297 + the ticket say "a constant location." (Resolved at checkpoint:
  **constant location**.)

## Desired End State

- `internal/proxy` is a Go package that embeds the four runtime modules and
  exposes an `Extractor` writing them to `<cache>/agent-creance/enforcer/`,
  returning the path to `enforcer.py` (the addon entrypoint AC-0020 hands to
  mitmproxy via `-s`).
- `state.Resolver.EnforcerRoot()` returns `<cache>/agent-creance/enforcer`.
- Extraction is idempotent (unchanged file untouched), self-healing (missing /
  corrupt file rewritten), and refreshes on a binary upgrade (embedded bytes
  changed).
- `docs/design.md:449` reconciled with the constant-location reality.
- `make test`, `go build ./...`, `make lint` all green.

## Key Decisions (from research + checkpoint)

1. **Constant cross-project location** `<cache>/agent-creance/enforcer/` via a new
   `EnforcerRoot()` accessor (sibling of `registries/`, `generators/`).
2. **Embed all four runtime modules** — enumerated explicitly in the directive
   (embed has no exclude, so `enforcer/*.py` would wrongly include `test_*.py`).
3. **Per-file byte comparison** for change detection (read extracted, compare to
   embedded, rewrite via tmp+rename if absent/different). No checksum sidecar; no
   mtime dependency (keeps unit tests hermetic against `FakeFileSystem`).
4. **No CLI wiring** in this ticket (AC-0020 owns proxy start).

---

## Phase 1 — `state.EnforcerRoot()` accessor

### Changes

`internal/state/state.go`:

- Add `enforcerSubdir = "enforcer"` to the constants block (`state.go:32-49`).
- Add a cross-project accessor mirroring `RegistriesRoot`/`GeneratorsRoot`:

  ```go
  // EnforcerRoot returns <cache>/agent-creance/enforcer — the constant,
  // cross-project home of the extracted mitmproxy enforcer addon. Like
  // RegistriesRoot/GeneratorsRoot it is a sibling of projects/<hash>/ and
  // project-independent: the addon is a constant shipped in the binary,
  // identical for every project (docs/design.md, "Tech stack"). The proxy
  // extractor (AC-0019) owns writing the module files beneath this root.
  func (r *Resolver) EnforcerRoot() (string, error) {
      cache, err := r.cacheRoot()
      if err != nil {
          return "", err
      }
      return filepath.Join(cache, appCacheSubdir, enforcerSubdir), nil
  }
  ```

`internal/state/state_test.go`:

- Add `TestEnforcerRootHonoursXDGThenFallsBackToHome` mirroring the existing
  `Registries`/`Generators` root tests (`state_test.go:141-213`): XDG set →
  `/xdg/agent-creance/enforcer`; XDG unset → `<home>/.cache/agent-creance/enforcer`;
  `HomeErr` surfaced.

### Success criteria

- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/state/...` passes (new test green).
- [ ] `make lint` clean.

---

## Phase 2 — `internal/proxy` embed + extractor

### Changes

New file `internal/proxy/extract.go`:

```go
// Package proxy embeds the mitmproxy enforcer addon in the agent-creance binary
// and extracts it to the out-of-tree state dir on first run. The addon is a
// constant — users never install or version it; AC-0020 starts mitmproxy with
// the extracted enforcer.py.
package proxy

import (
    "bytes"
    "embed"
    "errors"
    "fmt"
    "io/fs"
    "path"
    "path/filepath"

    "github.com/tobyS/agent-creance/internal/state"
    "github.com/tobyS/agent-creance/internal/sysdep"
)

// The runtime addon is four modules: enforcer.py plus the siblings it imports
// (policy/audit/responses). They must be extracted together into one directory
// so mitmproxy's sibling imports resolve. The directive enumerates them
// explicitly — `enforcer/*.py` would also embed the pytest suite (embed has no
// exclude). Dev-only files (test_*.py, conftest.py, requirements.txt, testdata/)
// are intentionally not shipped.
//
//go:embed enforcer/enforcer.py enforcer/policy.py enforcer/audit.py enforcer/responses.py
var enforcerFS embed.FS

const (
    embedDir       = "enforcer"     // path within enforcerFS (slash-separated)
    entrypointName = "enforcer.py"  // the addon mitmproxy loads with -s

    dirPerm    = 0o755
    filePerm   = 0o644
    tmpSuffix  = ".tmp"
)

// enforcerModules are the embedded addon files, relative to embedDir.
var enforcerModules = []string{"enforcer.py", "policy.py", "audit.py", "responses.py"}

// Extractor writes the embedded enforcer addon to the constant cross-project
// enforcer dir, through the injected filesystem seam.
type Extractor struct {
    fs    sysdep.FileSystem
    state *state.Resolver
}

// NewExtractor wires an Extractor from the OS seams.
func NewExtractor(fsys sysdep.FileSystem, paths sysdep.PathResolver) *Extractor {
    return &Extractor{fs: fsys, state: state.New(paths)}
}

// Extract writes the embedded addon modules to <cache>/agent-creance/enforcer/
// and returns the path to enforcer.py (the addon entrypoint). Idempotent: a file
// already matching the embedded bytes is left untouched; a missing or differing
// one is (re)written atomically (tmp + rename), which also refreshes the
// extraction after a binary upgrade.
func (e *Extractor) Extract() (string, error) {
    root, err := e.state.EnforcerRoot()
    if err != nil {
        return "", err
    }
    if err := e.fs.MkdirAll(root, dirPerm); err != nil {
        return "", fmt.Errorf("proxy: create enforcer dir %q: %w", root, err)
    }
    for _, name := range enforcerModules {
        want, err := enforcerFS.ReadFile(path.Join(embedDir, name))
        if err != nil {
            return "", fmt.Errorf("proxy: read embedded %q: %w", name, err)
        }
        if err := e.writeIfChanged(filepath.Join(root, name), want); err != nil {
            return "", err
        }
    }
    return filepath.Join(root, entrypointName), nil
}

// writeIfChanged writes want to dest only when the current content differs (or
// dest is absent), atomically via a temp file + rename. A genuine read error on
// an existing file is surfaced; absence is a normal "write it" path.
func (e *Extractor) writeIfChanged(dest string, want []byte) error {
    got, err := e.fs.ReadFile(dest)
    switch {
    case err == nil:
        if bytes.Equal(got, want) {
            return nil // already up to date
        }
    case errors.Is(err, fs.ErrNotExist):
        // first run for this file — fall through to write
    default:
        return fmt.Errorf("proxy: read extracted %q: %w", dest, err)
    }

    tmp := dest + tmpSuffix
    if err := e.fs.WriteFile(tmp, want, filePerm); err != nil {
        return fmt.Errorf("proxy: write %q: %w", tmp, err)
    }
    if err := e.fs.Rename(tmp, dest); err != nil {
        _ = e.fs.Remove(tmp) // best-effort cleanup of the orphaned temp file
        return fmt.Errorf("proxy: commit %q: %w", dest, err)
    }
    return nil
}
```

### Tests — new file `internal/proxy/extract_test.go` (white-box `package proxy`)

White-box so tests can read `enforcerFS` / `enforcerModules` and assert the
extracted bytes equal the embedded ones. Use `sysdeptest.FakeFileSystem` +
`sysdeptest.FakePathResolver` with `Env["XDG_CACHE_HOME"]="/cache"` so the root
is deterministically `/cache/agent-creance/enforcer`.

- `TestEmbedContainsAllRuntimeModules` — each name in `enforcerModules` reads
  non-empty from `enforcerFS`; `enforcer.py` contains the three sibling imports
  (guards against someone trimming the module set). Asserts dev files are NOT
  embedded (`enforcerFS.ReadFile("enforcer/test_enforcer.py")` errors).
- `TestExtractFirstRunWritesAllModules` — empty fake fs → `Extract()` returns
  `/cache/agent-creance/enforcer/enforcer.py`; all four files present with bytes
  equal to the embedded copy and perm `0o644`; dir created with `0o755`.
- `TestExtractIsIdempotent` — pre-seed all four dest files with the embedded
  bytes, then arm `WriteErrs`/`RenameErrs` on every `<dest>.tmp` to fail loudly;
  `Extract()` must succeed (proves no write path was taken when content matches).
- `TestExtractRefreshesChangedModule` — pre-seed a dest file with stale bytes
  (`[]byte("# old")`); `Extract()` rewrites it so its content equals the embedded
  copy; unchanged files are left as the embedded bytes.
- `TestExtractSelfHealsMissingModule` — pre-seed three of four; `Extract()`
  writes the missing one too.
- `TestExtractC4StaysUnderEnforcerRoot` — after `Extract()`, every key in
  `fs.Files`/`fs.Dirs` is under `/cache/agent-creance/enforcer`; nothing under a
  `projects/` path or any project tree (the extractor never receives a project
  dir).
- `TestExtractMkdirError` / `TestExtractWriteError` / `TestExtractRenameError`
  — inject `MkdirErrs[root]`, `WriteErrs[tmp]`, `RenameErrs[tmp]`; `Extract()`
  returns the wrapped error; on rename failure no final file is left and the tmp
  is removed (mirrors `generator/cache_test.go:85-98`).
- `TestExtractCacheRootError` — `FakePathResolver.HomeErr` set + no XDG →
  `Extract()` surfaces the error.

### Success criteria

- [ ] `go build ./...` compiles (embed directive resolves; all four files exist).
- [ ] `go test -race ./internal/proxy/...` passes.
- [ ] Content integrity: extracted bytes equal embedded bytes (asserted).
- [ ] Idempotency + refresh + self-heal + C4 + error paths all covered & green.
- [ ] `make lint` clean.

---

## Phase 3 — Reconcile docs & close the ticket

### Changes

- `docs/design.md:449` — change "extracted to the project's out-of-tree state
  directory (`~/.cache/agent-creance/projects/<hash>/`) on first run" to reflect
  the constant location, e.g. "extracted to a constant location in the
  out-of-tree state directory (`~/.cache/agent-creance/enforcer/`) on first run,
  refreshed when the binary's embedded copy changes." Keep it consistent with
  line 297.
- `thoughts/shared/tickets/AC-0019-embed-extract-enforcer.md` — tick the four
  acceptance criteria, set Status to Done, answer the research question (checksum
  vs byte-compare: byte-compare chosen), add a Notes entry recording the
  constant-location decision and the four-module finding.

### Success criteria

- [ ] `make test` green (full suite).
- [ ] `go build ./...` green.
- [ ] `make lint` clean.
- [ ] Ticket marked Done; design doc consistent.

---

## Testing Strategy

- **Unit (hermetic):** all of Phase 1 & 2 via `FakeFileSystem`/`FakePathResolver`
  — no real OS, no mitmproxy. Content-based assertions (mtime-independent).
- **No golden files needed:** the "golden" content is the embedded `.py`; a
  direct `bytes.Equal(extracted, embedded)` assertion is stronger and simpler.
- **No integration test required** by this ticket (mitmproxy start is AC-0020).
  The four-module co-location is what makes AC-0020's `-s enforcer.py` imports
  work; AC-0020's live probes will exercise that end-to-end.

## Automated verification (per profile.md)

- `make test` (= `go test -race ./...`)
- `go build ./...`
- `make lint` (= `go vet ./...` + `golangci-lint run`)

## Manual verification

- Build the binary, run the code path that triggers extraction (or a scratch
  call), confirm `~/.cache/agent-creance/enforcer/` contains the four modules and
  a second run leaves them untouched. (Optional — covered by unit tests; full
  end-to-end lands with AC-0020.)

## Out of Scope

- Starting mitmproxy with the extracted addon (AC-0020).
- The addon's behaviour (AC-0017/0018).
- Embedding the pytest suite / `requirements.txt`.
