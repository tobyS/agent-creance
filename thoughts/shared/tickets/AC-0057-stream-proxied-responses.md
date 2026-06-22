# AC-0057: Stream proxied responses through the enforcer instead of buffering them

**Status:** Open
**Estimated Complexity:** Medium
**Created:** 2026-06-22
**Updated:** 2026-06-22

## Problem Statement

The mitmproxy enforcer never enables response streaming: `internal/proxy/enforcer/enforcer.py`
sets no `stream` flag, and `mitmArgs` in `internal/proxy/lifecycle.go:394` passes no
`stream_large_bodies` / `body_size_limit`. mitmproxy therefore buffers each response body
in full before delivering any of it to the caged client. For a long-lived Server-Sent-Events
response — notably Claude Code's `POST /v1/messages` — the caged agent receives **zero bytes
until the entire generation finishes**, at which point the whole body is flushed at once.

As long as a response completes faster than Claude Code's stream/first-byte timeout this is
invisible. Once a single turn's generation time crosses that timeout (large accumulated
context, long outputs, extended thinking), every inference attempt aborts with
"Request timed out" and retries the identical request — which takes just as long — so the
session is stuck. Restarting the cage does not help because the resumed conversation carries
the same context and produces the same long response.

Confirmed on a live system during diagnosis:
- Through the running proxy, a host-side probe reaches `api.anthropic.com` in ~75 ms and
  `example.com` correctly gets a 470 — the proxy is healthy and enforcing policy.
- The egress audit log shows **telemetry** POSTs to `api.anthropic.com`
  (`/api/event_logging/...`) still returning `200`, while **`/v1/messages`** has hundreds of
  historical `200`s but none since the failure began. The audit entry is written in the
  `response` hook, which only fires once the full body is buffered, so a timed-out streaming
  request never completes and never logs — exactly the observed fingerprint.

Un-caged Claude (and a fresh small-context session) is unaffected because it gets native
incremental SSE rather than a buffered-all-at-once relay.

## Desired Outcome

Intercepted responses are relayed to the caged client incrementally as the upstream emits
them, so long/streaming egress no longer times out — while allow/deny enforcement and
status-level audit are preserved unchanged.

## User Stories / Use Cases

- As a developer running a long caged session, I want long Claude responses to stream through
  the cage so that my session doesn't get stuck retrying timed-out requests.
- As a security-conscious user, I want `api.anthropic.com` to stay intercepted and audited so
  that I keep the egress record without having to choose passthrough.
- As a developer using other streaming endpoints (other LLM APIs, progress streams), I want
  them to pass through incrementally so that the cage doesn't silently convert streaming into
  all-at-once delivery.

## Acceptance Criteria

- [ ] A streaming / chunked response from an allowed intercepted host reaches the caged client
      incrementally — the first bytes arrive as the upstream emits them, not only after the
      full body completes.
- [ ] A long streaming inference to `api.anthropic.com` (intercept mode) completes through the
      cage with no client-side "Request timed out", including responses whose total duration
      exceeds the pre-fix buffering threshold.
- [ ] The egress audit log still records one entry per intercepted request with method, URL,
      decision, matching rule, and final HTTP status.
- [ ] allow/deny is still decided before any upstream bytes are forwarded (a denied host is
      never connected to); 470/471 synthesized refusals keep their structured JSON body and
      `X-Cage-Reason` header.
- [ ] A large streamed response does not require holding the entire body in proxy memory.
- [ ] An automated enforcer test drives a streaming upstream through the proxy and asserts
      incremental delivery (not merely final-byte delivery).

## Out of Scope

- Defaulting `api.anthropic.com` to `mode: passthrough` — it remains an available per-host
  option, but is not the chosen fix (streaming keeps the audit).
- Inspecting or auditing streamed response *bodies*; status-only audit is unchanged.
- Tuning Claude Code's client-side timeouts (not ours to change).

## Open Questions

None — the root cause was established during diagnosis.

## Questions for Research/Planning

- [ ] Enable streaming unconditionally for all intercepted responses, or gate it (mitmproxy
      `stream_large_bodies` threshold, or `text/event-stream` content-type)? Weigh the
      trade-offs for audit timing and memory.
- [ ] Mechanism: a `responseheaders` hook setting `flow.response.stream = True` vs. the
      `stream_large_bodies` launcher option — which composes cleanly with the existing
      `request` / `response` hooks and keeps the final status available for the audit write.
- [ ] Does enabling streaming change *when* the `response` hook fires, and is the final HTTP
      status still captured for the audit entry?
- [ ] Is the large *request* body (big context) also a latency factor worth streaming, or is
      response-only streaming sufficient to fix this?
- [ ] How to drive a streaming upstream deterministically in `make test-enforcer-integration`
      so incremental delivery can be asserted.

## References

- `internal/proxy/enforcer/enforcer.py` — `request` / `response` hooks; no streaming enabled.
- `internal/proxy/lifecycle.go:394` — `mitmArgs`; no `stream_large_bodies` / `body_size_limit`.
- `docs/design.md` — "Per-host enforcement modes" (passthrough as the Anthropic alternative)
  and "Network refusal handling" (the 470/471 contract that must remain intact).

## Implementation Plan

_(Filled when the plan is created.)_

## Notes & Updates

### 2026-06-22

Created from a live diagnostic session. A caged session in another project began failing all
Claude inference with "Request timed out" while telemetry to the same host kept succeeding and
the proxy answered host-side probes normally. Root cause traced to the enforcer buffering
responses (no streaming configured), so long SSE inferences outrun Claude Code's timeout;
restarts don't help because the resumed context reproduces the same long response.

Author decisions: (1) durable fix is **enforcer-side streaming** (keep `api.anthropic.com`
intercepted and audited), not defaulting it to passthrough; (2) scope is **all streaming
responses**, not just the Anthropic inference path, so the latent buffering bug is fixed for
every streaming host. Complexity Medium — localized to the enforcer addon and its tests, but
needs a streaming integration test and care to preserve status-level audit.
