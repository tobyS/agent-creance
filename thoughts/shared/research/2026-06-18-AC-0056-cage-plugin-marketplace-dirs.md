---
date: 2026-06-18
ticket: AC-0056
branch: main
commit: a512476cf8e12385f000a2fbd4b044d96834bfd4
status: complete
---

# Research: Grant local Claude plugin marketplace directories into the cage (AC-0056)

## Research question

Caged Claude Code `EPERM`-fails to load a local-directory plugin marketplace
(`toby-plugins` at `/Users/toby/code/work/toby-plugins`) because that path is
outside the cage's filesystem mounts. How does the cage assemble its mounts, how
does the existing Claude-config import work (to mirror it), where does Claude
store the marketplace registration, and what is the cleanest way to grant the
needed directories read-only?

## Summary / answer

- **Exact mechanism of the failure.** Claude Code records every registered
  marketplace in `~/.claude/plugins/known_marketplaces.json`, a JSON object keyed
  by marketplace name. A **local** marketplace is the entry whose
  `source.source == "directory"` (or `"file"`); its absolute path is in
  `source.path` (mirrored in `installLocation`). On every startup Claude reads
  `<source.path>/.claude-plugin/marketplace.json` to load the catalog. The cage
  mounts only the project dir and `~/.claude`, so a `source.path` outside both is
  Seatbelt-denied (`EPERM`) and the marketplace — plus its plugins — fail to load.

- **Scope is narrow.** Installed plugin *runtime* files are copied into
  `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/`, and git-source
  marketplaces clone into `~/.claude/plugins/marketplaces/<name>/` — **both already
  readable** (under the RW `~/.claude` mount). Only **`directory`/`file`-source
  marketplace roots outside `~/.claude`** need granting. One read-only mount per
  such root covers its `.claude-plugin/marketplace.json` and the plugin subdirs it
  references (relative `./plugins/...` paths).

- **Live confirmation (this machine).** `known_marketplaces.json` holds four
  entries; three are `github` sources under `~/.claude/plugins/marketplaces/`
  (already mounted), and exactly one — `toby-plugins` — is
  `{"source":"directory","path":"/Users/toby/code/work/toby-plugins"}`, matching
  the error. So the feature would mount exactly that one directory read-only.

- **The cage mount chokepoint.** `cage.Build` (`internal/cage/cage.go:98-113`) is
  the single place the safehouse `--add-dirs` (RW) / `--add-dirs-ro` (RO) lists are
  finalized. RO comes verbatim from `config.Safehouse.AddDirsRO`; `~/.claude` is
  unconditionally appended to the RW list at `cage.go:109`. `Build` is a **pure**
  function (golden-tested) — any directory *detection* (I/O) must happen earlier
  (in `Builder.Resolve` or in `runRun`) and be fed in via `cage.Inputs`.

- **No existing path writes `add_dirs_ro` from detection.** The Claude-config
  importer (`internal/claudeimport`) produces only egress rules + ports, consumed
  by `init` (`gatherImports`). The `import` command **deliberately ignores** a
  `safehouse:` block in a fragment (`internal/cli/import.go:147`,
  `ignoredSectionsNote`), and `init`'s template hardcodes
  `safehouse:\n  add_dirs_rw: [.]` (`internal/cli/init.go:402-403`). AC-0056 is the
  first feature to derive a safehouse mount from detection.

- **Key design fork (for the checkpoint).** Marketplaces are **per-user**, not
  per-project, and the catalog is reloaded on **every** cage launch. That makes a
  *static* "write into a config file at init" approach awkward (it would belong in
  the global config, and goes stale when marketplaces change), and makes a
  *dynamic* "detect at each launch and grant read-only" approach naturally correct
  and self-maintaining. See Open Questions.

## Detailed findings

### How the cage assembles filesystem mounts

- Entry: `internal/cli/run.go:50` `runRun` → loads merged config
  (`run.go:114-118`), resolves project layout (`run.go:83`,
  `state.Resolver.Resolve` = `Abs`+`EvalSymlinks`+hash), then builds the cage
  (`run.go:156-179`).
- `cage.New(...).Resolve(cfg, layout, port)` packs config+layout+port+home into
  `cage.Inputs` (`internal/cage/cage.go:173-185`); the only I/O is
  `paths.UserHomeDir()`.
