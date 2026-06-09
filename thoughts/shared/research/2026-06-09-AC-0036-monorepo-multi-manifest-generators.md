---
date: 2026-06-09
ticket: AC-0036
title: "Monorepo support — multiple manifests per generator type"
status: complete
branch: main
commit: b8687493b22a19fcbd762a342291c9a1642cc451
---

# Research: AC-0036 — Monorepo support (multiple manifests per generator type)

## Research Question

How does the allowlist-generator framework currently encode the "one generator =
one fixed root manifest" assumption, and what are all the seams that must change
to let a generator type be listed **multiple times, each parameterized with a
manifest path**, with `init` auto-discovering monorepo packages via a bounded
scan that skips installed-dependency directories declared by the generators
themselves?

## Summary

The single-manifest assumption is encoded at **three independent chokepoints**,
all keyed by generator **type name** rather than by (type, path):

1. **Config schema** — `Egress.Generators []string` (a flat list of bare type
   names) with a strict raw-mirror decoder, string-identity merge/dedupe, and a
   config editor that doesn't touch generators at all.
2. **Compiler** — a hardcoded `type → filename` map (`manifestFiles`), a
   `readManifests` that returns `map[type][]byte` (one manifest per type), and a
   `runGenerators` fan-out loop that runs each type **once**.
3. **Attribution** — the source label `generated:<type>:<pkg>` carries no
   manifest path, so two `package_json` generators depending on the same package
   are indistinguishable (and get collapsed by rule dedupe).

Crucially, several layers are **already path-ready**:

- The generator itself takes `manifest []byte`, not a path (`Generate(ctx,
  manifest)`), so it can run any number of times against any bytes.
- The **generator-output cache is content-addressed** (`<type>/<sha256(bytes)>.json`),
  so two different manifests of the same type already produce distinct cache
  files — **no cache change needed**.
- The **input hash** hashes manifest bytes; it watches whatever `readManifests`
  collected, so it picks up arbitrary paths "for free" once the map is keyed by
  path instead of type (ticket criterion 5 confirmed).
- The renderers consume `r.Source` verbatim with dynamic column widths, so a
  longer path-qualified source renders without renderer changes.

There is **no notion of a dependency directory** (`vendor/`, `node_modules/`)
anywhere in the codebase today — that concept must be introduced, and the ticket
requires it to be **declared by the generators**, not the scanner.

A central design tension to resolve in planning: the manifest-filename↔generator
mapping currently lives in **two hardcoded places** (the compiler's
`manifestFiles` map and an inline table in `init.go`), and the generator package
exposes neither the filename it recognizes nor any dependency-dir metadata. The
ticket explicitly wants this metadata **owned by the generator implementations**
so the scanner's skip-set extends automatically when a new ecosystem is added.

## Detailed Findings

### 1. Config schema, decode, merge, edit (`internal/config/`)

**Type.** `Egress.Generators` is `[]string` of bare type names —
`internal/config/config.go:68` (public) and `:119` (raw mirror
`rawEgress.Generators`). The two are structurally identical, so decode is a
whole-struct conversion `Egress(raw.Network.Egress)` at `config.go:145` with
zero transformation.

