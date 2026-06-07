---
name: agent-creance
description: Explains how to react to agent-creance network egress refusals. Use when an HTTP request returns 403 with an "X-Cage-Reason" header or a JSON body whose "error" starts with "agent_cage_" (agent_cage_not_allowlisted = soft-deny, agent_cage_hard_deny = hard-deny). Covers the three response types — allowed, soft-deny, hard-deny — and the right action for each.
---

# Reacting to agent-creance network refusals

agent-creance runs you inside an egress-filtered cage. Outbound HTTP requests pass
through a proxy that returns one of three response types. Recognize a refusal by the
`X-Cage-Reason` response header and the `error` field of the JSON body (it starts
with `agent_cage_`).

## 1. Allowed

A normal upstream HTTP response. The URL matched the project allowlist. There is no
`X-Cage-Reason` header. Proceed as usual.

## 2. Soft-deny — not allowlisted, could be added

HTTP `403` with header `X-Cage-Reason: soft-deny`. JSON body fields: `error`
(`agent_cage_not_allowlisted`), `url`, `host`, `path`, `method`, `how_to_proceed`,
`allow_command_suggestion`.

**What to do:** Ignore the resource and proceed if you can find the needed
information elsewhere or can work reliably without it. Only if the information is
important and would contribute significantly to your success, prompt the user and
ask them to add the resource to the allowlist — the body's `allow_command_suggestion`
gives the exact `agent-creance allow '<host><path>'` command. Do not retry the same
URL blindly.

## 3. Hard-deny — permanently blocked, find another way

HTTP `403` with header `X-Cage-Reason: hard-deny`. JSON body fields: `error`
(`agent_cage_hard_deny`), `url`, `reason` (why it is blocked), `how_to_proceed`.

**What to do:** Treat it as final. Do NOT ask the user to allow it. Do NOT retry.
Find an alternative source, or tell the user no authoritative source could be found.
