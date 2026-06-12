---
name: agent-creance
description: Explains how to react to agent-creance network egress refusals and in-cage authentication failures. Use when an HTTP request returns status 470 or 471 with an "X-Cage-Reason" header or a JSON body whose "error" starts with "agent_cage_" (agent_cage_not_allowlisted = 470 = soft-deny, agent_cage_hard_deny = 471 = hard-deny), OR when a fetch tool such as WebFetch fails with a bare 470 or 471 — "response body was not retrieved", no headers or body visible — while running inside the cage, OR when Claude Code inside the cage shows a login/onboarding prompt or an OAuth error like "Failed to start OAuth callback server" / "Is port 0 in use?". Covers the three egress response types — allowed, soft-deny, hard-deny — plus the body-blind fetch case and the authentication-failure case, and the right action for each.
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

HTTP `470` with header `X-Cage-Reason: soft-deny`. JSON body fields: `error`
(`agent_cage_not_allowlisted`), `url`, `host`, `path`, `method`, `how_to_proceed`,
`allow_command_suggestion`.

**What to do:** Ignore the resource and proceed if you can find the needed
information elsewhere or can work reliably without it. Only if the information is
important and would contribute significantly to your success, prompt the user and
ask them to add the resource to the allowlist — the body's `allow_command_suggestion`
gives the exact `agent-creance allow '<host><path>'` command. Do not retry the same
URL blindly.

## 3. Hard-deny — permanently blocked, find another way

HTTP `471` with header `X-Cage-Reason: hard-deny`. JSON body fields: `error`
(`agent_cage_hard_deny`), `url`, `reason` (why it is blocked), `how_to_proceed`.

**What to do:** Treat it as final. Do NOT ask the user to allow it. Do NOT retry.
Find an alternative source, or tell the user no authoritative source could be found.

## 4. Body-blind clients — WebFetch hides the refusal

Some fetch tools (notably Claude Code's WebFetch) discard the body and headers
of non-2xx responses: you see only something like "The server returned HTTP
470 Unknown Status. The response body was not retrieved." Inside the cage, a
bare 470 is always a soft-deny and a bare 471 a hard-deny (the two refusals
above) — NOT the website blocking you, and NOT an authentication problem.

**What to do:** Do NOT conclude the site blocks direct fetches or requires
auth. Do NOT try mirrors or alternative URLs for the same content. Fetch the
same URL with curl in the shell instead — the cage already exports the proxy
environment and CA, so a plain `curl '<url>'` shows the structured JSON
refusal — then follow section 2 (soft-deny) or section 3 (hard-deny).

## 5. Authentication failure — log in on the host, never in the cage

Symptoms: a login/onboarding prompt appears inside the caged session, or an OAuth
error like `Failed to start OAuth callback server` / `Is port 0 in use?`. The cage
blocks all inbound binds by design, so the OAuth callback server can never start —
an in-cage login is impossible by construction, not a transient error.

**What to do:** Do NOT retry the login flow inside the cage. Tell the user to run
`claude` and log in **on the host, outside the cage** (their normal terminal), then
restart the caged session (`agent-creance run`). The cage shares the host's login
via the Keychain, so a valid host login is all that is needed. If the failure
persists after a host login, the login keychain may be locked — unlocking it (e.g.
via Keychain Access or `security unlock-keychain`) and restarting also happens on
the host.
