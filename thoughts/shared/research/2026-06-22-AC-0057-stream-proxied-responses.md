---
date: 2026-06-22
ticket: AC-0057
branch: main
commit: 8ab4451
topic: "Stream proxied responses through the enforcer instead of buffering them"
status: complete
---

# Research: AC-0057 — Stream proxied responses through the enforcer

## Research question

How should the mitmproxy enforcer be changed so that long / Server-Sent-Events (SSE)
responses are relayed to the caged client **incrementally** rather than buffered in
full, without breaking the allow/deny decision or the status-level audit log? Scope
(per the ticket): the fix applies to **all** streaming responses, keeping
`api.anthropic.com` intercepted and audited (not switching it to passthrough).

## Summary / TL;DR

- **Root cause confirmed in code.** The enforcer never enables streaming. mitmproxy's
  default is to fully buffer each response body before delivering any of it. The
  running `mitmdump` command (`internal/proxy/lifecycle.go:394` `mitmArgs`) passes no
  `stream_large_bodies`/`body_size_limit`, and the addon
  (`internal/proxy/enforcer/enforcer.py`) has **no `responseheaders` hook** and never
  sets `flow.response.stream`. So an SSE inference response (`POST /v1/messages`) is
  withheld from the caged client until the whole generation completes — once that
  exceeds Claude Code's stream/first-byte timeout, every attempt times out.
- **The fix is small and addon-local.** Add a `responseheaders` hook that sets
  `flow.response.stream = True`. This is the canonical mitmproxy mechanism; it must be
  in `responseheaders` (after headers, before body) — setting it in `response` is "too
  late". No `internal/proxy/lifecycle.go` change is required (the established pattern is
  to set proxy behavior addon-side, exactly like `connection_strategy=lazy` /
  `upstream_cert=False` in `running()`).
- **The audit survives streaming — this was the key risk and it's clear.** The
  `response` hook still fires for a streamed response (after the body drains), and
  `flow.response.status_code` is fully available there (status/headers are read at
  `responseheaders` time). The audit writer (`enforcer.py:232-255`) reads only
  `status_code` plus request fields and metadata — it never reads
  `flow.response.content` — so the single-audit-point design is preserved unchanged.
- **No conflict with 470/471 refusals.** Refusals are synthesized in the `request`
  hook and short-circuit before upstream is contacted, so `responseheaders` never fires
  for them. The wire-contract goldens (`internal/proxy/enforcer/testdata/`) are
  untouched.
- **One real open decision (detection mechanism):** stream **all** upstream responses
  unconditionally vs. gate on `Content-Type: text/event-stream` vs. gate on
  "no fixed Content-Length". See Open Questions.
- **Testing gap:** there is **no local upstream HTTP server** in the test suite;
  integration tests hit real external hosts. An incremental-delivery test needs a new
  local chunked/SSE origin (stdlib `http.server`, no new dependency) plus a streaming
  curl driver (`curl -N --no-buffer` read line-by-line), in the `integration`-marked
  suite.

## The bug, precisely

Default mitmproxy buffers response bodies so addons can inspect/modify them. The
enforcer inspects **nothing** on the response (decision is request-side only), yet it
inherits the buffering. For SSE, buffering means the client receives zero bytes until
the upstream closes the stream — converting streaming into all-at-once delivery.

Live-system evidence gathered during diagnosis (see the ticket): through the running
proxy, telemetry POSTs to `api.anthropic.com` keep returning 200 while `/v1/messages`
goes silent the moment a turn's generation outlasts the client timeout; a host-side
`curl` through the proxy reaches Anthropic in ~75 ms. The proxy is healthy — the
buffering is the fault. Un-caged Claude is unaffected (native incremental SSE).

## Detailed findings

### Enforcer hook flow (`internal/proxy/enforcer/enforcer.py`)

- `request` (`:198-230`) — the allow/deny decision point for intercepted flows. Builds
  `policy.Request` from `flow.request.pretty_host` / `.path` / `.method` only
  (`:203-207`) — **never reads the request body**. Stashes the verdict on
  `flow.metadata["creance_audit"]` (`:213-216`). On allow, returns (forward untouched);
  on soft/hard-deny, sets `flow.response = _make_response(...)` (`:230`).
