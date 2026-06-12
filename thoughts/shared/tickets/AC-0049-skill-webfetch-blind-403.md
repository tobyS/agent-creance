# AC-0049: Skill recognizes body-blind WebFetch 403s as cage refusals

**Status:** In Progress
**Estimated Complexity:** Small
**Created:** 2026-06-12
**Updated:** 2026-06-12

## Problem Statement

The shipped `agent-creance` skill triggers on the refusal markers the enforcer
puts on the wire: the `X-Cage-Reason` header and the `agent_cage_` JSON error
prefix. But Claude Code's WebFetch — the tool caged agents use most for web
research — discards the body and headers of non-2xx responses entirely and
reports only "The server returned HTTP 403 Forbidden. The response body was not
retrieved." (proven by experiment 2026-06-12 against a byte-identical soft-deny
mock). The skill's triggers never fire, the agent misreads the bare 403 as
site-side blocking or an auth problem, and hunts mirrors — observed live as an
8+ minute research stall in a caged session.

## Desired Outcome

A caged agent that sees a bare 403 from WebFetch (no body or headers available)
recognizes it as a probable cage refusal: the skill's frontmatter description
triggers on that situation, and the skill body explains that WebFetch hides the
structured refusal and tells the agent the right reaction — don't assume the
site blocks fetches or needs auth, don't hunt mirrors; curl the URL (proxy env
and mitmproxy CA are already set inside the cage) to see the structured JSON
refusal, then follow the existing soft-deny/hard-deny guidance.

## User Stories / Use Cases

- As a caged agent whose WebFetch fails with a bare 403, I want the skill to
  tell me this is likely cage policy and how to see the real reason, so that I
  react per the soft-deny/hard-deny contract instead of thrashing on mirrors.
- As a user running a caged research agent, I want denied fetches surfaced as
  allowlist questions within one tool call, so research tasks don't silently
  stall.

## Acceptance Criteria

- [ ] The skill frontmatter description includes a trigger for a WebFetch/fetch
      tool failing with a bare 403 (no response body or headers visible) while
      working inside an agent-creance cage.
- [ ] The skill body has a section explaining that WebFetch hides the structured
      refusal, with the reaction: don't conclude the site blocks fetches or
      requires auth; don't try mirrors; curl the URL inside the cage to see the
      JSON refusal, then apply the existing soft-deny/hard-deny guidance.
- [ ] Status-code references are written so AC-0047 (470 soft / 471 hard) only
      needs to swap the numbers later.
- [ ] The integration test asserting frontmatter triggers (AC-0045) covers the
      new trigger.
- [ ] Existing tests continue to pass (`make test`, `make lint`), and
      `make build` is run at the end so `bin/agent-creance` embeds the new skill.

## Out of Scope

- Distinct refusal status codes 470/471 and the launch-time cage briefing
  (AC-0047).
- Baseline allowlist additions (AC-0048).
- Any enforcer/wire-contract change — this is a skill-text-only fix.

## Open Questions

None — this is a well-understood quickfix.

## Questions for Research/Planning

- [ ] Exact current structure of `internal/setup/SKILL.md` (sections, wording
      style) and where the new section fits.
- [ ] Which test asserts the frontmatter triggers (AC-0045 integration test) and
      what exactly it matches on; any other tests pinning SKILL.md content.
- [ ] Does docs/design.md quote the skill's trigger description anywhere that
      must stay in sync?
- [ ] Does the installed-skill freshness check (`setupcheck`) compare content
      hashes such that `setup` must be re-run after this change?

## References

- Quickfix initiated via `/quickfix` command
- AC-0047 (`thoughts/shared/tickets/AC-0047-webfetch-visible-refusals.md`) —
  companion ticket carrying the experimental evidence (WebFetch status-line-only
  visibility; webhook.site mocks; WebFetch UA).
- AC-0045 — added the frontmatter-trigger integration test and the
  auth-failure skill section (structural precedent for this change).

## Implementation Plan

[Leave empty — will be filled when plan is created]

## Notes & Updates

### 2026-06-12
- Quickfix ticket auto-created from `/quickfix` command, born from the live
  debugging session that also produced AC-0047/AC-0048.