**Strict decoder constraint (load-bearing).** The package uses a parallel set of
"raw" structs with **no custom `UnmarshalYAML` anywhere** in the type tree,
because yaml.v3's `KnownFields(true)` strict-decode is silently defeated by *any*
custom unmarshaler in the tree (documented at `config.go:10-15`, go-yaml issue
#642). `Parse` builds a decoder, calls `dec.KnownFields(true)` (`config.go:131`),
decodes into the raw mirror, then converts. **Implication for AC-0036:** a
generator entry that may be *either* a bare scalar *or* a `{type, path}` mapping
cannot be modeled with a naive `UnmarshalYAML` without disabling strict checking
for the whole document. The existing precedent for "structured entries without
breaking strict decode" is `host_services`: keep the raw field permissive and
parse it in a **post-decode pass** (`parseHostService`, `validate.go:56-73`,
wired from `applyDefaults` at `config.go:162-170`). New keys inside a generator
mapping (e.g. `path:`) only get strict-checked if they decode through a tagged
raw struct, not a custom unmarshaler.

**Generators are never validated in the config package.** `applyDefaults`
(`config.go:162-174`) and `validate` (`validate.go:12-15`) touch only rules and
host_services. Generator-name validity is checked later, in the compiler, via
`generator.Known` (`compile.go:202-206`).

**Merge / dedupe identity = exact string.** Two code paths union generators:
- `config.merge` → `dedupeStrings` keyed on whole-string `map[string]bool`
  (`merge.go:34`, `merge.go:101-115`).
- The compiler's *separate* `mergeGenerators(global, project []string)`
  (`compile.go:201`, `compile.go:340-354`), also string-keyed.

With paths, "same generator" is no longer "same string": `package_json` and
`package_json @ apps/api/package.json` must be **distinct**. The dedupe key
shifts to the `(type, path)` pair. Precedents for struct dedupe already exist:
`dedupeHostServices` (comparable-struct set, `merge.go:119-133`) fits a generator
entry with a plain string path; `dedupeRules` (`reflect.DeepEqual`,
`merge.go:141-159`) is the pointer-field variant.

**Editor (`edit.go`) does not edit generators.** `AppendRule` (`edit.go:52-81`)
is a comment-preserving textual splicer for `allow`/`deny_always` rules only
(enum `AllowList`/`DenyList`, `edit.go:30-45`). It walks `network → egress →
<list>` and never inspects/rewrites the `generators` key. The only
generators-bearing golden fixture is `with_generators.{in,golden}.yaml` under
`internal/config/testdata/edit/` (7 golden + 5 input fixtures total) — it proves
`generators` is left untouched when an `allow` list is spliced in. **So `init`
does not use the editor to emit generators** (see §3).

### 2. Generator framework (`internal/generator/`)

**No instance registry — a factory switch.** Two layers each hardcode the two
names:
- Name constants: `manifest.go:11-14` (`GeneratorPackageJSON = "package_json"`,
  `GeneratorComposerJSON = "composer_json"`).
- `Known(name) bool` switch — `generator.go:33-40`.
- `New(name, fs, clock, getter, registriesRoot, generatorsRoot) (*Generator,
  error)` factory switch — `generator.go:46-55` — wires `packageJSON{}` +
  `registry.NewNPM(...)` or `composerJSON{}` + `registry.NewPackagist(...)`.

**The `ecosystem` strategy interface** (`manifest.go:19-22`):
```go
type ecosystem interface {
    name() string
    deps(manifest []byte) ([]string, error)
}
```
Implemented by zero-field `packageJSON` (`manifest.go:25-38`) and `composerJSON`
(`manifest.go:42-62`). **It exposes neither its manifest filename nor any
dependency-dir name.** This is the surface AC-0036 must extend (the ticket wants
the generator to declare both its recognized filename and its dependency-dir
name(s)).

**The generator takes bytes, not a path.** `Generate(ctx, manifest []byte)
([]Rule, error)` (`generator.go:65`) and `Invalidate(manifest []byte)`
(`generator.go:99`). The generator never reads the file — the **caller owns the
read** (see §3). This means a single generator type can already be run N times
against N manifests with no change to the generator itself.

**The filename mapping lives outside the generator.** `manifestFiles`
(`compile.go:55-58`) maps `package_json → package.json`, `composer_json →
composer.json`. A second, inline copy lives in `init.go:80-83`. The doc comment
at `compile.go:53-54` states the contract explicitly: *"The generator itself
takes bytes, not a path, so the compiler owns this mapping (and the read)."*

**Source label.** `source(gen, pkg) = "generated:" + gen + ":" + pkg`
(`generator.go:158-160`), called at `generator.go:145` as
`source(g.eco.name(), pkg)`. Stored on `generator.Rule.Source` (`rule.go:31-35`).
**No manifest-path component.**

**FS access** is entirely through the `sysdep.FileSystem` seam — the output cache
(`cache.go`), `Invalidate` via `sysdep.RemoveIfPresent`, and the compiler's
`ReadFile`. No direct `os` calls in logic packages.

**No dependency-directory concept exists.** A grep across `internal/` finds zero
`node_modules` references and no `vendor/`-as-directory concept; the only
`vendor` hits are Packagist `vendor/name` package-name parsing
(`manifest.go:65-71`) and test fixtures. This must be introduced.

### 3. `init` command (`internal/cli/init.go`)

**Flow.** `newInitCmd` (`init.go:23`) → `RunE` → `runInit(ctx, app, ".", force)`
(`init.go:30`; dir is `"."` in production, a parameter only for tests).

**No-clobber gate** (`init.go:42-55`): `Stat` of `<dir>/.agent-creance.yaml`
(`configFile`, `run.go:23`); exists + `!force` → refuse with
`"...already exists (use --force to overwrite)"`; `fs.ErrNotExist` → proceed.

**Detection (root-only).** `detectGenerators(fsys, dir) []string`
(`init.go:75-89`) iterates the inline `{manifest, name}` table (`init.go:81-82`)
and `Stat`s exactly `<dir>/package.json` and `<dir>/composer.json`. **No
`ReadDir`, no recursion.** Order deterministic (`package_json` first). Returns at
most one entry per *type* — never per *manifest location*.

**Emission (own template, not `edit.go`).** `renderConfigTemplate(gens)`
(`init.go:117`) splices `generatorsBlock(gens)` into a `configTemplate` raw
string (`init.go:141-160`). `generatorsBlock` (`init.go:124`): empty → commented
placeholder; non-empty → **a loop writing one `      - <name>` line per slice
element** (`init.go:130-135`). **This loop is already "one entry per detected
item"** — extending to one-per-manifest is purely a matter of what the detector
returns and what each entry renders as (bare vs. parameterized). Atomic write via
`writeFileAtomic` (`init.go:99-111`, WriteFile-tmp + Rename through the seam).

**Tests.** Unit: `init_test.go` — golden `TestRenderConfigTemplate`
(`testdata/init/{none,package_only,both}.golden`, `-update` flag) and
`TestRenderConfigTemplateParses` (binds emitted YAML to
`cfg.Network.Egress.Generators`). Behavior tests seed manifests by writing into
`f.fs.Files[...]`. Testscript: `internal/cli/testdata/script/init.txtar` (single
hermetic file, `PATH=$CREANCE_BIN`, manifests provided as txtar blocks at each
subdir root).

### 4. Compiler & attribution (`internal/policy/compile/`, `render/`)

**Fan-out point — `runGenerators(ctx, gens []string, manifests map[string][]byte)`**
(`compile.go:442-467`): iterates `gens` names, pulls `manifests[name]`, calls
`runner.Run(ctx, name, manifest)` **once per type name**, stamps `r.Source`,
`r.LowerTrust`, defaults `Mode=intercept`. **This is the single fan-out point
that must iterate `(type, path)` pairs instead of unique names**, with
`manifests` keyed by path (or `(type, path)`).

**Runner seam** `generatorRunner` (`compile.go:64-70`): `Run(ctx, name,
manifest []byte)` / `Invalidate(name, manifest)` — already path-agnostic at the
bytes level; `realGenerators.Run` (`compile.go:82-88`) constructs via
`generator.New(name, …)` and calls `Generate`. Running the same name with
different bytes works as-is.

**`readManifests`** (`compile.go:358-375`): iterates `gens`, looks up
`manifestFiles[name]`, reads `filepath.Join(projectDir, file)`, stores
`manifests[name] = data` (keyed by **name**; absent manifest → `continue`). This
is the one-manifest-per-type bottleneck on the read side.

**Output cache — already per-manifest.** `cachePath =
<generatorsRoot>/<type>/<sha256(manifest bytes)>.json` (`cache.go:36-39`). Two
different `package.json` files hash differently → two cache files under the same
`<type>/`. **No change required.** (Caveat: two manifests with *identical bytes*
collide, which is correct — identical input, identical output.)

**Input hash — watches manifest bytes.** `inputHash(global, project, overlay,
manifests)` (`compile.go:381-398`) marshals the three configs + a
`Manifests map[string]string` of bytes (currently keyed by type at
`compile.go:383`) and SHA-256s it. The mechanism is path-agnostic; it watches
whatever `readManifests` collected. **Ticket criterion 5 confirmed** — editing
any referenced manifest changes the hash once the map is keyed by path. (The map
key must become the path so two manifests of one type don't overwrite each other
in the payload.) The cache gate is in `Compile` (`compile.go:240-249`): hit
requires `Version` + `InputHash` match, returns `Skipped: true` with zero work.

**Rule dedupe collapses duplicates.** `buildRuleSet` (`compile.go:417-438`)
orders global → project → generated → overlay, then `dedupe`
(`compile.go:486-501`) keyed on host/paths/methods/mode/reason (ignoring source),
**keeping the first**. So two same-type generators emitting an identical rule for
a shared dependency → only the **first** source annotation survives. This
interacts directly with the attribution-disambiguation criterion: if the source
strings differ (path-qualified), the rules are still identical on the dedupe key
and one source is still dropped. Planning must decide whether path-disambiguated
sources should change the dedupe key, or whether dropping a redundant source is
acceptable (the rule is the same host either way).

**Attribution rendering.** `policy show`/`explain` prefix each rule line with
`tag(r.Source) = "[" + source + "]"` (`render.go:271-276`), column widths
computed dynamically from `len(tag(...))` (`render.go:75,101`). A longer
path-qualified source renders without renderer changes. The single place the
label string is built is `generator.source()` (`generator.go:158-160`); the
generator has no path knowledge today, so the path must be threaded either into
the generator's construction/`Generate` or **stamped by the compiler** in
`runGenerators` where it already mutates `r.Source` (`compile.go:453-464`).

### 5. Design-doc intent (`docs/design.md`)

- Generators read `./package.json` / `./composer.json` (`design.md:158-159`).
- "No options in v0.1 ... Per-generator configuration (**custom paths**,
  transitive-deps mode, ...) is a future extension" (`design.md:177`) — AC-0036
  is the custom-path slice of that future, scoped to monorepo support.
