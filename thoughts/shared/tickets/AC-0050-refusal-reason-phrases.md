# AC-0050: Informative HTTP reason phrases on 470/471 refusals

**Status:** Done
**Estimated Complexity:** Small
**Created:** 2026-06-13
**Updated:** 2026-06-13

## Problem Statement

AC-0047 made the cage refuse with custom status codes 470 (soft-deny) / 471
(hard-deny). mitmproxy's `Response.make` defaults the HTTP reason phrase to the
empty string for unregistered codes, so today's 470/471 responses go out with a
blank reason over HTTP/1.1. A non-empty, self-describing reason phrase costs
nothing and aids every context that surfaces it.

Empirical finding (2026-06-13, this matters for scope): Claude Code's WebFetch
does **not** surface a useful name — it reports "HTTP 470 Unknown Status"
generated from its own status-code table, and HTTP/2 carries no reason phrase at
all (mitmproxy negotiates h2 with capable clients). So the reason phrase is NOT
a channel to a WebFetch-using agent; it serves HTTP/1.1 clients that echo it
(curl -v, git, some libraries), mitmproxy's own logs, and humans debugging the
cage.

## Desired Outcome

470/471 refusals carry an explicit, self-describing reason phrase
("agent-creance soft-deny (not allowlisted)" / "agent-creance hard-deny
(blocked)") instead of an empty one, so any client or log that shows the status
line gets a meaningful name. No change to the status numbers, headers, or JSON
bodies.

## User Stories / Use Cases

- As a developer debugging the cage with `curl -v` or reading mitmproxy logs, I
  want the 470/471 status line to name the cage and the deny type so I don't
  have to look up what the number means.
- As an HTTP/1.1 client that echoes the server reason phrase, I want a
  meaningful phrase rather than a blank one.

## Acceptance Criteria

- [x] Soft-deny responses carry reason phrase `agent-creance soft-deny (not allowlisted)`.
- [x] Hard-deny responses carry reason phrase `agent-creance hard-deny (blocked)`.
- [x] The phrase reaches the wire over HTTP/1.1 (verified by the enforcer
      integration suite against a live mitmdump —
      `test_refusal_reason_phrase_on_the_wire_http1`, 11 passed).
- [x] Status numbers, `X-Cage-Reason` header, and JSON bodies are unchanged
      (body goldens untouched).
- [x] `make test-enforcer`, `make test`, `make lint` pass; `make build` at the end.

## Out of Scope

- Making the name visible to WebFetch — impossible (h2 has no reason phrase;
  WebFetch synthesizes "Unknown Status" for unknown codes). The number +
  briefing + skill remain the WebFetch-facing channel (AC-0047).
- Any change to status codes, headers, or bodies.
- Upstream WebFetch fix (separate, to be filed on anthropics/claude-code).

## Open Questions

None.

## Questions for Research/Planning

None — implementation is well-understood from the AC-0047 session: add a
`reason_phrase` to the enforcer's `CageResponse`, set it on the mitmproxy
`Response`. No separate research/plan doc.

## References

- AC-0047 (`thoughts/shared/tickets/AC-0047-webfetch-visible-refusals.md`) —
  introduced 470/471; this completes the "as informative as possible" goal for
  non-WebFetch clients.
- `internal/proxy/enforcer/responses.py`, `internal/proxy/enforcer/enforcer.py`.

## Implementation Plan

[Leave empty - trivial; see Questions for Research/Planning]

## Notes & Updates

### 2026-06-13
- Created after the AC-0047 follow-up discussion on whether the agent receives a
  status name. Verified WebFetch shows "Unknown Status"; reason phrase scoped to
  non-WebFetch clients accordingly.