- `cage.Build(in)` (pure, `cage.go:87-156`) emits the argv. Mount assembly
  (`cage.go:98-113`):
  - `rw := append(sh.AddDirsRW..., filepath.Join(in.HomeDir, ".claude"))` →
    `--add-dirs` (colon-joined). `~/.claude` is **always** RW (`cage.go:109`,
    AC-0045 posture).
  - `if len(sh.AddDirsRO) > 0 { args += "--add-dirs-ro", expandColonList(sh.AddDirsRO) }`
    (`cage.go:111-113`). **This is the RO chokepoint.**
- Path expansion: `expandPath` (`cage.go:304-315`) maps `~`/`~/x`→home, absolute→
  `Clean`, relative→`Join(projectDir, p)`; `expandColonList` (`cage.go:319-325`)
  joins with `:`. **No dedup / overlap collapse** at this layer.
- Config-merge dedup (`internal/config/merge.go:28,113-127`) removes only **exact
  string** duplicates per list, pre-expansion — so `.`/`/proj` or
  `~/.claude`/`/home/x/.claude` are not recognized as equal there.
- Tested via the `invocation.golden.json` golden (`cage_test.go:48-64`; fixture
  sets `AddDirsRO: ["~/.config/git"]`) and end-to-end via the `ProcessGroup` fake
  in `run_test.go` (`argsContain(started[0].Args, "--add-dirs-ro", <path>)`).
  Real safehouse is only exec'd under `//go:build integration`.

### Where Claude stores marketplaces (on-disk format)

