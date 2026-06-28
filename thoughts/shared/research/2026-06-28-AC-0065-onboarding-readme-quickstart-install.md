---
date: 2026-06-28T11:50:50+02:00
git_commit: 2a84a007f2bc96a38675271da30a4f92710dce5f
branch: main
repository: agent-creance
topic: "AC-0065 — fix stale README, add quickstart and an install path"
tags: [research, ux, readme, onboarding, install, docs]
status: complete
last_updated: 2026-06-28
ticket: AC-0065
---

# Research: AC-0065 — user-facing onboarding (stale README, quickstart, install path)

**Date**: 2026-06-28T11:50:50+02:00
**Git Commit**: 2a84a007f2bc96a38675271da30a4f92710dce5f
**Branch**: main
**Repository**: agent-creance

## Research Question

AC-0065 combines findings S2 and S3 of the 2026-06-25 UX audit. First contact
with the project is broken for a new user: (S2) the README status banner is stale
and self-contradicting, and (S3) there is no quickstart and no documented install
path. Research the current README, the *actual* implemented command surface, the
design doc's command reference (the quickstart's source material), and the
install-method options (`go install` vs a `make install` target, and the version
stamping question the ticket raises).

## Summary

The ticket is accurate and well-scoped. Concretely:

1. **The implemented surface is far larger than the banner claims.** 16 top-level
   commands are registered and fully implemented (no stubs), including `run`
   (which really constructs and launches the cage), `setup`, `init`, `import`,
   `policy`, `allow`/`deny`, plus more the README never mentions
   (`status`, `logs`, `clean`, `domain`/`service`/`mount`/`include`, `doctor`,
   `version`). The banner's claim "only `version` and `doctor` are implemented"
   (`README.md:9-12`) and the "(not yet wired up)" qualifier (`README.md:18`) are
   both flatly false.

2. **There is no Quickstart/Install heading in the README at all.** The
   obtain → `setup` → `init` → `run` happy path exists only as prose in
   `docs/design.md:406-475` and partial in-CLI "Next:" hints. The README jumps
   from feature descriptions straight to a `## Development` section
   (`README.md:104-113`) whose only build instruction is `make build` →
   `./bin/agent-creance` — never an install onto `PATH`.

3. **Both install methods work; the tradeoff is version stamping and clone vs
   no-clone.** The repo is public (`go install …@latest` resolves via the Go
   proxy), so a from-source clone is not required. The only real difference is
   that `go install` produces a binary whose `version` reports the `dev`
   fallback, because the version metadata is injected only by the Makefile's
   `-ldflags` — it does not affect any runtime behavior.

No config drift found; the profile and ticket-system config still match reality.

## Detailed Findings

### The actual implemented command surface (corrects S2)

The cobra tree is assembled in `internal/cli/cli.go:88-136` (`newRootCmd`).
**16 top-level commands** are registered at `cli.go:119-134`, every one fully
implemented (a subagent confirmed no stubs, no `Hidden`, no build-tag gating):

| Command | Defined | Status |
|---|---|---|
| `init` | `init.go:28` | scaffolds `.agent-creance.yaml`, host-setup gate, git-remote allowlisting, interactive imports |
| `version` | `version.go:11` | prints build + tested-tool versions |
| `doctor` | `doctor.go:21` | full diagnostics (`--fix`, `--json`) |
| `policy` | `policy.go:18` | parent: `show` / `explain` / `refresh` |
| `logs` | `logs.go:18` | audit log dump / `--summary` / `--follow` |
| `run` | `run.go:50` | **really launches the cage** (see below) |
| `setup` | `setup.go:25` | CA trust + skill install + global-config scaffold |
| `allow` | `allow.go:16` | soft-allow rule + recompile (alias of `domain add`) |
| `deny` | `deny.go:14` | deny_always rule + recompile |
| `domain` | `domain.go:36` | parent: `add` / `remove` |
| `service` | `service.go:21` | parent: `add` / `remove` (host services) |
| `mount` | `mount.go:19` | parent: `add` / `remove` (safehouse add_dirs) |
| `include` | `include.go:19` | add include entry + recompile |
| `import` | `import.go:28` | merge YAML fragment after review |
| `status` | `status.go:17` | list running cages across projects (`--json`) |
| `clean` | `clean.go:17` | tear down proxy/lock/overlay |

