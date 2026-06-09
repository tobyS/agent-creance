# AC-0036: Monorepo support — multiple manifests per generator type

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-06-09
**Updated:** 2026-06-09

## Problem Statement

The allowlist generators assume a single-package repository: each generator
(`package_json`, `composer_json`) is named once and reads one fixed manifest at
the project root (`./package.json`, `./composer.json`). Monorepos — multiple
packages of the same ecosystem under one repo (`apps/web/package.json`,
`apps/api/package.json`, `packages/ui/package.json`, …) — are a common,
first-class layout (and the maintainer's own preferred way of working), but
there is no way to express them: a generator type can appear only once and its
path is not configurable. A monorepo user gets dependency allow-rules for at
most one of their packages and must hand-allowlist the rest.

`init` (AC-0029) compounds this: it detects manifests only at the project root,
so monorepo packages are never auto-discovered, and a naive deep scan would
fall into the inverse trap — descending into installed-dependency folders
(`vendor/<pkg>/composer.json`, `node_modules/<pkg>/package.json`) and
mistaking installed dependencies for monorepo packages.

## Desired Outcome

When this is complete:

1. A generator can be listed **multiple times for the same type**, each
   **parameterized with the manifest path it reads** — so a monorepo gets
   dependency allow-rules for every one of its packages.
2. The existing **bare-string form keeps working unchanged** (`package_json`
   means `./package.json`); the path parameter is additive and optional. No
   migration is forced on existing project or global configs.
3. `init` performs a **bounded scan (≤2 directory levels deep)** of the
   repository and pre-populates one generator entry per detected manifest,
   covering a monorepo's packages automatically.
4. The scan **does not descend into installed-dependency directories**. The set
   of directories to skip is **declared by the generator implementations
   themselves** (e.g. `composer_json` declares `vendor/`, `package_json`
   declares `node_modules/`), so adding a new ecosystem generator in the future
   automatically extends the skip-set with no separate change.
5. The config-input hash continues to watch **all** referenced manifests (it
   already watches "the manifests referenced by enabled generators"), so editing
   any package's manifest triggers recompilation — this follows for free once a
   generator can name an arbitrary path.

## User Stories / Use Cases

- As a developer working in a monorepo, I want `init` to detect every package's
  manifest so that the cage's allowlist covers all my packages' dependencies
  without me hand-listing each one.
- As a developer, I want to add a second package manually
  (`package_json: apps/api/package.json`) so that a package `init` didn't catch
  (or one added later) still gets its dependency allow-rules.
- As an existing single-repo user, I want my current config
  (`generators: [package_json, composer_json]`) to keep working untouched so
  that this change costs me nothing.
- As a developer whose repo vendors dependencies in `vendor/` or
  `node_modules/`, I want the scan to ignore manifests inside those so that I
  don't get a generator entry per installed dependency.

## Acceptance Criteria

- [ ] A config may list the same generator type more than once, each scoped to a
      different manifest path, and the compiled policy contains the dependency
      allow-rules for **every** listed manifest.
- [ ] The bare-string form (`package_json`, `composer_json`) still parses, loads,
      merges, and compiles, and resolves to the root manifest (`./package.json`,
      `./composer.json`) — verified against an existing-style config unchanged.
- [ ] `init` on a monorepo (manifests at root and/or under sub-dirs ≤2 levels
      deep) writes one generator entry per detected manifest.
- [ ] `init` does **not** emit a generator entry for any manifest located inside
      a directory declared as a dependency dir by some generator — specifically,
      a `composer.json` inside `vendor/<anything>/` and a `package.json` inside
      `node_modules/<anything>/` are ignored (the stated trap).
- [ ] The dependency-dir skip-set is sourced from the generator implementations,
      not a separate hardcoded list in the scanner — adding a new generator with
      its own dependency dir extends the skip-set with no scanner edit.
- [ ] The scan is bounded to ≤2 directory levels deep (it does not walk the
      entire tree).
- [ ] `init`'s no-clobber behavior (AC-0029) is preserved: an existing
      `.agent-creance.yaml` is not overwritten without `--force`.
- [ ] `policy show` (AC-0015) still attributes each generated rule to its source,
      and the attribution disambiguates which manifest produced a rule when two
      generators of the same type are present.
- [ ] `make test`, `make lint`, and `make golden` (reviewed) are green.

## Out of Scope

- New ecosystem generators (`pyproject_toml`, `cargo_toml`, `go_mod`, etc.) —
  this ticket only generalizes the existing two and the framework around them.
  (Those remain the v0.2 roadmap item.)
- A `run`-time or background re-scan that auto-edits committed config when new
  packages appear — detection stays a scaffold-time (`init`) concern. Picking up
  packages added later is done by re-running `init --force` or hand-editing.
- A dedicated "rescan/merge into existing config" command.
- Workspace-declaration parsing (npm `workspaces`, pnpm-workspace.yaml, composer
  path repositories) as the detection mechanism — detection is manifest-presence
  + dependency-dir exclusion, not workspace-aware.
- Per-generator options beyond the manifest path (transitive-deps mode, custom
  scoping) — still the v0.4 roadmap item.
- Lockfile-based generation — unchanged; generators still read the manifest.

## Open Questions

_None — business/scope decisions resolved during ticket creation (see Notes)._

## Questions for Research/Planning

- [ ] **Schema shape for the parameterized form.** What YAML expresses
      "generator type + manifest path" while keeping the bare string valid?
      (e.g. `- package_json: apps/web/package.json` map form alongside the bare
      `- package_json` string; or `{type:, path:}` objects.) Affects the
      `Egress.Generators` type (currently `[]string` in `internal/config/config.go`),
      the strict decoder (`rawConfig`), `merge.go` union/dedupe semantics, and
      `edit.go` (the programmatic config editor + golden files under
      `internal/config/testdata/edit/`).
- [ ] **Merge/dedupe identity.** With paths, the dedupe key becomes
      (type, path) rather than type alone — confirm union semantics across
      global + project + includes match this.
- [ ] **Generator registry surface.** How should a generator declare (a) the
      manifest filename it recognizes and (b) its dependency-dir name(s), so the
      scanner can consume both? Where does this live relative to the current
      generator implementations (`internal/`… registry)?
- [ ] **Scan implementation.** Bounded recursive walk through the `sysdep` FS
      seam (no direct `os` calls); depth semantics (is root level 0, so levels
      0–2 inclusive?); ordering/determinism of emitted entries for stable golden
      output.
- [ ] **Root vs sub emission style.** When the root manifest is detected
      alongside sub-package manifests, does `init` emit the root as the bare
      form and sub-packages as the parameterized form, or all parameterized for
      consistency?
- [ ] **Policy attribution disambiguation.** The current source label is
      `generated:<type>:<package>` (e.g. `generated:package_json:react`). With
      two `package_json` generators that could both depend on the same package,
      how is the manifest path woven into the annotation (`policy show` /
      `policy explain`, AC-0015)?
- [ ] **Compiler fan-out.** How does the policy compiler (AC-0013) run N
      instances of the same generator keyed by path, and how does the
      generator-output cache (keyed on manifest hash) key per-manifest?
- [ ] **Symlink / cycle safety** during the bounded scan.

## References

- `docs/design.md` — "Allowlist generators" (current two-generator model, the
  "No options in v0.1" and "Future, post-v0.1" notes), "Config compilation"
  (input-hash watches referenced manifests).
- AC-0029 (`init` command) — current root-only manifest detection.
- AC-0012 (allowlist generators), AC-0013 (policy compiler), AC-0015
  (`policy show`/`explain`), AC-0007 (config schema/loader), AC-0008
  (include/merge).
- `internal/config/config.go` (`Egress.Generators`), `internal/config/merge.go`,
  `internal/config/edit.go`, `internal/cli/init.go`.

## Implementation Plan

_To be filled by `/create_plan`._

## Notes & Updates

### 2026-06-09

Created from a maintainer request: monorepos were missed in the v0.1 generator
design, and the maintainer primarily works in monorepos.

Decisions made during ticket creation:

- **Scan trigger: `init` only.** Detection is a scaffold-time concern; no `run`-
  time or background config mutation. Re-run `init --force` or hand-edit to pick
  up later additions.
- **Dependency-dir exclusion: declared by the generators, not the scanner.** Each
  generator implementation owns its dependency-dir name(s) (`composer_json` →
  `vendor/`, `package_json` → `node_modules/`); the scanner skips the union. New
  generators extend the skip-set automatically. This directly addresses the
  stated trap (a `composer.json` under `vendor/` is an installed dep, not a
  monorepo package).
- **Backward compatibility: required.** The bare-string generator form keeps
  meaning the root manifest; the manifest-path parameter is additive/optional.
  No forced migration of existing project or global configs.

Complexity rationale (Large): touches the config schema + strict decoder, merge
dedupe semantics, the programmatic config editor and its golden files, the
generator registry (new declared metadata), the `init` scan, the policy compiler
fan-out + output cache keying, and policy-show attribution — a coordinated change
across many of the generator-framework seams rather than a single localized edit.
