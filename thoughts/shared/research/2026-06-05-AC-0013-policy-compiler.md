---
date: 2026-06-05
ticket: AC-0013
git_commit: 3c1b50f4972a8b52d2c274260404935c00e96576
branch: main
tags:
  - research
  - AC-0013
  - policy-compiler
  - WP-2.4
---

# Research: AC-0013 — Policy compiler → `policy.json` with input-hash cache (WP-2.4)

## Research Question

How should `internal/*` compile the effective config (explicit rules + generator
output + included files + global + session-overlay) into an out-of-tree
`policy.json`, with each rule annotated by source, gated by an input-hash cache —
given the structures AC-0006/0008/0010/0012 already provide?

## Summary

AC-0013 is the **convergence point** of the Phase-2 work. Almost everything it needs
already exists; the compiler is mostly *orchestration glue* plus two genuinely new
pieces of design:

1. **A source-annotated, optionally-versioned on-disk schema.** The matcher type
   `policy.RuleSet` (and its `policy.Rule`) is already the de-facto `policy.json`
   shape, but it carries **no per-rule source annotation** and **no version field** —
   both are required/asked-for by the ticket. This needs a small schema extension.
2. **An input-hash cache keyed on the merged inputs + referenced manifests**, written
   atomically to `Layout.PolicyJSON()`.

The single significant integration *gap*: `config.Loader.Load` **fuses** the global +
project + includes into one flat `*Config` and **discards provenance** (confirmed: no
source tracking anywhere in `internal/config`). To annotate rules as `explicit` vs
`global` vs `once`, the compiler must load the layers separately rather than call the
existing fused `Load`. This is the main design decision (Q2 below).

Everything else — generator dispatch (`generator.New`/`Generate`), the project-state
path scheme (`state.Layout`), the config→rule bridge (`policy.FromConfig`), the atomic
temp-write-then-rename idiom, the golden-file `-update` pattern, the `sysdep`
filesystem seam — is already in place and directly reusable.

## Detailed Findings

### The data the compiler consumes

**Effective config (`internal/config`).**
- `config.Config` — `internal/config/config.go:29-35`. After resolution, `Include` is
  cleared; list fields (`Egress.Allow`, `Egress.DenyAlways`, generators, host services)
  are unioned + deduped.
- `config.Egress` — `config.go:67-71`: `Generators []string`, `Allow []Rule`,
  `DenyAlways []Rule`.
- `config.Rule` — `config.go:77-83`: `Host string`, `Paths *[]string`,
  `Methods *[]string`, `Mode string`, `Reason string`. The `*[]string` pointers
  distinguish an omitted key (`nil`) from an explicit empty list. Mode constants
  `ModeIntercept`/`ModePassthrough` at `config.go:86-89`; default applied by
  `defaultRuleModes` (`config.go:176-182`).
- Loader: `config.NewLoader(fs sysdep.FileSystem, paths sysdep.PathResolver) *Loader`
  (`load.go:39`); `(*Loader).Load(projectPath string) (*Config, error)` (`load.go:48`).
  Global path is computed *inside* `Load` as `~/.config/agent-creance.yaml`
  (`load.go:55`). The recursive workhorse `resolve(path, home, optional, stack, depth)`
  (`load.go:77`) is **unexported** — it does include resolution, cycle detection
  (`ErrIncludeCycle`), and the depth limit (`maxIncludeDepth = 10`).
- **No provenance**: `merge` (`merge.go:20`) concatenates rules from different files
  into flat `[]Rule` with no per-element origin. `MatchedRule` identifies a rule only by
  `{List, Index}` (`policy.go:94-97`). Confirmed there is no source/origin/FromFile
  field anywhere in the config package.

**Generator output (`internal/generator`, AC-0012).**
- Dispatch is a **constructor switch**, not a registry map:
  `generator.New(name, fs, clock, getter, registriesRoot, generatorsRoot) (*Generator, error)`
  (`generator/generator.go:43`), validated by `generator.Known(name) bool`
  (`generator.go:30`). Names: `GeneratorPackageJSON = "package_json"`,
  `GeneratorComposerJSON = "composer_json"` (`manifest.go:11-14`).