- "Future, post-v0.1 ... Per-generator configuration: ... **custom manifest
  paths**" (`design.md:203-207`).
- Input hash "watches ... **the manifests referenced by enabled generators**"
  (`design.md:290`) — already the design intent; AC-0036 makes "referenced"
  cover arbitrary paths.
- `init` "detects existing manifests (package.json, composer.json) and
  pre-populates the generators: list accordingly" (`design.md:358-361`) — the
  scan generalizes this.
- Attribution examples `[generated:package_json:react]` (`design.md:194-196`).
- Forge→content-host mapping is **data, not per-generator code**
  (`design.md:175`) — a precedent the ticket echoes for dependency-dir metadata
  being declared/owned data rather than scanner-hardcoded.

## Change-Surface Map

| Concern | Location | Current keying | Ready? |
|---|---|---|---|
| Config generator list | `config.go:68,119` | `[]string` bare name | No — no path slot |
| Strict decode | `config.go:10-15,131` | raw mirror, no custom unmarshaler | Constraint — use post-decode pass |
| Merge/dedupe | `merge.go:34,101-115`; `compile.go:340-354` | exact string | No — needs `(type,path)` |
| Config editor | `edit.go` | rules only | N/A — init emits its own YAML |
| Generator strategy iface | `manifest.go:19-22` | `name()`+`deps()` | No — add filename + dep-dir decl |
| Generator run | `generator.go:65,99` | `manifest []byte` | **Yes** — path-agnostic |
| Type→filename map | `compile.go:55-58`; `init.go:80-83` | type → fixed root file (×2 copies) | No — duplicated, should move to generator |
| Manifest read & map | `compile.go:358-375` | `map[type][]byte` | No — one per type |
| **Compiler fan-out** | `compile.go:442-467` | one `Run` per name | **No — single fan-out point** |
| Output cache | `cache.go:31-39` | `<type>/<sha256(bytes)>` | **Yes — content-addressed** |
| Input hash | `compile.go:381-398` | bytes; map keyed by type | Mechanism ready; rekey by path |
| Source label | `generator.go:158-160` | `generated:<type>:<pkg>` | No — add path |
| Rule dedupe | `compile.go:486-501` | host/paths/methods/mode/reason | Caveat — collapses dup sources |
| Renderers | `render.go:271-276` | verbatim `r.Source` | **Yes** |
| init detection | `init.go:75-89` | root-only `Stat` ×2 | No — needs ≤2-level `ReadDir` |
| init emission loop | `init.go:130-135` | one line per slice elem | **Yes** — already per-item |
| sysdep FS scan | `filesystem.go:35` (`ReadDir`) | sorted entries, `ErrNotExist` on absent | **Yes** — fakes support it |

