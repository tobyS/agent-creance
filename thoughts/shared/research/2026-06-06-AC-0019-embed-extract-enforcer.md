---
date: 2026-06-06
ticket: AC-0019
title: "Research: Embed & extract enforcer.py (WP-3.3)"
status: complete
branch: main
commit: c3751a08174b70b76d4c074b78bf0f63dc874e74
repo: github.com/tobyS/agent-creance
---

# Research: Embed & extract the enforcer addon (AC-0019, WP-3.3)

## Research Question

How should `agent-creance` embed the mitmproxy enforcer addon into the Go
binary (`go:embed`) and extract it to the out-of-tree state dir on first run —
idempotently, refreshing when the embedded copy changes? What is the right
extraction location, what exactly must be embedded, and how should "the embedded
copy differs" be detected cheaply?

## Summary / TL;DR

- **The addon is *not* a single file.** `enforcer.py` imports three sibling
  modules — `policy`, `audit`, `responses` — so the runtime addon is **four**
  Python files that must land in **one directory** (mitmproxy puts a `-s`
  script's parent dir on `sys.path`, so sibling imports resolve). Embedding and
  extracting only `enforcer.py` would crash mitmproxy with an `ImportError`.
  The dev/test files (`test_*.py`, `conftest.py`, `test_vectors.py`,
  `testdata/`, `requirements.txt`) are **not** runtime artifacts and must not be
  shipped.
- **There is no Go code in `internal/proxy` yet** and **no `go:embed` anywhere
  in the repo.** AC-0019 introduces both. The package `internal/proxy` will hold
  the embed directive + an extractor.
- **The repo has a single, well-established idiom for everything around this**:
  atomic temp-file-then-rename writes through the `sysdep.FileSystem` seam, plus
  `state.Resolver` for the out-of-tree path. The extractor should mirror it.
- **Open design point (genuine, doc-level contradiction):** `docs/design.md`
  contradicts itself on *where* the addon is extracted — line 297 + the ticket
  say "a constant location, not per-project file"; line 449 says
  `projects/<hash>/`. Resolving this picks the `state` API shape (a new
  cross-project accessor vs a per-project `Layout` method). Surfaced at the
  question checkpoint.
- **Change detection:** the simplest robust approach that matches the repo's
  read-compare style is **per-file byte comparison** (read the extracted file,
  compare to the embedded bytes, rewrite if missing/different). No separate
  checksum/manifest file is needed for four small files, and it self-heals a
  corrupted extraction.

## What must be embedded (runtime vs dev files)

`internal/proxy/enforcer/` currently contains:

Runtime modules (the addon mitmproxy loads) — **embed these**:

- `enforcer.py` — the mitmproxy glue addon. Imports `audit`, `policy`,
  `responses` (`internal/proxy/enforcer/enforcer.py:49-51`).
- `policy.py` — pure decision engine (stdlib only).
- `audit.py` — JSONL audit writer (stdlib only).
- `responses.py` — the three wire 403 bodies (stdlib only).

Confirmed each non-glue module imports stdlib only (no third-party, no
cross-package beyond the three siblings):

```
responses.py: json, dataclasses
audit.py:     json, os, datetime, typing, urllib.parse
policy.py:    json, dataclasses, typing
```

Dev/test only — **do NOT embed**:

- `test_enforcer.py`, `test_policy.py`, `test_audit.py`, `test_responses.py`,
  `test_integration.py`, `test_vectors.py`, `conftest.py` — pytest suite.
- `testdata/*.golden` — golden fixtures for the Python tests.
- `requirements.txt` — pins `mitmproxy==12.2.3` + `pytest==8.3.4` for the dev
  venv; mitmproxy itself is a host prerequisite (detected by `internal/prereq`),
  not something we ship.

`enforcer.py:35-37` already documents that `go:embed`/extraction is AC-0019's
job and the policy/audit paths are wired by AC-0020 as mitmproxy options.

### Implication for `go:embed`

`go:embed enforcer/*.py` would also pull in `test_*.py`/`conftest.py` (embed has
no exclude/negation). So the directive must **enumerate the four runtime files
explicitly**:

```go
//go:embed enforcer/enforcer.py enforcer/policy.py enforcer/audit.py enforcer/responses.py
var enforcerFS embed.FS
```

The `.go` file holding the directive must live in `internal/proxy/` (embed can
only reach files in the package dir or below — `enforcer/` is a subdir, so this
works).

## Where things live (codebase map)

### The out-of-tree state dir — `internal/state`

- `state.Resolver` (`internal/state/state.go:53-60`) maps a project dir →
  `Layout{Canonical, Hash, Root}` via the `sysdep.PathResolver` seam; performs
  **no I/O** itself (`state.go:12-16`).
- Per-project root is `<cache>/agent-creance/projects/<hash>/`
  (`state.go:101-108`). Per-project artifact accessors are `Layout` methods:
  `PolicyJSON()`, `NetworkSB()`, `ProxyLock()`, `EgressJSONL()`,
  `ClaudeConfigDir()`, `SessionOverlay()` (`state.go:154-171`), each a filename
  constant in the block at `state.go:32-49`.
- **Cross-project (constant) roots already exist** as siblings of `projects/`:
  `RegistriesRoot()` → `<cache>/agent-creance/registries`
  (`state.go:110-123`) and `GeneratorsRoot()` →
  `<cache>/agent-creance/generators` (`state.go:125-138`). These are the
  precedent for a constant, project-independent location — exactly the shape a
  "constant location" enforcer dir would take (e.g. a new `EnforcerRoot()` →
  `<cache>/agent-creance/enforcer`, with a new `enforcerSubdir` constant).
- `cacheRoot()` honours `XDG_CACHE_HOME` then `$HOME/.cache`
  (`state.go:140-152`); deliberately not `os.UserCacheDir` (wants `~/.cache` on
  macOS, not `~/Library/Caches`).

### The filesystem seam — `internal/sysdep`

- `sysdep.FileSystem` interface (`internal/sysdep/filesystem.go:19-39`):
  `ReadFile`, `WriteFile`, `Stat`, `MkdirAll`, `Remove`, `Rename`. Its doc
  comment (`filesystem.go:9-13`) *literally names "the extracted enforcer.py"*
  as an intended consumer of this seam — the seam was designed with this ticket
  in mind.
- Prod impl `OSFileSystem` (`filesystem.go:46-72`); helper
  `RemoveIfPresent(fsys, name)` (`filesystem.go:82-93`).
- Fake `sysdeptest.FakeFileSystem` (`internal/sysdep/sysdeptest/filesystem.go`):
  in-memory `Files`/`Dirs`/`Perms`, per-op error knobs (`WriteErrs`,
  `StatErrs`, `MkdirErrs`, `RenameErrs`, …), absent path → `fs.ErrNotExist`.
  `Stat` synthesizes `FileInfo` from `Files`/`Dirs` (`filesystem.go:73-84`);
  **`ModTime` is always the zero time** (`filesystem.go:127-128`) — so the
  extractor must **not** rely on mtime for change detection in unit tests;
  content comparison is the testable choice.

### The atomic-write idiom (mirror this)

The canonical "MkdirAll → WriteFile(tmp) → Rename(tmp, dest), Remove(tmp) on
failure" pattern, used near-verbatim in several packages:

- `internal/generator/cache.go:59-81` (`writeCache`) — primary reference;
  perm/suffix constants `cacheDirPerm=0o755`, `cacheFilePerm=0o644`,
  `cacheTempSuffix=".tmp"` at `cache.go:13-18`.
- `internal/profile/compile.go:75-89` (`write`) — leanest (writes a string);
  constants `dirPerm/filePerm/tmpSuffix` at `compile.go:12-16`; comment notes
  the C4 invariant "the only directory it creates is the state root — never
  anything inside the project tree."
- `internal/policy/compile/compile.go:517-537` and
  `internal/generator/registry/registry.go:227-249` — two more copies.

Construction pattern: a struct holds injected seams (`fs sysdep.FileSystem`,
`state.New(paths)`) wired by a `New(...)` constructor — e.g.
`internal/profile/compile.go:26-39`.

### Content hashing (if a checksum is preferred over byte-compare)

- `internal/generator/cache.go:31-34` — `sha256.Sum256` + `hex.EncodeToString`
  (full hex, content-addressed cache key).
- `internal/state/state.go:96-99` — truncated (first 8 bytes) for a readable dir
  name.

Both use `crypto/sha256` + `encoding/hex`; no crc/fnv anywhere in the repo.

## Detailed findings

### How change detection should work

The ticket asks (Questions for Research/Planning): *"Version/checksum the
embedded addon so 'differs' is cheap to detect?"*

Findings:

- The extracted filenames are **fixed** (mitmproxy loads `enforcer.py` by path),
  so the content-addressed-filename trick from `generator/cache.go` does **not**
  apply — the name can't carry the hash.
- The repo's existing read-side idiom (`cache.go:44-57`, `registry.go:185-202`)
  is "read it back, treat absent/garbage as a miss, self-heal on next run."
  Applied here: read the extracted file, compare bytes to the embedded copy;
  if absent or different, atomically (re)write it. This is:
  - **cheap** — four small files (~6–15 KB each), read once per run;
  - **self-healing** — a truncated/edited extraction is repaired;
  - **mtime-independent** — works with `FakeFileSystem` (zero ModTime), keeping
    unit tests hermetic;
  - **no extra state file** — avoids a `.version`/checksum sidecar that could
    itself drift from the actual files.
- A separate checksum/manifest file is **not recommended**: it adds a second
  source of truth, and per-file byte-compare is already O(file size) which is
  negligible here. (If the addon ever grew large this calculus could change.)

### Idempotency / refresh — the three required behaviours

From the ticket's verification steps, the extractor must satisfy:

1. **First run** writes all four files to the target dir.
2. **Re-run** is a no-op (no rewrite when content already matches).
3. **Upgraded binary** (embedded bytes changed) rewrites the differing file(s).

The byte-compare-then-atomic-write design covers all three. Closest existing
test precedents to mirror:

- `internal/generator/cache_test.go:36-55` — "second run does no work."
- `cache_test.go:57-69` — "changed input invalidates / rewrites."
- `cache_test.go:71-83` — corrupt file self-heals (miss).
- `cache_test.go:85-98` — `RenameErrs` injection: rename fails ⇒ error, no final
  file left behind.

### C4 (out-of-tree) guard

- The extractor must only ever create dirs/write files under the state root,
  never in the project tree — the same invariant the profile compiler documents
  (`compile.go:72-74`). A test should assert the extraction path is under the
  resolved cache root and that nothing is written into the project dir.
- `docs/design.md:292` states the rationale: the cage mounts `./` read-write, so
  the enforcer (like policy/lock/audit) lives out-of-tree and unmounted, so a
  prompt-injected agent can't rewrite its own controls.

### Testing conventions to follow

- **Table-driven** for pure logic (`internal/prereq/version_test.go:10-37`).
- **Hermetic fakes** — `FakeFileSystem` + `FakePathResolver`
  (`internal/profile/compile_test.go:18-25` shows both wired together).
- **Golden-file** with `-update` if any rendered artifact is produced
  (`internal/prereq/report_test.go:21,36-48`) — likely not needed here since the
  "golden" content *is* the embedded `.py` (a content-equality assertion against
  the `embed.FS` bytes is more direct).
- External tools (mitmproxy etc.) only under `//go:build integration`.

## Code references

- `internal/proxy/enforcer/enforcer.py:35-37,49-51` — AC-0019 scope note; the
  three sibling imports proving the addon is multi-file.
- `internal/proxy/enforcer/{policy,audit,responses}.py` — the other three
  runtime modules (stdlib-only imports verified).
- `internal/proxy/enforcer/requirements.txt` — dev/test pins (not shipped).
- `internal/state/state.go:32-49,101-152,154-171` — constants, roots,
  accessors; `RegistriesRoot`/`GeneratorsRoot` as the constant-location
  precedent.
- `internal/sysdep/filesystem.go:9-13,19-39,82-93` — the seam (doc names the
  enforcer), interface, `RemoveIfPresent`.
- `internal/sysdep/sysdeptest/filesystem.go:17-52,73-84,127-128` — fake;
  zero ModTime caveat.
- `internal/generator/cache.go:13-18,31-34,44-81` — perm constants, hashing,
  read/write-atomic idiom.
- `internal/profile/compile.go:12-16,26-39,72-89` — lean atomic write +
  construction + C4 comment.
- `docs/design.md:292,295,297,449,489` — out-of-tree rationale + the
  location contradiction (297 vs 449).

## Architecture / impact

- **New package `internal/proxy`** (first Go code there): an `embed.FS` of the
  four runtime modules + an `Extractor` type holding `fs sysdep.FileSystem` and
  a `*state.Resolver` (or a passed-in target dir), with a `New(...)` constructor
  and an `Extract()`-style method, mirroring `internal/profile`/`generator`.
- **`internal/state` change**: add the enforcer location. If "constant location"
  wins (recommended), add `enforcerSubdir` const + `EnforcerRoot()` accessor on
  `Resolver` (cross-project, like `RegistriesRoot`). If "per-project" wins, add
  an `enforcer` filename/dir const + a `Layout` method instead.
- **No CLI wiring in this ticket** — AC-0020 starts mitmproxy with the extracted
  path. AC-0019 only embeds + extracts. (The extractor will be *called* during
  the proxy-start path AC-0020 builds; here it just needs to exist and be tested,
  or be invoked from an existing first-run path — to confirm in planning.)
- **Doc fix**: `docs/design.md:449` should be reconciled with line 297 once the
  location is decided.

## Open Questions (for the checkpoint)

1. **Extraction location — constant vs per-project.** The ticket + `design.md:297`
   say "a constant location, not per-project file"; `design.md:449` says
   `projects/<hash>/`. This determines the `state` API. *Recommendation:* a
   single constant cross-project dir `<cache>/agent-creance/enforcer/` (mirrors
   `RegistriesRoot`/`GeneratorsRoot`; avoids N redundant copies; matches the
   ticket's explicit wording), and fix `design.md:449`.
2. **(Resolved by research, noted not asked)** Embed all four runtime modules,
   not just `enforcer.py` — forced by the sibling imports; there is no valid
   single-file option.
3. **(Resolved by research, noted not asked)** Detect "differs" via per-file
   byte comparison, not a checksum sidecar — matches repo idioms and stays
   mtime-independent for hermetic tests.
