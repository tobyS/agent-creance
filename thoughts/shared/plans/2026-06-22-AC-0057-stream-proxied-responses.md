---
date: 2026-06-22
ticket: AC-0057
branch: main
commit: 767b3ca
topic: "Stream proxied responses through the enforcer instead of buffering them"
status: ready
---

# Implementation Plan: AC-0057 — Stream proxied responses through the enforcer

## Overview

The mitmproxy enforcer buffers every upstream response body before delivering any of
it to the caged client, which converts Server-Sent-Events (SSE) streams into
all-at-once delivery. Once a single Claude inference response generates for longer than
Claude Code's stream/first-byte timeout, every attempt times out. The fix is a new
`responseheaders` hook in the enforcer that sets `flow.response.stream = True` for
**all** upstream responses, so chunks are relayed incrementally. The status-level audit
is preserved because the `response` hook still fires (with `status_code` available)
after the body streams.

Per the question checkpoint: stream **all** responses unconditionally (the enforcer
reads no response body, so buffering buys nothing).

## Current state

- `internal/proxy/enforcer/enforcer.py` has no `responseheaders` hook and never sets
  `flow.response.stream`. The `request` hook (`:198-230`) decides allow/deny from
  host/path/method only; the `response` hook (`:232-255`) writes the single audit entry
  reading only `flow.response.status_code`. mitmproxy therefore buffers response bodies
  by default.
- `internal/proxy/lifecycle.go:394` `mitmArgs` passes no streaming option (no change
  needed — streaming is set addon-side, consistent with `connection_strategy=lazy` /
  `upstream_cert=False` in `running()`).
- Tests: `internal/proxy/enforcer/test_enforcer.py` (addon-hook units via
  `taddons`/`tflow`/`tutils`) and `internal/proxy/enforcer/test_integration.py` (live
  `mitmdump` via `running_proxy`, driven by `_curl`, asserted via `_wait_for_audit`).
  **No local upstream server exists** — integration tests hit real external hosts.

See `thoughts/shared/research/2026-06-22-AC-0057-stream-proxied-responses.md` for full
detail.

## Desired end state

- Every allowed, intercepted upstream response is relayed to the caged client
  incrementally (first bytes arrive as upstream emits them).
- A long SSE / chunked response no longer waits for completion before the client sees
  data.
- The egress audit log still records one entry per intercepted request with method,
  URL, decision, rule, and final HTTP status.
- 470/471 refusals and their wire goldens are unchanged.
- An automated integration test proves incremental delivery and that the audit entry is
  still written.

## What we're NOT doing

- Not switching `api.anthropic.com` to `mode: passthrough` (it stays intercepted and
  audited).
- Not changing `internal/proxy/lifecycle.go` / `mitmArgs` (no `--set stream_large_bodies`).
- Not streaming or otherwise changing request bodies.
- Not capturing/auditing response bodies (status-only audit unchanged).
- Not gating streaming by content-type or Content-Length (chosen: stream all).

## Implementation phases

### Phase 1 — Enforcer streaming hook + unit test

**Change `internal/proxy/enforcer/enforcer.py`:** add a `responseheaders` hook to the
`Enforcer` class (between `request` and `response`, with the other enforcement hooks)
that marks every real upstream response for streaming:

```python
def responseheaders(self, flow: http.HTTPFlow) -> None:
    """Stream every upstream response body to the client incrementally.

    mitmproxy buffers response bodies by default so addons can inspect or modify
    them; this enforcer inspects no response body (allow/deny is request-side and
    the audit reads only the status code), so the default buffering merely adds
    latency and breaks long Server-Sent-Events responses -- the caged client sees
    no bytes until the upstream stream closes, so Claude inference times out.

    `stream` must be set here, in the responseheaders hook (after the status line
    and headers are read, before the body): setting it in `response` is too late,
    the body is already buffered. The `response` hook still fires afterwards with
    `status_code` intact, so the audit entry is unaffected. Refusals synthesized
    in `request` short-circuit before upstream is contacted, so this hook never
    runs for them.
    """
    if flow.response is not None:
        flow.response.stream = True
```

Notes:
- The `if flow.response is not None` guard is belt-and-suspenders; `responseheaders`
  fires only when a real upstream response exists.
- No change to `running()`, `request`, or `response`. Keep the
  `ctx.options.update(connection_strategy="lazy", upstream_cert=False)` call intact.

**Add a unit test in `internal/proxy/enforcer/test_enforcer.py`** modelled on the
existing `taddons`/`tflow`/`tutils` tests (e.g. `test_intercept_allow_logs_entry_and_scrubs_url`):

- `test_responseheaders_marks_response_for_streaming`: build an allowed `_https_flow`,
  call `addon.request(flow)` (allow → no response set), set
  `flow.response = tutils.tresp(content=b"data")`, call `addon.responseheaders(flow)`,
  assert `flow.response.stream is True`.
- `test_response_still_audits_after_streaming`: same setup, then call
  `addon.response(flow)` and assert the audit log still has one entry with
  `status == 200` and `decision == "allow"` (proves the audit survives the streaming
  flag). Reuse the `_read_audit` helper.

**Success criteria:**

#### Automated verification
- [ ] `make test-enforcer` passes (new unit tests green; existing units unaffected).