- `response` (`:232-255`) — **the single audit-write point**. Reads
  `flow.metadata["creance_audit"]` (the stashed verdict) and `flow.response.status_code`
  (`:253`); writes `audit.request_entry(method, pretty_url, decision, rule, status)`.
  Never reads `flow.response.content`. Docstring (`:234-240`): fires for real upstream
  responses **and** synthesized refusals (mitmproxy emulates it for addon-set
  responses).
- `http_connect` (`:167-184`) / `tls_clienthello` (`:186-196`) — passthrough handling;
  host-only audit. Unaffected by response streaming.
- `running` (`:109-125`) — sets `ctx.options.update(connection_strategy="lazy",
  upstream_cert=False)` and starts the policy hot-reload poll. **This is the precedent**
  for configuring proxy behavior addon-side. A streaming change must preserve this call
  intact.
- **No `responseheaders`/`requestheaders` hook exists** (confirmed by grep across
  `internal/`). The addon never touches `flow.response.stream` / `flow.request.stream`
  or any response/request body.

### Why the audit is safe under streaming (the crux)

mitmproxy event lifecycle (official docs):
- `responseheaders` — "HTTP response headers were successfully read. At this point, the
  body is empty." This is where `flow.response.stream = True` must be set.
- `response` — "The full HTTP response has been read. Note: If response streaming is
  active, this event fires after the entire body has been streamed."

So under streaming the `response` hook fires **later** (at stream completion) but still
fires, and `status_code` — part of the headers read at `responseheaders` time — is
populated. The enforcer's audit reads only `status_code` + request fields + stashed
metadata, none of which depend on a buffered body. Net: the audit entry is still
written, with the correct status. (Timing note: for a long SSE stream the audit line
lands when the stream closes, not at first byte — acceptable; status is accurate.)

Body availability: when streamed, `flow.response.content`/`.text` are **not** available
(body not stored). Irrelevant here — the enforcer reads no response body.

### Where / how to enable streaming

- **Mechanism A (recommended): addon `responseheaders` hook** setting
  `flow.response.stream = True`. Precise, content-aware if gated, and consistent with
  the existing `running()` pattern. No Go change.
- **Mechanism B: `stream_large_bodies` option** (e.g. `--set stream_large_bodies=1` or a
  size threshold), set via `ctx.options` in `running()` or via `mitmArgs`. Size-based
  triggering is a poor fit for SSE (small chunks may not promptly cross a byte
  threshold), so Mechanism A is preferred. `mitmArgs` (`lifecycle.go:394-403`) is the
  single assembly point if a `--set` were ever wanted; `TestMitmArgsShape`
  (`lifecycle_internal_test.go:9-22`) asserts args by membership, so additions don't
  break it unless an assertion is added.

Setting `flow.response.stream = True` unconditionally is documented as "equivalent to
`--set stream_large_bodies=1`" — i.e. stream every body.

### Audit / policy / launch supporting detail

- `audit.py` — `request_entry(ts, method, url, decision, rule, status)` →
  `{ts, method, url (scrubbed), decision, rule, status}`; `AuditLog.write` appends one
  JSONL line, flushes, rotates at 500 MB. Single-writer on the asyncio loop.
- `policy.py` — enforcer calls `decide` (→ `Result{decision, mode, matched}`),
  `host_disposition`, `load_policy`, `RuleSet`. Response streaming does not touch any of
  this (decision is request-side).
- `lifecycle.go` — `mitmArgs(port, cfg)` builds `mitmdump --listen-host 127.0.0.1
  --listen-port N -s <enforcer.py> --set creance_policy=… --set creance_audit_log=… -q`;
  spawned via `m.proc.Spawn` (`:138`) in the cold-start/crash-restart branch.

### No conflict with synthesized refusals (470/471)

Refusals are built in `request`/`http_connect` and short-circuit (no upstream
connection under `connection_strategy=lazy`). `responseheaders` only fires for genuine
upstream responses, so a refusal body is never marked for streaming. As a safety belt,
the streaming assignment can guard against a flow that already has an addon-set response
(implementation detail for the plan). Wire goldens under
`internal/proxy/enforcer/testdata/` are unaffected.

### Testing reality (what exists, what's missing)

- Live-proxy scaffold: `test_integration.py` `running_proxy(policy_obj)` context manager
  spawns a real `mitmdump -s enforcer.py --set …` on a free port, waits for readiness,
  yields a `_Proxy` handle. Driven by `_curl(proxy, url, use_mitm_ca=…)` (writes body to
  a file, returns after completion) and asserted via `_wait_for_audit(path, predicate)`.
  Marked module-wide `pytest.mark.integration`.
