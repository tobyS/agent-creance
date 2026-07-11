---
date: 2026-07-11
topic: "Field feedback: how a caged agent suggested `agent-creance allow` to its user"
status: recorded
---

# Field feedback: in-cage agent suggesting `agent-creance allow`

Feedback from another coding session (a website project) whose agent runs inside an
agent-creance cage. Recorded verbatim below, followed by an analysis of what it shows
about the tool's guidance surfaces. Two concrete gaps came out of it.

## Transcript (caged agent talking to its user)

> Couldn't pull the MailerLite doc (the cage blocks their help site), so I'll go on
> what I know plus your screenshots.
>
> [… task-specific MailerLite discussion elided …]
>
> ❯ Give me the agent-creance command to unblock all MailerLite docs.
>
> `agent-creance allow 'www.mailerlite.com/help/*'`
>
> That unblocks the whole /help/ docs section (any article path under it). Run it
> with the ! prefix so I can see the result:
>
> `! agent-creance allow 'www.mailerlite.com/help/*'`
>
> If you'd rather open up the entire MailerLite marketing/docs domain (not just
> /help/), use the broader form instead:
>
> `! agent-creance allow 'www.mailerlite.com/*'`
>
> Once it's allowlisted I'll re-fetch the double opt-in / confirmation-thank-you-page
> doc and give you the exact, verified steps (and whether the custom redirect is
> premium-gated on your trial).

## What went well

- The agent recognized the block as the cage (not the site), degraded gracefully
  (continued from prior knowledge + user screenshots), and only escalated to an
  allowlist request when the user asked — exactly the soft-deny posture the skill
  (`internal/setup/SKILL.md` section 2) prescribes.

## Gap 1: the `!` prefix advice is wrong inside the cage

The agent told the user to run the command with Claude Code's `!` prefix "so I can
see the result". In a caged session, `!` runs the command **inside the cage**, where
it cannot work:

- Since AC-0053's security fix, the cage denies in-cage *writes* of every resolved
  config file, so an in-cage `agent-creance allow` cannot append to
  `.agent-creance.yaml`.
- `policy.json` lives out-of-tree in the state dir and is unmounted; only the
  host-side CLI can write it (docs/design.md, "policy.json").

Correct guidance: the user runs `agent-creance allow …` **on the host, in a separate
terminal**; the config watcher / enforcer hot-reload propagates it to the running
proxy within milliseconds, no cage restart needed. The in-cage skill
(`internal/setup/SKILL.md` section 2) tells agents to relay `allow_command_suggestion`
but never says *where* the command must run — worth an explicit sentence ("ask the
user to run it on the host in a separate terminal; it takes effect immediately — do
not run it yourself or via `!` inside the caged session").

## Gap 2: agents invent glob syntax the rule language doesn't need

The agent composed `'www.mailerlite.com/help/*'` and `'www.mailerlite.com/*'`. Both
are *valid* (a `*` path segment matches one segment; matching is prefix-by-default
past the pattern, see `internal/policy/glob.go`), but non-idiomatic and subtly
narrower than intended:

- `www.mailerlite.com/help/*` requires at least one segment after `help`, so the
  section index page `/help/` itself stays blocked. Idiomatic:
  `agent-creance allow 'www.mailerlite.com/help/'` (prefix covers the whole subtree
  including `/help` itself) — the form the 470 body's `allow_command_suggestion`
  uses (docs/design.md example: `allow 'docs.somelib.io/v2/auth/'`).
- `www.mailerlite.com/*` excludes the bare root path `/`. Idiomatic host-wide form:
  `agent-creance allow www.mailerlite.com`.

Likely cause: the agent never saw `allow_command_suggestion` — WebFetch is
body-blind on 470 (AC-0047/AC-0049), and it evidently skipped the skill's section-5
curl step that would have surfaced the exact command — so it fabricated a pattern
from generic glob intuition. Two possible mitigations to weigh:

1. Strengthen SKILL.md: when suggesting an allow command without having seen the
   470 body, use a plain path prefix, never `*`/`**` (prefix-by-default makes
   wildcards unnecessary for "everything under this path").
2. Optionally: `agent-creance allow` could warn (not refuse) on a trailing `/*`
   segment, since it almost always means "prefix" and silently excludes the index
   page.

No ticket filed yet — recorded for future reference; fold into the next skill/UX
ticket touching `internal/setup/SKILL.md`.