- **`~/.claude/plugins/known_marketplaces.json`** — per-user, JSON keyed by name.
  Per entry: `source` (object), `installLocation` (absolute), `lastUpdated`.
  Official docs confirm the path and per-user nature
  (https://code.claude.com/docs/en/plugin-marketplaces). Field names beyond
  `source` are observed-from-disk, not contractually documented — treat as stable
  today but version-sensitive.
- Source discriminator `source.source`:
  - `"directory"` / `"file"` → **local**; path in `source.path` (== `installLocation`,
    referenced in place).
  - `"github"` → `source.repo`; cloned under `~/.claude/plugins/marketplaces/<name>`.
  - `"url"` / `"git-subdir"` → `source.url` (+ in-repo `path` for subdir).
- Installed plugins: copied to `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/`,
  tracked in `~/.claude/plugins/installed_plugins.json` (`installPath`). Under
  `~/.claude` → already readable.
- `<root>/.claude-plugin/marketplace.json`: `{name, owner, plugins[]}`; each plugin
  `source` is a relative `./path` string (within the root) or a typed object.
- Optional extra surface: project/local-scope marketplaces can also be declared in
  `.claude/settings.json` via `extraKnownMarketplaces`; `known_marketplaces.json`
  is the resolved per-user store. Enumerable officially via
  `claude plugin marketplace list --json` (documented fields `name`, `source`,
  `repo`/`url`/`path`).

### The existing Claude-config import (pattern to mirror)

- `internal/claudeimport/claudeimport.go` — `Project(fs, paths, dir)` /
  `Global(fs, paths)` return `(Result{WebRules, MCPRules, Ports}, []string warns)`.
  Lenient reader `readJSON` (`:279-292`): absent file → skip, **no warning**;
  unreadable/parse error → one warning appended to a `*[]string`. Home via
  `paths.UserHomeDir()`.
- Wiring: `internal/cli/init_imports.go` `gatherImports` (`:22-69`) calls the
  importer, confirm-gates each source, `printWarnings` to stdout (`:71-75`),
  dedups by key (`dedupeRulesByHost` `:79-105`). `init` renders + atomically writes
  (`init.go:325-335`).
- Tests: `claudeimport_test.go` seeds fake files in `FakeFileSystem.Files` by
  absolute path, sets `FakePathResolver.HomeDir`, asserts the warning contract
  (0 for missing, 1 for malformed/unreadable). `init_imports_test.go` drives the
  whole command with `f.term.Interactive=true` + scripted stdin.

### Reusable path/normalization patterns

- Canonical project identity: `internal/state/state.go:85-100` (`Abs` +
  `EvalSymlinks`) — the basis for deduping a detected dir against the project dir
  and `~/.claude`.
- Mount-path expansion: `cage.expandPath`/`expandColonList` (above).
- Dedup-by-key idiom: `map[string]bool` "seen" keyed on the canonical string
  (`claudeimport` `orderedSet` `:354-376`; `init_imports.go` dedupers).
- Warning surfacing: collect into `*[]string`, print non-fatally
  (`init_imports.go:71-75` to stdout for init-time; `run.go:186-193`
  `warnVersionSkew` to stderr for launch-time).

## Code references

- `internal/cage/cage.go:98-113` — RW/RO mount assembly; **RO chokepoint** at 111-113.
- `internal/cage/cage.go:109` — `~/.claude` unconditionally RW.
- `internal/cage/cage.go:173-185` — `Resolve` (I/O: `UserHomeDir`); `:304-325` path expansion.
- `internal/cli/run.go:50,114-118,156-179` — run setup → config load → cage build/run.
- `internal/config/config.go:45-49,119-123` — `Safehouse{AddDirsRW,AddDirsRO,Enable}` (yaml `add_dirs_ro`).
- `internal/config/merge.go:28,113-127` — exact-string dedup per list.
- `internal/claudeimport/claudeimport.go:61,88,279-292` — importer + lenient reader.
- `internal/cli/init_imports.go:22-105` — detection wiring, confirm-gating, dedup, `printWarnings`.
- `internal/cli/import.go:142-164` — safehouse block ignored by `import`.
- `internal/cli/init.go:343-368,402-403` — config template; hardcoded `add_dirs_rw: [.]`.
- `internal/state/state.go:85-100` — canonical path resolution.
- `internal/cage/cage_test.go:48-64`, `internal/cli/run_test.go` (`argsContain`) — mount tests.
- Live: `~/.claude/plugins/known_marketplaces.json` (`toby-plugins` = directory source).

## Architecture insights

- **`cage.Build` must stay pure.** Marketplace detection is I/O; do it in
  `Builder.Resolve` (already does `UserHomeDir`) or in `runRun`, and feed the
  resolved RO dirs into `cage.Inputs` so `Build` simply appends them to the RO
  list. This preserves the golden-test design.
- **Read-only is sufficient and correct** — the agent never needs to write the
  marketplace source; mounting RO is the narrowest grant.
- **Dedup must be post-canonicalization** — a detected dir already inside the
  project or `~/.claude` must not be mounted again; compare canonical (`Abs` +
  `EvalSymlinks`) strings, not raw config strings (merge dedup won't catch these).
- **Only `directory`/`file` sources outside `~/.claude` matter** — skip git
  sources and any path already under a mounted root.

## Open questions (for the Phase 2 checkpoint)

1. **Detection timing / persistence — dynamic at launch vs static into config.**
   - *Dynamic (recommended by research):* read `known_marketplaces.json` during
     run setup, grant `directory`/`file` source dirs (outside the cage) read-only
     via `cage.Inputs`, print a one-line notice. Always current; self-maintaining
     as marketplaces change; naturally per-user. Trade: the mount is not written
     into a config file (less "auditable in YAML", though shown at launch).
   - *Static:* detect at `init`/global-seed time and write visible
     `safehouse.add_dirs_ro` entries. Auditable in config, but per-user
     marketplaces really belong in the **global** config, and the list goes stale
     when marketplaces are added/removed (needs a re-run).
2. (Bakeable unless the user objects) Print a one-line launch notice listing the
   granted marketplace dir(s) — consistent with `warnVersionSkew` on stderr.
3. (Planning detail) Whether to also honor project/local-scope marketplaces in
   `.claude/settings.json` `extraKnownMarketplaces`, or rely solely on the
   resolved `known_marketplaces.json`.

## tce Config Drift

`.claude/tce/tickets.md` is **missing** (only `config` + `profile.md` exist under
`.claude/tce/`). The project nonetheless operates a working **tmt** ticket system
(prefix `AC`, files in `thoughts/shared/tickets/`, `**Status:**` line, docs-only
commits). The tce workflow commands expect `tickets.md` to define the ID/read/
create/status mechanics. Recommend running `/tce:refresh` (or `/tce:init`) to
regenerate `tickets.md` so the adapter is explicit rather than inferred. Advisory
only — no config was edited by this research.