- `(*Generator).Generate(ctx, manifest []byte) ([]Rule, error)` (`generator.go:62`) —
  **takes manifest bytes, not a path**. It internally content-addresses an output cache
  by `sha256(manifest)` under `<generatorsRoot>/<name>/<hash>.json`
  (`cache.go:31-39`); a cache hit makes zero registry lookups.
- `generator.Rule` — `generator/rule.go:31-35`:
  ```go
  type Rule struct {
      Rule       policy.Rule `json:"rule"`
      Source     string      `json:"source"`       // e.g. "generated:package_json:react"
      LowerTrust bool        `json:"lower_trust,omitempty"`
  }
  ```
  `Source` is built by `source(gen, pkg)` → `"generated:" + gen + ":" + pkg`
  (`generator.go:113`). `LowerTrust` flags host-wide companion CDN rules (e.g.
  `objects.githubusercontent.com`) a stricter model can drop (`forge.go`).
- **No method returns the referenced manifest path.** The generator never reads a file
  from disk by path — the caller supplies bytes. The name→filename mapping
  (`package_json`→`package.json`, `composer_json`→`composer.json`) is implicit; the
  compiler must own it (read the manifest, pass bytes to `Generate`, and fold the
  manifest bytes into the *policy* input hash).

**Matcher / on-disk schema (`internal/policy`, AC-0010).**
- `policy.RuleSet` — `policy.go:78-81`: `Allow []Rule`, `DenyAlways []Rule`, both with
  `json:",omitempty"`. **This is already the `policy.json` shape** that the matcher and
  (future) `enforcer.py` read.
- `policy.Rule` — `policy.go:68-74`: `Host`/`Paths`/`Methods`/`Mode`/`Reason` with json
  tags, plain `[]string`. **No `source`, no `version`.**
- `policy.FromConfig(e config.Egress) RuleSet` (`policy.go:112`) — the existing bridge,
  dereferencing `*[]string` (nil pointer → nil slice). The package doc explicitly names
  AC-0013 as the consumer (`policy.go:5-6`, `policy.go:110-111`).
- **Cross-language contract**: the decision-vector corpus
  (`internal/policy/vectors_test.go:20`, cross-cutting C1) decodes
  `Ruleset RuleSet json:"ruleset"`. Any schema change to `policy.Rule`/`RuleSet` must
  keep these vectors valid. Adding `omitempty` fields (absent in vectors) is safe.

### Where the artifact goes (`internal/state`)

- `state.New(paths sysdep.PathResolver) *Resolver` (`state.go:58`);
  `(*Resolver).Resolve(dir string) (Layout, error)` (`state.go:77`) canonicalizes
  (`Abs` → `EvalSymlinks`), hashes, and returns a `Layout{Canonical, Hash, Root}`.
- Project identity hash: `hashPath` (`state.go:96`) = first **8 bytes** of
  `sha256(canonical-path)` → 16 hex chars. (Distinct from the *content* hashing this
  ticket adds.)
- **`Layout.PolicyJSON()` → `<Root>/policy.json`** (`state.go:155`) — the exact write
  target. `Layout.SessionOverlay()` → `<Root>/session-overlay.yaml` (`state.go:171`) —
  the overlay file already has a defined home. No new path helper is strictly required
  unless we add a sidecar hash file (see Q2/Q3).
- `(*Resolver).GeneratorsRoot()` (`state.go:132`) and `RegistriesRoot()`
  (`state.go:117`) supply the two roots `generator.New` needs; both are
  **project-independent** (siblings of `projects/`), so two projects sharing a manifest
  share generated rules.

### How to hash / cache (existing patterns)

- Two production hashing sites, both `crypto/sha256` + `encoding/hex`:
  - project identity, 8-byte truncation (`state.go:96`);
  - generator output cache, **full 32-byte** hex over raw manifest bytes, no
    canonicalization, no TTL (`generator/cache.go:31`).
- Atomic write idiom to mirror: `MkdirAll` parent → `WriteFile(path+".tmp")` →
  `Rename` → best-effort `Remove` on failure (`generator/cache.go:62-81`,
  `registry.go:217`).