Plus cobra's auto-generated `completion` command (reachable, documented in the
README's completion section already).

**`run` is not a placeholder.** `runRun` (`run.go:68-287`) does the real
orchestration: prereq check, setup precondition, credential precondition,
project-config gate, policy compile (`internal/policy/compile`), sandbox profile
compile, enforcer extraction + proxy start (`internal/proxy`), cage construction
(`internal/cage` — `Resolve/Prepare/Build`), config hot-reload watcher, and the
launch via `cage.NewRunner(app.ProcessGroup).Run(ctx, inv)` (`run.go:283`).

**Conclusion for S2:** the banner should stop enumerating per-command status.
The accurate framing is "v0.1 core is implemented — `setup`/`init`/`run` and the
config/policy commands all work" rather than a list that will keep going stale.

### Current README structure (what changes)

- `README.md:9-12` — the stale status banner ("project skeleton plus `version`
  and `doctor`"). **Rewrite.**
- `README.md:14-19` — `## Requirements`; line 18 carries "(not yet wired up)".
  **Remove the qualifier.**
- `README.md:21-79` — `## Egress baseline` and `## First-run config (init
  imports)`: accurate, detailed, but they describe *individual features* before
  the reader has any obtain→run narrative. A Quickstart should precede them.
- `README.md:82-102` — `## Shell completion`: accurate and recent (AC-0066). Keep.
- `README.md:104-131` — `## Development`: `make build` → `./bin/agent-creance`,
  test/lint/hooks. Accurate but is the *only* "how to get the binary" content,
  and it is a contributor section, not a user install path.

There is **no** Getting Started / Quickstart / Install heading anywhere
(confirmed by reading the full file). The natural insertion point is a
`## Quickstart` + `## Install` pair immediately after `## Requirements` and
before `## Egress baseline`.

### The happy-path command reference (quickstart source material)

`docs/design.md:406-475` is the de-facto command reference the Quickstart
distills. The canonical ordered path:

1. `agent-creance setup` — one-time per machine: trust mitmproxy CA, install
   skill, scaffold `~/.config/agent-creance.yaml` baseline (design.md:409-420).
2. `agent-creance init` — one-time per project: write `.agent-creance.yaml`,
   detect manifests, allowlist the project's own git remotes, optional imports
   (design.md:422-446).
3. `agent-creance run` — start the cage + agent; refuses with a pointer to
   `setup` if setup hasn't run (design.md:447-450).

External prerequisites the Quickstart must name: `agent-safehouse` and
`mitmproxy` on `PATH` (plus Go 1.26+ to build/install). `setup`/`run` already
refuse-and-suggest when these are missing (`internal/prereq/prereq.go:157-168`).

The `setup` vs `init` distinction (host-once vs project-once) is exactly what S5
of the audit flags as unoriented — the Quickstart's ordered list with one-line
"once per machine / once per project" annotations resolves it for free, within
this ticket's scope.

### Install method analysis (resolves "Questions for Research/Planning" Q1)

Repo facts (verified): module path `github.com/tobyS/agent-creance`
(`go.mod:1`), `go 1.26.3` (`go.mod:3`); remote
`git@github.com:tobyS/agent-creance.git`, **visibility PUBLIC**, **no tags, no
GitHub releases**. `go env GOPATH` = `/Users/toby/go` (so `go install` lands in
`/Users/toby/go/bin`); `GOBIN` unset.

**Version stamping mechanics.** `internal/buildinfo/buildinfo.go:21-28` declares
`Version`/`Commit`/`Date` as package vars defaulting to `"dev"` / `"none"` /
`"unknown"`. They are overridden **only** via the Makefile's `-ldflags`
(`Makefile:8-18,41`), where `VERSION = git describe --tags --always --dirty`.
With no tags, `--always` falls back to the short SHA. `agent-creance version`
(`version.go:18-20`) is the only consumer of these vars. Crucially, **nothing in
`doctor`/`run` behavior depends on `Version`** — the version-skew logic uses
`buildinfo.TestedVersions` (constants, `buildinfo.go:50-53`), independent of the
build-stamped `Version`. So an unstamped binary is fully functional; only the
`version` line is cosmetic.

| Method | Works for outside user? | Clone? | Version line | Lands in |
|---|---|---|---|---|
| `go install github.com/tobyS/agent-creance/cmd/agent-creance@latest` | Yes (public repo) | No | `dev (commit none, built unknown)` | `$(go env GOPATH)/bin` |
| `make install` (new target = `go install -ldflags …`) | Yes (after clone) | Yes | `<short-sha> (commit <sha>, built <date>)` | `$(go env GOPATH)/bin` |
| `make build` (exists) | Yes (after clone) | Yes | stamped | `./bin/` (NOT on PATH) |

Key realization: a `make install` target implemented as
`go install -ldflags "$(LDFLAGS)" ./cmd/agent-creance` both **stamps the version**
*and* **lands in the same `$(go env GOPATH)/bin`** as the bare `go install
@latest`. So the two methods are complementary, not competing — same destination,
differing only by clone-vs-no-clone and stamped-vs-`dev`. Both require
`$(go env GOPATH)/bin` to be on the user's `PATH`, which the README must state.

`go install …@latest` with no tags resolves `@latest` to a pseudo-version of the
latest default-branch commit — it works without a release being cut.

(Out of scope but noted: the `dev` version under `go install` could be eliminated
by having `buildinfo` fall back to `runtime/debug.ReadBuildInfo().Main.Version`
when ldflags aren't applied. That is a code change beyond this docs ticket and is
*not* required to satisfy the acceptance criteria.)

### Accuracy pass — other stale claims? (resolves Q3)

Read the full README for contradictions beyond S2's two:

- "Go 1.26+" (`README.md:16`) — **accurate** (`go.mod` = `go 1.26.3`).
- `## Egress baseline` (21-48), `## First-run config` (50-79) — **accurate**
  against `setup`/`init` behavior.
- `## Shell completion` (82-102) — **accurate** (AC-0066, recent).
- `## Development` (104-131) — **accurate**; only gap is that it is the sole
  "obtain the binary" content and is contributor-oriented (addressed by adding a
  user-facing Install section, not by changing Development).

**Finding:** the only stale claims are the two the ticket already names (banner
per-command list; "not yet wired up"). No additional contradictions found. A
light accuracy pass is folded into the edit, but no extra corrections are needed.

## Code References

- `README.md:9-12` — stale status banner (rewrite).
- `README.md:18` — "(not yet wired up)" qualifier (remove).
- `README.md:104-113` — `## Development`, the only build instruction (`make build`).
- `internal/cli/cli.go:119-134` — the 16 registered, implemented commands.
- `internal/cli/run.go:68-287` — `run` really launches the cage.
- `docs/design.md:406-475` — command reference the Quickstart distills.
- `docs/design.md:575` — Homebrew tap is a v1.0 roadmap item (out of scope here).
- `Makefile:8-18,41` — `-ldflags` version stamping; `VERSION = git describe`.
- `internal/buildinfo/buildinfo.go:21-28,50-53` — `Version` var (ldflags-only)
  vs `TestedVersions` constants (behavior-relevant).
- `internal/cli/version.go:18-20` — sole consumer of build-stamped `Version`.

## Architecture / Documentation Notes

The README is the only user-facing doc surface in scope; `docs/design.md` is the
deep reference (explicitly out of scope to rewrite — the Quickstart distills from
it). No code-behavior change is required to satisfy the acceptance criteria,
except the optional small `make install` target (a Makefile addition, not Go
code). This keeps the ticket primarily a docs change with one ~3-line Makefile
target — matching its Medium estimate.

## Open Questions (for the checkpoint)

1. **Install method to document as the default.** Research recommends
   documenting **both**, since a `make install` target implemented as a stamped
   `go install` lands in the same place as the bare `go install …@latest` — they
   complement rather than compete. `go install …@latest` is the zero-clone
   "single command" path (caveat: `version` shows `dev`); `make install` is the
   stamped from-source path. Alternative: pick only one.

2. **Quickstart location.** Research recommends a **self-contained Quickstart in
   the README** with a pointer to `docs/design.md` for depth (the audit framed S3
   as a README gap, and design.md is out of scope to rewrite). Alternative: a
   thinner README quickstart that points to a new fuller `docs/` guide.

(Q3 — "other stale claims" — is resolved above: none beyond the two named.)

## Related Research

- `thoughts/shared/research/2026-06-25-ux-audit.md` — the parent UX audit
  (findings S2, S3; also S5 setup/init orientation, which the Quickstart's
  ordered list incidentally addresses within scope).
