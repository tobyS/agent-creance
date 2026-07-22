# AC-0071: Time-boxed research window — temporary arbitrary-GET egress + research skill

**Status:** Open
**Estimated Complexity:** Large
**Created:** 2026-07-22
**Updated:** 2026-07-22

## Problem Statement

The cage's egress allowlist is deliberately narrow: anything not in `allow:` gets a
soft-deny (470). That is correct for normal work, but it blocks a legitimate need — an
agent occasionally has to do **open-ended web research** to verify an answer, where the
useful sources cannot be predicted in advance and allowlisting each one up front (or
escalating a 470 per page) is impractical. Today the only tools are `allow --once URL`
(one host/path at a time) and asking the user to hand-edit config — neither supports
"let me read around the web for a few minutes to check this."

We want a controlled way to open a **temporary, time-boxed window** for read-only
(GET) requests to arbitrary hosts, so the agent can research freely, and then have the
cage return to its normal locked state — plus a shipped skill that runs the research
well and captures its results so useful sources can be promoted to the durable
allowlist.

## Desired Outcome

When this is complete:

- A host-side command opens a **research window**: GET/HEAD to arbitrary hosts is
  permitted for a user-chosen duration, capped by a hard maximum. A second command
  closes it early; otherwise it **auto-expires** and the cage reverts to normal
  soft-deny with no residual rules.
- Opening the window is **strictly a relaxation of egress and nothing else**:
  `deny_always` (hard-denies, secrets paths) stays fully enforced, only GET/HEAD is
  allowed, requests are still TLS-intercepted and **audited**, and **no credential
  injection** happens on the wildcard-opened hosts (injection stays bound to the
  explicit allowlist).
- A shipped **research skill** (installed by `agent-creance setup`, like the existing
  refusal skill) drives the agent: it asks the user to open the window for an estimated
  time, coaches source-quality judgement while open, and at the end produces (a) a
  research summary — what was learned and from which source — and (b) a
  recommended-durable-sources list, written as a config fragment that
  `agent-creance import` can merge.

## User Stories / Use Cases

- As a **developer whose caged agent hit a soft-deny wall mid-research**, I want to
  grant a short, bounded research window on request, so that the agent can verify its
  answer against real sources without me permanently widening the allowlist or
  approving pages one at a time.
- As a **caged agent needing to verify a claim**, I want a skill that tells me to ask
  for a research window and then how to research effectively, so that I use the open
  window well and report back what I found and where.
- As a **security-conscious developer**, I want the window to be strictly time-boxed,
  read-only, still hard-deny-enforced, and never inject my credentials to arbitrary
  hosts, so that "open research" can never become an open-ended data-exfiltration or
  credential-leak path.
- As a **developer reviewing research afterward**, I want the agent to hand me the
  useful sources as an importable fragment, so that I can promote the good ones to the
  durable allowlist in one reviewed `import` instead of reconstructing them by hand.

## Acceptance Criteria

- [ ] A host-side command opens a research window for a user-specified duration
      (e.g. `--minutes N`); when omitted it uses a default (15 min); a request above
      the hard ceiling (60 min) is clamped or rejected with a clear message. Default
      and ceiling are constants, documented in the design doc.
- [ ] While open, GET and HEAD requests to hosts **not** otherwise allowlisted succeed
      instead of returning 470; non-GET/HEAD methods to those hosts still return a
      refusal.
- [ ] `deny_always` rules (hard-deny hosts and secrets-path denies like `**/.env`)
      still return 471 while a window is open — the window never overrides a hard-deny.
- [ ] Requests served only because the window is open are **TLS-intercepted and
      appear in the audit log** (host, path, method, status), so `agent-creance logs`
      can enumerate what the research touched.
- [ ] Credential injection is **not** applied to hosts allowed solely by the window;
      injection remains bound to explicit `allow ... inject:` rules only.
- [ ] A host-side command closes the window immediately; and the window **auto-expires**
      at its deadline. After close/expiry, a previously window-only GET returns 470
      again, and no window rule survives into the next session (parity with how
      `--once` overlays are purged on teardown).
- [ ] `agent-creance status` (or equivalent) shows whether a research window is open
      and its remaining time.
- [ ] `agent-creance setup` installs a research skill into `~/.claude/skills/`; an
      existing install is updated, and `--no-skill` (or an equivalent opt-out) skips it.
