# AC-0048: Add Claude documentation hosts to the global egress baseline

**Status:** Done
**Estimated Complexity:** Small
**Created:** 2026-06-12
**Updated:** 2026-06-14

## Problem Statement

The scaffolded global baseline (AC-0043) covers the hosts Claude Code needs to
*operate* (api.anthropic.com, claude.ai, platform.claude.com, downloads.claude.ai,
raw.githubusercontent.com), but not the hosts a caged agent needs to *read
Anthropic's own documentation*. Observed live (toby-plugins cage, 2026-06-12): a
research subagent's fetches of `code.claude.com/docs/...` were soft-denied, which
— combined with the refusal-visibility problem (AC-0047) — sent it mirror-hunting
for 8+ minutes. Claude agents reach for these docs constantly (tool references,
Agent SDK, API docs); soft-denying them produces recurring thrash for zero
security benefit, since they are public, credential-free documentation pages
owned by Anthropic.

Deliberately NOT in scope: GitHub hosts. Filtering GitHub per project is a core
purpose of the cage — github.com/api.github.com must stay project-allowlisted,
never baseline-allowed.

## Desired Outcome

A fresh `agent-creance setup` produces a baseline in which a caged Claude agent
can fetch Anthropic's documentation (the claude.com docs properties, e.g.
`code.claude.com`; exact host set determined in research from the official docs
properties) out of the box. Users with an existing global config (which setup
never modifies, per AC-0043) have a documented, low-friction way to adopt the
addition.

## User Stories / Use Cases

- As a user running a caged research/coding agent, I want it to read Anthropic's
  own docs without allowlist friction so that routine "how does Claude Code X
  work" lookups don't degenerate into denied-fetch thrash.
- As an existing agent-creance user whose global config predates this change, I
  want to learn about and adopt the new baseline hosts easily so that I don't
  have to reverse-engineer them from soft-deny logs.
- As a security-conscious user, I want the addition limited to credential-free
  Anthropic documentation hosts so that the baseline stays minimal and auditable.

## Acceptance Criteria

- [ ] The scaffolded global baseline includes the Anthropic documentation hosts
      (at minimum `code.claude.com`) in `intercept` mode, scoped as tightly as
      the docs properties allow (GET-only and/or docs paths if practical).
- [ ] A fresh-setup cage can fetch `https://code.claude.com/docs/...` (verified
      by an intercept allow decision in the audit log / golden policy tests).
- [ ] No GitHub host is added to the baseline; existing project-level GitHub
      filtering is untouched.
- [ ] Existing global configs are still never modified by setup (AC-0043
      invariant); the addition is documented for manual adoption (e.g. README /
      design.md baseline section showing the snippet to paste).
- [ ] Golden files / tests covering the scaffolded baseline are updated and
      `make test` stays green.

## Out of Scope

- GitHub hosts in any form (explicit product decision — project-filtered only).
- Telemetry hosts (Statsig/Datadog/Sentry stay commented out per AC-0043; the
  audit-log noise from Datadog's ~15s soft-deny retries is a separate concern,
  noted 2026-06-12, no decision yet).
- Auto-merging baseline updates into existing global configs, and `doctor`
  staleness checks for the baseline (both already excluded by AC-0043).
- Broader "research preset" allowlists (general docs sites, package registries
  beyond what generators produce).
- Refusal visibility itself (AC-0047).

## Open Questions

None.

## Questions for Research/Planning

- [ ] Exact current set of Anthropic docs properties and their hosts
      (code.claude.com; does platform.claude.com already cover the API docs via
      its passthrough entry? docs.anthropic.com legacy redirects? anthropic.com
      engineering posts?) — pick from the official docs, keep minimal.
- [ ] Can the entries be path-/method-scoped (e.g. GET `/docs/**`) without
      breaking docs assets, or is host-level intercept the practical choice?
- [ ] Where do the baseline template + its tests live after AC-0043
      (`internal/cli/setup.go` scaffold step), and which golden files change?
- [ ] Where should the "existing configs: add this snippet" documentation live
      (README vs design.md baseline section)?

## References

- AC-0043 (Done) — scaffolded global baseline; defines the "never modify existing
  config" invariant and the current host list.
- Debugging session 2026-06-12 — audit log `~/.cache/agent-creance/projects/df53512b3221d9e6/egress.jsonl`:
  `code.claude.com/docs/...` soft-denied; platform.claude.com and
  raw.githubusercontent.com allowed by the existing baseline.
- AC-0047 — refusal visibility for WebFetch (the companion fix; this ticket
  shrinks how often refusals happen, AC-0047 makes the remaining ones legible).

## Implementation Plan

- Research: `thoughts/shared/research/2026-06-14-AC-0048-claude-docs-baseline-hosts.md`
- Plan: `thoughts/shared/plans/2026-06-14-AC-0048-claude-docs-baseline-hosts.md`

## Notes & Updates

### 2026-06-12

- Scope decision by Toby: "yes about the claude.com things, no about GitHub" —
  GitHub project-filtering is a main purpose of the cage.
- Sized Small: baseline template edit + tests + adoption note; host research is
  the only open work.

### 2026-06-14 (implementation)

- Added three Anthropic docs hosts to `globalConfigTemplate` (`internal/cli/setup.go`):
  `code.claude.com` (intercept, GET, path-scoped to `/docs/`) plus legacy
  redirectors `docs.anthropic.com` / `docs.claude.com` (intercept, host-wide GET).
  All intercept so GET/path scoping is enforceable and audited.
- Checkpoint decisions (Toby): include the two legacy redirector hosts (not just
  code.claude.com) so older doc links resolve; put the adoption snippet in README.
- Research-resolved (no checkpoint): `platform.claude.com` already covers the API
  docs via its passthrough entry (unchanged); Mintlify CDN asset hosts excluded
  (agent reads docs via GET, not browser rendering — keeps the baseline
  Anthropic-only/minimal); no GitHub host added.
- `TestSetupScaffoldsGlobalConfig` asserts the new rules' mode/paths/methods.
  `make test`, `make lint`, `make build` green. End-to-end allow (live cage run)
  is the optional manual confirmation.