- The registry client caches by **timestamp** (30-day refresh), not hash
  (`registry.go:35,185,221`) — not relevant to the policy input hash, but it means a
  registry-metadata change is *not* detected by the policy input hash (correct: that's
  the registry layer's job; `policy refresh` forces it).

### sysdep seams + test patterns

- `sysdep.FileSystem` (`filesystem.go:18-38`): `ReadFile`, `WriteFile(name,data,perm)`,
  `Stat`, `MkdirAll`, `Remove`, `Rename`. Fake: `sysdeptest.FakeFileSystem`
  (`sysdeptest/filesystem.go`), in-memory with per-op error knobs. **Caveat:**
  `fakeFileInfo.ModTime()` returns the zero time — the fake does not model mtime, so an
  **mtime-based** cache cannot be unit-tested. (Implication for acceptance criterion 4 —
  see Open Questions.)
- `sysdep.Clock` (`clock.go`), `sysdep.PathResolver` (`pathresolver.go`) — fakes in
  `sysdeptest`.
- Golden pattern: `internal/prereq/report_test.go:21,36-48` — package-level
  `var update = flag.Bool("update", …)`, build artifact, `testdata/*.golden`, write on
  `-update` else compare with `require.Equal`. Wired to `make golden` (= `go test
  ./... -update`). This is the model for a golden `policy.json` test.
- CLI behavior (if a `policy compile`/`policy show` surface is ever added) → hermetic
  testscript `.txtar` under `internal/cli/testdata/script/`. **Out of scope here** —
  AC-0013 is the library compiler; no command is required by its acceptance criteria.

## Code References

- `internal/config/config.go:29-89` — Config/Egress/Rule types + mode constants
- `internal/config/load.go:48-133` — `Load` (fused global+project) and unexported `resolve`
- `internal/config/merge.go:20-176` — union/dedupe merge (provenance discarded here)
- `internal/generator/generator.go:30-115` — `Known`, `New`, `Generate`, `source`
- `internal/generator/rule.go:31-35` — `generator.Rule` (embeds `policy.Rule` + Source/LowerTrust)
- `internal/generator/cache.go:31-81` — content-hash output cache + atomic write idiom
- `internal/policy/policy.go:60-138` — Rule/RuleSet/FromConfig + mode constants + doc naming AC-0013
- `internal/policy/vectors_test.go:20` — cross-language corpus consuming `RuleSet`
- `internal/state/state.go:77-171` — Resolve/Layout/PolicyJSON/SessionOverlay/GeneratorsRoot
- `internal/sysdep/filesystem.go:18-38` — FileSystem seam
- `internal/prereq/report_test.go:21-48` — golden `-update` pattern

## Architecture Insights

- **Keep `internal/policy` pure.** Its package doc states it is "deliberately pure: no
  filesystem, no clock, no OS" (`policy.go:1-6`). The compiler does I/O + network
  (generators) + orchestration, so it belongs in a **new package** (e.g.
  `internal/compiler`) that imports `policy`, `config`, `generator`, `state`, `sysdep`.
  The *pure schema types* (a versioned/annotated artifact) can live in `internal/policy`
  (types + marshal only, no I/O); the side-effecting `Compiler` lives in the new package.
- **Provenance must be reconstructed by layered loading**, because `Load` fuses and
  discards it (see Summary). The annotation taxonomy the ticket/design wants is a
  **3-bucket** problem, not per-include: `explicit` (project YAML + its includes),
  `global` (the implicit `~/.config/agent-creance.yaml` + its includes), `once` (the
  session-overlay), plus `generated:<gen>:<pkg>` from generator output. Per-include
  granularity is **not** required by the acceptance criteria.
- **The session overlay is "just another config file"** unioned on top (design.md
  "Session-scoped allows"). The compiler reads `Layout.SessionOverlay()` if present,
  parses it with the same schema, and tags its rules `once`. AC-0030 writes it; AC-0013
  only reads it. Treat absent overlay as empty.
- **The input hash must include the manifests**, since editing `package.json` must bust
  the policy cache even though no `.yaml` changed (acceptance criterion 4). The generator
  has its *own* manifest-hash cache, but that only short-circuits the registry fetch; the
  *policy* compiler needs the manifest bytes in *its* key to decide skip-vs-recompile.
- **`policy.json` lives at a fixed path** (`Layout.PolicyJSON()`), not a content-addressed
  path, because the proxy polls a fixed file and hot-reloads on mtime change (design.md
  "Config compilation"). So the cache cannot key the *path* on the hash; instead the
  compiler stores the input hash (sidecar file or an in-artifact field) and compares on
  the next run, skipping the write (and thus the mtime bump) on a hit.

## Open Questions (for the AC-0013 checkpoint)

These map directly to the ticket's "Questions for Research/Planning".

**Q1 — Version field in `policy.json`?** Currently nothing in the repo carries a schema
version (`RuleSet`, generator `cacheRecord` — none). The ticket asks about forward-compat
with `enforcer.py` (a *second*, Python implementation that parses this file). Options:
(a) add a top-level `version` (cheap cross-language insurance, mirrors the "named-key
wrapper so it can grow" rationale already used for the generator cache record); (b) defer
until a breaking change actually arrives (YAGNI; the repo's current convention).
*Per-rule `source` annotation is required regardless* (acceptance criterion 2) — that is
not optional, only the `version` field is in question. **Recommendation: add
`version: 1`** — a security-critical artifact parsed by two languages is exactly where a
version pays off, and it is one field.

**Q2 — Provenance / loader strategy.** To emit `explicit`/`global`/`once` annotations the
compiler needs layer separation that `Load` destroys. Options:
(a) **Compiler-owned layered load** (recommended): add a thin *additive* exported method
to `config.Loader` — e.g. `ResolveLayer(path string, optional bool) (*Config, error)`
wrapping the existing `resolve(...)`, plus exposing the global path — and have the
compiler call it three times (global, project, overlay), tagging each bucket. No change
to `merge`/`Load`/validation semantics; no risk to AC-0008's behavior.
(b) **Thread provenance through the loader**: extend the config types + `merge` to carry a
per-rule source through the existing fused `Load`. More invasive, changes shared types and
merge semantics, touches the decision-vector-adjacent path.
**Recommendation: (a)** — smaller blast radius, keeps the fused `Load` untouched for its
existing callers.

**Q3 — Hash algorithm + what is canonicalized.** Establish `sha256` (consistent with the
rest of the repo). The question is *what bytes go in*. Options:
(a) **Canonical serialization of the resolved inputs** (recommended): `sha256` over
`json.Marshal` of the three resolved config layers (global/project/overlay — Go struct +
sorted-map JSON is deterministic) concatenated with the referenced manifest bytes in a
fixed order. Robust to cosmetic YAML edits, requires no enumeration of raw include files,
and is "never stale" (identical merged inputs ⇒ identical output). (b) **Raw input file
bytes**: hash each input file's bytes in deterministic order — the literal reading of the
design, but requires the loader to enumerate every transitively-included file (an extra
API), and busts the cache on comment/whitespace-only edits.
**Recommendation: (a).** Store the resulting hash either as a sidecar (`<Root>/policy.hash`,
needs a one-line `Layout` helper) or as an in-artifact field (`input_hash`, ignored by
matcher/enforcer); **recommendation: in-artifact field** for an atomic single-file cache
with no two-file consistency window.

**Q4 — "Touching the overlay forces regeneration" (acceptance criterion 4).** If "touch"
means *content change* (adding/removing a `--once` rule), a content hash (Q3a) handles it.
If it means a literal `touch(1)` mtime bump with identical content, a content hash will
**not** regenerate — and crucially the test fake (`FakeFileSystem`) does not model mtime,
so an mtime-based cache could not be unit-tested anyway. **Recommendation/assumption:**
interpret it as content change (the security-relevant case), and write the criterion-4
test as "mutate overlay content ⇒ recompiles". Flagging because it slightly reinterprets
the ticket's wording.

**Q5 — Carry `lower_trust` into the artifact?** `generator.Rule.LowerTrust` flags
host-wide CDN companion rules that `policy show` is meant to surface distinctly (design.md
"Forge content hosts" / "Visibility"). Preserving it now (one `omitempty` field on the
annotated rule) avoids a schema re-spin when `policy show` lands. Out of AC-0013's strict
acceptance criteria but cheap and forward-looking. **Recommendation: include it.**

## Related Research / Sources

- `docs/design.md` — "Config compilation", "Session-scoped allows", "Allowlist
  generators" / "Visibility", "Per-host enforcement modes".
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — WP-2.4.
- Prior tickets in the convergence set: AC-0006 (WP-1.1 config parse), AC-0008 (WP-1.3
  loader/merge), AC-0010 (WP-2.1 matcher), AC-0012 (WP-2.3 generators).