- [ ] The skill instructs the agent to: request a research window with an estimated
      duration; judge source quality (prefer official docs / primary sources, treat
      soft-denied or low-authority pages skeptically); and honor hard-denies as final.
- [ ] At the end of a research phase the skill has the agent produce a **summary**
      listing each knowledge item and the source URL it came from.
- [ ] The skill has the agent produce a **recommended durable sources** list and write
      it to an **importable config fragment**, choosing rule granularity by host type —
      path-scoped rules for mass-hosters (e.g. github.com, gitlab.com, npmjs.com),
      host-wide GET rules for single-product doc/brand domains — as GET-only intercept
      rules; the user promotes them via `agent-creance import <fragment>`.

## Out of Scope

- Letting the **caged agent** open or close the window itself. Opening egress is a
  privileged, human-authorized action; the agent can only *ask* (via the skill), the
  same as it does for `allow`. The agent cannot run host-side CLI.
- Non-GET methods (POST/PUT/DELETE) to arbitrary hosts. The window is read-only by
  design.
- Passthrough (non-intercepted) research. Window hosts are intercepted so they remain
  audited; opening the window never creates audit blind spots.
- A config kill-switch to forbid research mode. Decided against (the time limit is the
  guardrail); may be revisited as a follow-up if a team needs to hard-forbid it.
- Auto-promotion of researched sources into the committed allowlist. The skill only
  *recommends* and writes a fragment; promotion stays a reviewed `import`.
- Persisting a window across a full cage teardown / machine reboot.

## Open Questions

None outstanding — the time-limit policy, kill-switch decision, and durable-rule
granularity were resolved during authoring (see Notes & Updates).

## Questions for Research/Planning

- [ ] Command surface: a grouped `agent-creance research open|close` vs. top-level
      `agent-creance open|close`. (Draft assumes `research open|close`.)
- [ ] Where the window's expiry timer lives so it fires reliably for the shared,
      refcounted proxy and multi-agent sessions — enforcer-side deadline in
      `policy.json` vs. a host-side timer that rewrites the overlay. It must expire even
      if the opening terminal is gone.
- [ ] How the window overlay composes with the existing session-overlay/`--once`
      mechanism and the config compiler (a new overlay kind vs. a flagged rule).
- [ ] How the wildcard-GET rule is represented so the proxy still evaluates
      `deny_always` first and suppresses injection for window-only matches.
- [ ] How the skill enumerates "sources captured" — read back from the audit log
      (`agent-creance logs`) vs. the agent tracking its own fetches — and what host-type
      classification drives path-scoped vs. host-wide recommendations.
- [ ] Whether HEAD rides alongside GET, and how the window interacts with the
      launch-time cage briefing / existing refusal skill.

## References

- `docs/design.md` — "Network refusal handling" (470/471 semantics), "Session-scoped
  allows" (`--once` overlay + teardown purge), "Config compilation", "Commands", and
  the setup skill-install path.
- Existing shipped skill: `~/.claude/skills/agent-creance/SKILL.md` (refusal handling)
  — the research skill is a sibling installed the same way.
- Related: AC-0047 (custom refusal codes + cage briefing), AC-0053 (in-session
  hot-reload / config-ro), AC-0067 (config-editing command surface, `import`).

## Implementation Plan

[Leave empty — filled when the plan is created.]

## Notes & Updates

### 2026-07-22
Ticket created via guided discussion. Key decisions:
- **Time limit:** user specifies a duration on open, capped by a hard-coded ceiling
  (draft: default 15 min, max 60 min, as tunable constants) — chosen over a single
  fixed duration or a config-set ceiling.
- **No kill switch:** research mode is always available; the fixed time limit is the
  sole guardrail. A config flag to forbid it was explicitly declined (possible
  follow-up).
- **Durable-rule granularity:** the skill classifies by host type — path-scoped rules
  for mass-hosters, host-wide GET rules for single-product doc/brand domains.
- **Asserted safety envelope:** read-only (GET/HEAD), `deny_always` still enforced,
  intercepted/audited, no credential injection on window-only hosts, human-opened/closed
  with auto-expiry.
- **Complexity:** Large — touches the policy compiler, two commands + expiry timer, and
  a new shipped skill, but reuses the overlay/import/skill-install/audit seams.
