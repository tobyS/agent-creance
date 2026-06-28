# AC-0065: user-facing onboarding — fix stale README, add quickstart and an install path

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-27
**Updated:** 2026-06-28

## Problem Statement

The 2026-06-25 UX audit
(`thoughts/shared/research/2026-06-25-ux-audit.md`, findings S2 and S3) found that
first contact with the project is broken for a new user in two compounding ways.

**S2 — the README is stale and self-contradicting.** The status banner claims
*"What's implemented today is the project skeleton plus the `version` and
`doctor` … commands"* (`README.md:9-12`), while the **same README** then documents
`setup` (README.md:21-48), `init` imports (README.md:50-79), and `import`
(README.md:74-79) in detail — and all of `setup`/`init`/`run`/`allow`/`deny`/
`import`/`policy` are implemented and tested in `internal/cli/`. The
`## Requirements` block compounds it: *"For actually running a cage (not yet wired
up)…"* (`README.md:18`). A shell engineer reads the banner first and concludes
the tool isn't usable, then bounces.

**S3 — there is no quickstart and no documented install path.** The
clone/install → `setup` → `init` → `run` happy path is never written end-to-end
for a user in one place; it exists only as prose in the *design* doc
(`docs/design.md:406-475`) and partial in-CLI "Next:" hints
(`internal/cli/init.go:140,142`). The README has no Getting Started / Quickstart /
Install heading, and there is no documented way to get the binary onto `PATH` —
no Homebrew formula (no `*.rb` in the repo), no `go install` line, no release
pointer; only `make build` → `./bin/agent-creance` (`README.md:82-90`). For a tool
whose whole pitch is "a single command," there is no documented way to obtain that
single command.

## Desired Outcome

A new user landing on the README can, in one place, (1) trust that the
implemented surface is described accurately, (2) follow a Quickstart that takes
them from obtaining the tool to a running caged session, and (3) install the
`agent-creance` binary onto their `PATH` by at least one documented method.

## User Stories / Use Cases

- As an engineer evaluating the tool, I want the README's status/requirements to
  accurately state what works, so that I don't dismiss a working tool as
  unfinished.
- As a new user, I want a Quickstart that lists the exact commands in order
  (obtain → setup → init → run), so that I can get a caged session running without
  reading the design doc.
- As a new user, I want at least one documented way to install `agent-creance`
  onto my `PATH`, so that I can invoke it as "a single command" rather than via
  `./bin/agent-creance`.

## Acceptance Criteria

- [ ] The README status banner no longer claims only `version`/`doctor` are
      implemented; it accurately reflects the implemented command surface (or
      drops the per-command claim entirely).
- [ ] The "not yet wired up" qualifier in `## Requirements` (README.md:18) is
      removed or corrected to match reality.
- [ ] The README contains a Quickstart / Getting Started section listing the
      ordered happy-path commands: obtain/install → `agent-creance setup` →
      `agent-creance init` → `agent-creance run`, including the external
      prerequisites (`agent-safehouse`, `mitmproxy`).
- [ ] At least one install method that puts `agent-creance` on `PATH` is
      documented and works (e.g. `go install …`, or a `make install` target to a
      standard bin dir).
- [ ] No statement in the README is contradicted by the code or by another part
      of the README.

## Out of Scope

- A Homebrew tap / formula — that is a v1.0 roadmap item
  (`docs/design.md:575`); this ticket only requires *one* working install method,
  not the tap.
- In-CLI `--help` content (root `Long:`, per-command examples, grouping) — tracked
  separately in AC-0064.
- Signed releases / release automation (v1.0 roadmap).
- Rewriting `docs/design.md` (it stays the deep reference; the Quickstart distills
  from it).

## Open Questions

None — well-understood from the audit.

## Questions for Research/Planning

- [ ] Which install method should be the documented default — `go install
      github.com/tobyS/agent-creance/cmd/agent-creance@latest`, a `make install`
      target, or both? (Note: `go install` would not stamp the ldflags version
      metadata that `make build` injects — confirm whether that matters for the
      documented path.)
- [ ] Should the Quickstart live entirely in the README, or in the README with a
      pointer to a fuller `docs/` guide?
- [ ] Are there other stale claims in the README beyond the two identified (worth
      a quick pass for accuracy while editing)?

## References

- `thoughts/shared/research/2026-06-25-ux-audit.md` — UX audit, findings S2 and
  S3.
- `README.md:9-19` — stale status banner and "not yet wired up" requirements.
- `README.md:82-90` — current build-only instructions (`make build`).
- `docs/design.md:406-475` — the command reference the Quickstart distills.
- `docs/design.md:540,575` — "single-step Homebrew install" intent and the v1.0
  Homebrew-tap roadmap item.

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-06-27
Created from UX audit findings S2 and S3, combined per request because both are
README/onboarding first-contact problems. Complexity Medium: primarily docs, but
"a working install path" may require a small `make install` target and a decision
on `go install` vs. ldflags version stamping. Homebrew tap explicitly deferred to
the v1.0 roadmap.
