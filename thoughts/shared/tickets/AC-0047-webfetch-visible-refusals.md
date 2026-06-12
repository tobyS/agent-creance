# AC-0047: Make egress refusals visible to body-blind HTTP clients (WebFetch)

**Status:** In Progress
**Estimated Complexity:** Medium
**Created:** 2026-06-12
**Updated:** 2026-06-12

## Problem Statement

The cage's refusal contract (HTTP 403 + `X-Cage-Reason` header + structured JSON
body with `agent_cage_*` error, `how_to_proceed`, `allow_command_suggestion`) is
invisible to Claude Code's WebFetch tool — the tool caged agents use most for
web research. Live experiments (2026-06-12, against a byte-identical soft-deny
mock over HTTPS) proved WebFetch surfaces **only the status line** to the model
and discards body and headers entirely:

> The server returned HTTP 403 Forbidden. The response body was not retrieved.
> If this URL requires authentication, use an authenticated tool…

The consequences, observed in a real caged session (toby-plugins, 2026-06-12):

- The agent misattributes the bare 403 to site-side blocking or auth ("code.claude.com
  blocks direct fetches") — WebFetch's own error text actively suggests that wrong theory.
- It then hunts mirrors and alternate URLs for the same content, which are also
  soft-denied, burning 8+ minutes per research task while looking "hung" to the user.
- The shipped `agent-creance` skill never triggers, because its trigger markers
  (`X-Cage-Reason`, `agent_cage_` prefix) never reach the model via WebFetch.

The proxy itself was verified healthy throughout (soft-deny answered in 8 ms,
allowed fetches delivered normally) — this is purely a refusal-*visibility* problem.

## Desired Outcome

A caged Claude Code agent recognizes an egress refusal as cage policy — not as a
broken site or auth problem — on the **first** failed WebFetch, and reacts per the
skill (ignore / ask the user to allowlist / never retry hard-denies), regardless of
whether the refusal arrived via WebFetch, curl, git, or a package manager.

Two complementary mechanisms:

1. **Launch-time cage briefing.** `agent-creance run` passes a short
   `--append-system-prompt` to the caged Claude Code stating: you are inside an
   egress-filtered cage; WebFetch failures with status 470/471 (and 4xx generally)
   are cage policy denials whose reason WebFetch hides; to see the structured
   refusal, curl the URL (proxy env + CA are already set inside the cage) or
   consult the `agent-creance` skill; do not hunt mirrors for denied content.

2. **Distinct refusal status codes.** Refusals use custom, unmistakable HTTP
   status codes instead of 403 for intercepted requests: **470 = soft-deny,
   471 = hard-deny**. The status code is the only signal WebFetch always
   surfaces verbatim (verified: a mocked custom code is reported as e.g.
   "HTTP 460 Unknown Status"), so it becomes the universal refusal marker.
   Everything else about the wire contract (X-Cage-Reason header, JSON body
   shape) stays unchanged.

## User Stories / Use Cases

- As a user running a caged agent, I want the agent to recognize a denied fetch
  immediately and tell me which host it wants allowlisted, so that research tasks
  don't silently burn many minutes thrashing against the allowlist.
- As a caged agent (including subagents without the Skill tool), I want every
  refusal — through any HTTP client — to carry a marker I can actually see, so
  that I can follow the soft-deny/hard-deny guidance instead of misdiagnosing
  the failure as a site or auth problem.
- As an operator reading `agent-creance logs`, I want refusal entries to remain
  clearly distinguishable (soft vs hard), consistent with the new status codes.

## Acceptance Criteria

- [ ] `agent-creance run` launches the caged Claude Code with an appended system
      prompt containing the cage briefing (presence verifiable in a testscript via
      the stubbed agent binary's recorded argv).
- [ ] The briefing names the refusal status codes (470/471), states that WebFetch
      hides refusal bodies, and instructs the agent to curl for the structured
      reason or consult the `agent-creance` skill instead of trying mirrors.
- [ ] Intercepted soft-denied requests return HTTP 470; hard-denied requests
      (including passthrough hosts refused at CONNECT) return HTTP 471; the
      `X-Cage-Reason` header and JSON body are byte-identical to today's contract
      apart from the status line.
- [ ] No refusal path returns 403 anymore (golden files, enforcer tests, and the
      audit log's `status` field reflect 470/471).
- [ ] docs/design.md's "Network refusal handling" section documents the new codes
      and the rationale (WebFetch surfaces only the status code; 470/471 chosen to
      avoid AWS ALB's 460/463/464).
- [ ] Manual verification in a live cage: a WebFetch of a non-allowlisted URL makes
      the agent report the cage denial (not "site blocks fetches" / auth theories)
      without mirror-hunting.

## Out of Scope

- **Skill description/trigger update** (trigger on "HTTP 470/471" etc.) — handled
  as an immediate separate quickfix, but must land in the same release as the
  status-code change since the old skill keys on 403.
- **UA-scoped refusal rendering** (serving WebFetch-specific responses keyed on the
  `Claude-User (claude-code/…)` User-Agent, e.g. redirect-based refusals carrying
  the marker in the Location URL) — proven viable in experiments, deliberately
  deferred until 470/471 + briefing prove insufficient.
- **Baseline allowlist for claude.com docs hosts** — separate ticket.
- **Upstream Claude Code fix** (WebFetch discarding 4xx bodies) — worth filing on
  anthropics/claude-code, but outside this repo's control.
- Non-Claude agents (v0.1 is Claude-only per design.md).

## Open Questions

(none — wording of the briefing is delegated to implementation, anchored by the
acceptance criteria above)

## Questions for Research/Planning

- [ ] Where does `agent-creance run` build the agent argv, and does appending
      `--append-system-prompt` interact with user-supplied agent args?
- [ ] Does `--append-system-prompt` reach interactive (non-`-p`) Claude Code
      sessions, and is it inherited by subagents? (Main-agent-only is acceptable;
      document the limitation if so.)
- [ ] Which golden files / testscripts / integration tests assert 403 today
      (enforcer responses, audit entries, skill integration test)?
- [ ] Does the `http_connect`-phase hard-deny for passthrough hosts go through the
      same response renderer (i.e., one place to change the status code)?
- [ ] Any in-repo consumers of the literal 403 besides the skill (doctor checks,
      `policy explain` rendering, docs)?

## References

- `internal/proxy/enforcer/responses.py` — single implementation of the refusal
  wire responses (status constant lives here).
- `internal/setup/SKILL.md` — shipped skill whose triggers key on the refusal shape.
- `docs/design.md` — "Network refusal handling" (fixes the wire contract this
  ticket amends).
- Debugging session 2026-06-12 (this ticket's origin): caged research subagent in
  toby-plugins thrashed 8+ min; audit log showed instant soft-denies; WebFetch
  experiments via webhook.site mocks proved status-code-only visibility.
  WebFetch UA observed: `Claude-User (claude-code/2.1.175; +https://support.anthropic.com/)`.

## Implementation Plan

[Leave empty - will be filled when plan is created]

## Notes & Updates

### 2026-06-12

- Created out of a live debugging session; all experiments referenced above were
  run against the real toby-plugins cage proxy and webhook.site mocks.
- 470/471 chosen over registered codes: 451's legal connotation is wrong; unknown
  4xx is treated as generic 400-class per RFC 9110 (clients fail cleanly, no
  retries); AWS ALB squats 460/463/464, so those were avoided.
- Launch briefing endorsed by Toby as the primary fix ("definitely super good");
  status codes added because they are the only WebFetch-visible channel.
- Redirect-based refusal rendering explicitly parked (works, but adds semantic
  risk for redirect-following clients; revisit only if needed).