## Code References

- `internal/config/config.go:68,119,128-157,162-174` — generators type, raw mirror, `Parse`, defaults
- `internal/config/validate.go:56-73` — `parseHostService` post-decode pattern (the strict-safe model)
- `internal/config/merge.go:34,101-115,119-133,141-159` — generator dedupe + struct-dedupe precedents
- `internal/config/edit.go:30-45,52-81,86-129` — `AppendRule`, list enum, `planInsert` (rules only)
- `internal/config/testdata/edit/with_generators.{in,golden}.yaml` — generators-untouched fixture
- `internal/generator/manifest.go:11-14,19-22,25-62` — name constants, `ecosystem` iface, impls
- `internal/generator/generator.go:33-40,46-55,65,99,145,158-160` — `Known`, `New`, `Generate`, `source`
- `internal/generator/cache.go:31-39` — content-addressed output cache
- `internal/generator/registry/registry.go:88,93,155-160` — per-package cross-project registry cache
- `internal/policy/compile/compile.go:53-58,164-172,178-227,232-252,340-354,358-375,381-398,417-438,442-467,486-501` — `manifestFiles`, resolve/compile/build, merge, read, hash, fan-out, dedupe
- `internal/policy/render/render.go:39-153,261-276` — `Show`/`Explain`, `tag()`
- `internal/cli/init.go:23-30,42-55,75-89,99-111,117-160` — init flow, no-clobber, detection, emission
- `internal/cli/init_test.go`, `internal/cli/testdata/script/init.txtar` — init tests
- `internal/sysdep/filesystem.go:19-44` — `FileSystem` iface (`ReadDir` at :35)
- `internal/sysdep/sysdeptest/filesystem.go:19,107-137,202-217` — fake `ReadDir`/dir entries
- `docs/design.md:154-207,290,358-361` — generators, options-future, input hash, init