#### Manual verification
- [ ] Diff of `enforcer.py` shows only the new `responseheaders` hook added; `running`,
      `request`, `response` unchanged.

### Phase 2 — Integration test: incremental delivery + audit

**Add an integration test to `internal/proxy/enforcer/test_integration.py`** (module is
already `pytest.mark.integration`) that proves the body streams incrementally. Because
no local origin exists and a real external SSE endpoint can't be asserted
deterministically, introduce a **local plaintext-HTTP streaming origin** (stdlib
`http.server` in a thread — no new dependency, no TLS, so it sidesteps upstream cert
verification):

- A `BaseHTTPRequestHandler` with `protocol_version = "HTTP/1.0"` (close-delimited body,
  so no Content-Length/chunked framing needed) that responds 200 with
  `Content-Type: text/event-stream` and writes several `data: chunkN\n\n` events with a
  fixed delay (e.g. 4 events, ~0.4 s apart), calling `self.wfile.flush()` after each,
  then returns (connection close signals end of body).
- Start it on a free port (reuse the `_free_port()` pattern) in a `threading.Thread`
  (daemon), with a context manager / fixture that shuts it down.
- Policy: allow the origin host (`127.0.0.1`) as an intercept rule — craft the policy
  dict modelled on the existing allow policies in `test_integration.py`. Drive the
  request as **plain HTTP** through the proxy: `curl -x http://127.0.0.1:<proxyport>
  http://127.0.0.1:<originport>/` (no `--cacert` needed; no TLS on either leg's origin).
- Streaming driver: a new helper (don't reuse `_curl`, which buffers to a file and
  returns only on completion). Use `subprocess.Popen` with
  `curl -N --no-buffer -x http://127.0.0.1:<proxyport> http://.../`, read stdout
  line-by-line, and record a monotonic timestamp for the first and last data line.
- Assertions:
  - First data line arrives meaningfully **before** the last (e.g.
    `last_ts - first_ts >= 0.3 s` given the server's total ~1.2 s spread) — proves
    chunks were not all buffered and flushed at once.
  - All emitted events are received (body integrity).
  - `_wait_for_audit(p.audit_path, decision == "allow")` returns an entry with
    `status == 200` — proves the audit still fires for a streamed response.
- This test uses a **local** origin, so it does **not** need the `egress` fixture
  (no external network); it still needs `mitmdump` via `running_proxy` /
  `_require_tooling`.

If `running_proxy` needs to allow plain-HTTP proxying or a `127.0.0.1` origin and that
turns out not to work out of the box, fall back to a local **HTTPS** origin with a
self-signed cert and pass `--set ssl_insecure=true` to the test proxy only (extend
`running_proxy` to accept extra `--set` args). Prefer the plaintext path first; record
in the test which path was taken.

**Success criteria:**

#### Automated verification
- [ ] `make test-enforcer-integration` passes, including the new incremental-delivery
      test.
- [ ] `make test-enforcer` still passes (fast suite unaffected).

#### Manual verification
- [ ] The test fails when the `responseheaders` hook is temporarily reverted (sanity:
      it actually catches buffering). Re-apply the hook after checking.

### Phase 3 — Whole-suite verification, build, and ticket close

- [ ] `make test` (Go race suite) passes — unaffected, run for safety.
- [ ] `make lint` clean.
- [ ] `make build` so `bin/agent-creance` re-embeds the updated `enforcer.py` (the user
      tests with this binary; the embedded copy is what gets extracted at runtime).
- [ ] Set ticket `AC-0057` status to **Done** with a dated note.

## Testing strategy

- **Unit (fast, hermetic):** `make test-enforcer` — assert the `responseheaders` hook
  sets `flow.response.stream` and that `response` still audits. `tflow` can't model
  incremental delivery, so timing lives in the integration test.
- **Integration (live mitmdump):** `make test-enforcer-integration` — local streaming
  origin + `curl -N` driver asserts first-byte-before-last and audit persistence.
- **Go suite + lint + build:** unchanged behavior, run to confirm nothing regressed and
  the binary re-embeds the addon.
- **Manual (optional, real cage):** run a caged Claude session that produces a long
  response and confirm it streams without "Request timed out". Not automated.

## Success criteria (rollup)

#### Automated verification
- [ ] `make test-enforcer` passes.
- [ ] `make test-enforcer-integration` passes (incremental delivery + audit).
- [ ] `make test` passes.
- [ ] `make lint` clean.
- [ ] `make build` succeeds.

#### Manual verification
- [ ] Reverting the hook makes the integration test fail (it genuinely detects
      buffering).
- [ ] `enforcer.py` diff is limited to the new hook.

## References

- Research: `thoughts/shared/research/2026-06-22-AC-0057-stream-proxied-responses.md`
- Ticket: `thoughts/shared/tickets/AC-0057-stream-proxied-responses.md`
- `internal/proxy/enforcer/enforcer.py:198-255` — request/response hooks (insertion point).
- `internal/proxy/enforcer/test_enforcer.py` — unit-test patterns.
- `internal/proxy/enforcer/test_integration.py:74-185` — `running_proxy`/`_curl`/`_wait_for_audit`.
- `Makefile:59-79` — enforcer test targets.