- **No local upstream server exists** — integration tests hit `example.com` (allow) and
  `example.org` (passthrough), gated by the `egress` fixture. For an incremental-delivery
  test we must introduce a local chunked/SSE origin (stdlib `http.server`, no new dep)
  and a streaming curl driver (`curl -N --no-buffer` via `Popen`, reading lines with
  timestamps to prove first-byte arrives before stream end).
- Unit layer (`test_enforcer.py` with `taddons`/`tflow`/`tutils`): can assert that the
  new `responseheaders` hook sets `flow.response.stream` for the chosen condition, but
  `tflow` does not model incremental delivery — the "delivered incrementally" assertion
  must live in the integration suite.
- Run via `make test-enforcer` (`-m "not integration"`) and `make test-enforcer-integration`
  (`-m integration`). Pinned `mitmproxy==12.2.3`, `pytest==8.3.4`
  (`internal/proxy/enforcer/requirements.txt`). Marker registered in `conftest.py`.

### Loose end resolved: the "~39 MB body cap"

The design's "vite ~39 MB packument exceeds the HTTP body cap" refers to the
**generator's host-side registry HTTP client** (`internal/generator/registry/npm.go:12`),
not the proxy. It is the reason the generator fetches the abbreviated packument. It has
nothing to do with the enforcer or response streaming.

## Code references

- `internal/proxy/enforcer/enforcer.py:109-125` — `running()`; addon-side option pattern.
- `internal/proxy/enforcer/enforcer.py:198-230` — `request` hook (decision; request-body never read).
- `internal/proxy/enforcer/enforcer.py:232-255` — `response` hook (single audit point; reads only `status_code`).
- `internal/proxy/enforcer/audit.py:97-114,146-156` — audit entry shape and write path.
- `internal/proxy/lifecycle.go:392-403` — `mitmArgs` (mitmdump command assembly).
- `internal/proxy/lifecycle_internal_test.go:9-22` — `TestMitmArgsShape` (membership assertions).
- `internal/proxy/enforcer/test_integration.py:74-185` — `running_proxy` / `_curl` / `_wait_for_audit`.
- `internal/proxy/enforcer/test_enforcer.py` — addon-hook unit tests (`taddons`/`tflow`/`tutils`).
- `internal/proxy/enforcer/conftest.py:29-42` — `integration` marker + warning filters.
- `Makefile:59-79` — `enforcer-venv` / `test-enforcer` / `test-enforcer-integration`.
- `internal/generator/registry/npm.go:12-13` — the unrelated 39 MB packument body cap.

## Open questions (for the planning checkpoint)

1. **Detection mechanism for "all streaming responses":**
   - (a) Stream **all** upstream responses unconditionally (`flow.response.stream = True`
     in `responseheaders` for every flow; ≡ `stream_large_bodies=1`). Broadest, simplest,
     most robust; the enforcer reads no response body so buffering buys nothing.
   - (b) Gate on `Content-Type: text/event-stream` — precise SSE fix (covers Claude
     inference + LLM SSE), leaves non-SSE responses buffered as today; smaller blast
     radius.
   - (c) Gate on "no fixed `Content-Length`" (chunked/SSE) — streams genuinely-streaming
     responses, buffers fixed-length bodies; middle ground with slightly more logic.

   These are all valid; the choice is a behavior/risk trade-off (audit-timing shift and
   blast radius vs. completeness). The ticket's stated scope ("all streaming responses")
   favors (a) or (c).

## Questions answered by this research (no longer open)

- Mechanism + hook: `flow.response.stream = True` in a new `responseheaders` hook.
- Audit timing: `response` still fires with `status_code` available — audit preserved.
- Request-body streaming: not needed; the issue and fix are response-side only.
- Go/lifecycle change: not required (addon-side, like `lazy`/`upstream_cert`).
- Refusal conflict: none (refusals short-circuit before `responseheaders`).

## Related documents

- `thoughts/shared/tickets/AC-0057-stream-proxied-responses.md` — the ticket.
- `thoughts/shared/plans/2026-06-06-AC-0017-enforcer-decision-engine.md` — original
  enforcer decision-engine design (hook structure; audit deferred to AC-0018).
- `thoughts/shared/research/2026-06-06-AC-0019-embed-extract-enforcer.md` — the four
  embedded enforcer modules (`enforcer`/`policy`/`audit`/`responses`).