## Related Documentation

- `thoughts/shared/tickets/AC-0036-monorepo-multi-manifest-generators.md` — this ticket
- `thoughts/shared/research/2026-06-05-AC-0012-allowlist-generators.md` + plan — generator design
- `thoughts/shared/research/2026-06-05-AC-0013-policy-compiler.md` + plan — compiler + input-hash cache
- `thoughts/shared/research/2026-06-05-AC-0015-policy-show-explain.md` + plan — attribution rendering
- `thoughts/shared/research/2026-06-05-AC-0007-config-schema-loader.md` + plan — schema, strict decoder
- `thoughts/shared/research/2026-06-05-AC-0008-config-include-merge.md` + plan — merge/dedupe semantics
- `thoughts/shared/research/2026-06-07-AC-0029-init-command.md` + plan — init detection + no-clobber
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — overarching spec

## Open Questions for Planning

These map onto the ticket's "Questions for Research/Planning". Research narrowed
the option space; the remaining decisions need maintainer judgment:

1. **Schema shape.** Map form `- package_json: apps/web/package.json` (alongside
   bare `- package_json`) vs. object form `- {type:, path:}`. Research favors a
   shape parseable by a **permissive raw field + post-decode conversion** (the
   `host_services` pattern) to preserve `KnownFields(true)`. Which wins?

2. **Generator-metadata ownership & registry surface.** The ticket wants the
   generator to declare its manifest filename and dependency-dir name(s). This
   means extending the `ecosystem` interface (or adding a registry/metadata
   table in `internal/generator/`) and **collapsing the two duplicated
   `type→filename` tables** (`compile.go:55-58`, `init.go:80-83`) into that
   single source. Confirm the shape/location.

3. **Root vs. sub emission style in `init`.** Emit root manifest as the bare form
   and sub-packages as parameterized, or all parameterized for consistency?
   (Affects golden output and the `init.go:130-135` loop.)

4. **Attribution disambiguation + dedupe interaction.** How does the manifest
   path enter `generated:<type>:<pkg>` (e.g.
   `generated:package_json:apps/web/package.json:react`), and should a
   path-qualified source change the **rule dedupe key** (`compile.go:486-501`) so
   both sources survive, or is dropping a redundant source for an identical host
   acceptable?

5. **Scan depth semantics & determinism.** Is the project root level 0 (so
   levels 0–2 inclusive)? Ordering of emitted entries for stable golden output
   (the fake `ReadDir` returns name-sorted entries — root-first, then sorted
   subdirs is the natural deterministic order). Symlink/cycle safety during the
   bounded walk (the `sysdep` FS seam exposes `ReadDir`/`Stat`/`IsDir`; symlink
   handling is whatever `os.ReadDir`'s `DirEntry.Type()` reports — decide whether
   to skip symlinked dirs).

6. **Compiler fan-out & input-hash rekey.** Confirm `runGenerators` iterates
   `(type, path)` pairs and that `readManifests`/`inputHash` rekey their maps by
   path (not type) so two manifests of one type don't collide.
