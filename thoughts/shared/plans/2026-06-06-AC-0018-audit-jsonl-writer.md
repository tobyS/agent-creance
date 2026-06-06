---
date: 2026-06-06
ticket: AC-0018
title: "Plan — Audit JSONL writer + rotation (WP-3.2)"
status: ready
research: thoughts/shared/research/2026-06-06-AC-0018-audit-jsonl-writer.md
git_commit: d025b6e
branch: main
---

# Plan: AC-0018 — Audit JSONL writer + rotation (WP-3.2)

## Overview

Extend the AC-0017 mitmproxy enforcer addon so every egress decision is recorded to
a `0600` JSONL audit log (`egress.jsonl`) in the out-of-tree state dir, with
size-based rotation (500 MB → `.1`, ~1 GB/project cap) that never drops entries, and
with sensitive query-string tokens scrubbed from the logged URL.

The work is entirely in the Python addon (`internal/proxy/enforcer/`):

1. A new pure module `audit.py` — entry builders + URL scrubbing (no mitmproxy
   import, golden/unit-testable) and an `AuditLog` writer class owning the file
   handle, `0600` mode, and rotation.
2. Wiring in `enforcer.py` — a new `creance_audit_log` mitmproxy option (mirroring
   `creance_policy`; Go wiring is AC-0020) and four logging points across the
   existing hooks.

## Decisions carried from the research checkpoint

- **Redaction = listed fields + URL query scrub, no headers map.** The audit entry
  contains exactly the design's fields (timestamp, method, URL, decision, matching
  rule, status). "Redaction" is implemented by scrubbing sensitive query-string
  parameters (`token`, `access_token`, `api_key`, `apikey`, `key`, `secret`,
  `client_secret`, `password`, `code`, `sig`, `signature`, `auth`) out of the logged
  URL — param name matched case-insensitively, value replaced with `REDACTED`, the
  param kept so the audit shows a token *was* present. No request-headers map is
  logged, so "redacted headers are absent" holds trivially and nothing secret leaks
  via headers either.
- **Commits this session are unsigned** (`git commit --no-gpg-sign`); the signing key
  is unreadable in this environment.

## Current state

- `enforcer.py:129-176` has `http_connect` (refuse CONNECT for host-denied
  passthrough), `tls_clienthello` (tunnel clean passthrough via
  `ignore_connection`), and `request` (intercept allow/soft/hard). No `response`
  hook. `enforcer.py:27` lists audit logging as deferred to AC-0018.
- `policy.decide` returns `Result(decision, mode, matched)`, `matched` is a
  `MatchedRule` with `.to_dict()` → `{"list","index"}` (`policy.py:112-133`).
- `responses._encode` produces *indented* operator JSON — the audit writer needs
  *compact* one-line JSONL instead, so it gets its own encoder.
- The out-of-tree path + C4 invariant are Go-side and already tested
  (`state.go:164`, `state_test.go:215-246`). The addon writes only where its option
  points.

## Desired end state

- `egress.jsonl` (mode `0600`) accrues one compact JSON line per egress decision:
  - **Intercepted** (allow/soft-deny/hard-deny): `{ts, method, url, decision, rule,
    status}`, `url` scrubbed.
  - **Passthrough** (host-only): `{ts, host, decision}` — no path/method/status, no
    byte counts (impossible for ignored connections; documented limitation).
- A write that would exceed 500 MB rotates first (`egress.jsonl` → `egress.jsonl.1`,
  fresh current), preserving every entry.
- `make test-enforcer` covers schema goldens, URL scrubbing, mode, rotation, and the
  four hook scenarios; `make test-enforcer-integration` confirms live entries +
  host-only passthrough.

## What we're NOT doing

- Reading/tailing the log (AC-0021) and the Go `internal/audit` reader.
- Wiring the `creance_audit_log` option from the Go launcher (AC-0020).
- Byte counts for passthrough tunnels (not obtainable from the addon for ignored
  connections — see research finding #3).
- A request-headers map in the entry (per the checkpoint decision).

---

## Phase 1 — `audit.py` pure module + writer, with tests

### Changes

**New file `internal/proxy/enforcer/audit.py`** (no mitmproxy import; module docstring
in the house style explaining purity + JSONL + rotation):

- Constants:
  - `DEFAULT_MAX_BYTES = 500 * 1024 * 1024`
  - `ROTATED_SUFFIX = ".1"`
  - `REDACTED = "REDACTED"`
  - `REDACT_QUERY_PARAMS = frozenset({"token","access_token","api_key","apikey",
    "key","secret","client_secret","password","code","sig","signature","auth"})`
    (lowercased; matched case-insensitively)
- `now_iso() -> str` — `datetime.now(timezone.utc).isoformat()` (hooks call this; the
  pure builders take the timestamp as a parameter so goldens are deterministic).
- `scrub_url(url: str) -> str` — `urllib.parse.urlsplit`, parse query with
  `parse_qsl(keep_blank_values=True)`, replace values whose key `.lower()` is in
  `REDACT_QUERY_PARAMS` with `REDACTED`, re-`urlencode` (order preserved),
  `urlunsplit`. Returns the URL unchanged when there is no query.
- `request_entry(ts, method, url, decision, rule, status) -> dict` — ordered:
  `{"ts","method","url","decision","rule","status"}`; `url` scrubbed; `rule` is the
  `MatchedRule.to_dict()` dict or `None`.
- `passthrough_entry(ts, host, decision) -> dict` — ordered: `{"ts","host","decision"}`.
- `encode(entry: dict) -> bytes` — compact one line:
  `json.dumps(entry, ensure_ascii=False, separators=(",", ":")) + "\n"`, UTF-8.
- `class AuditLog`:
  - `__init__(self, path, max_bytes=DEFAULT_MAX_BYTES)` — store path, rotated path
    (`path + ROTATED_SUFFIX`), `max_bytes`; `_fh=None`, `_size=0`.
  - `_ensure_open()` — `os.makedirs(dirname, exist_ok=True)` if dirname; open with
    `os.open(path, O_WRONLY|O_CREAT|O_APPEND, 0o600)`, then `os.fchmod(fd, 0o600)` to
    guarantee the bits regardless of umask; `os.fdopen(fd, "ab", buffering=0)`; seed
    `_size = os.path.getsize(path)`.
  - `write(self, entry)` — `line = encode(entry)`; `_ensure_open()`; if
    `_size > 0 and _size + len(line) > max_bytes: _rotate()`; `_fh.write(line)` (the
    `buffering=0` handle writes through; an explicit `flush` is added for safety on
    buffered handles); `_size += len(line)`. (The `_size > 0` guard means a single
    oversized line is still written to a fresh file rather than looping — an entry is
    never split or dropped.)
  - `_rotate(self)` — `self.close()`; `os.replace(path, rotated)` (atomic; replaces
    any existing `.1`, satisfying "delete `.1`, rename current→`.1`"); `_ensure_open()`;
    `_size = 0`.
  - `close(self)` — close + null `_fh` if open.

**New test `internal/proxy/enforcer/test_audit.py`:**

- Golden (uses the existing `update_golden` fixture + `_assert_golden`-style helper,
  mirroring `test_responses.py`):
  - `request_entry` for an allow with a tokened URL →
    `testdata/egress_request_entry.jsonl.golden`.
  - `passthrough_entry` → `testdata/egress_passthrough_entry.jsonl.golden`.
- Table test for `scrub_url`: no query; `?api_key=abc` → `REDACTED`; mixed
  sensitive+benign (`?q=go&token=xyz` → `q` kept, `token` REDACTED); case-insensitive
  key (`?API_KEY=...`); repeated params; value with special chars (round-trips).
- Writer tests (`tmp_path`):
  - **Mode**: write one entry; assert `oct(os.stat(p).st_mode & 0o777) == 0o600`.
  - **Rotation**: `AuditLog(p, max_bytes=<tiny>)`; write enough entries to cross the
    threshold; assert `egress.jsonl.1` exists, current file is fresh (smaller / new
    first entry), and the **count** of lines across `egress.jsonl` + `egress.jsonl.1`
    equals the number written (no entry lost). Run rotation twice to assert the old
    `.1` is replaced, not accumulated, and disk stays ≤ ~2×max.
  - **Dir creation**: point at a path under a not-yet-existing subdir; assert it's
    created and only `egress.jsonl` (+ `.1` after rotation) appear there (light C4
    "writes only where pointed" check).
  - **Reopen seeds size**: write, `close()`, new `AuditLog` on same path, write again;
    assert appended (no truncation) and size tracking continues.

### Verification

- `make test-enforcer` green (new pure + writer tests pass; existing suite
  unaffected).
- `make test-enforcer -- --update` only when intentionally (re)generating the two
  goldens; review the diff.

### Success criteria

#### Automated
- [ ] `make test-enforcer` passes including `test_audit.py`.
- [ ] Golden files `testdata/egress_request_entry.jsonl.golden` and
      `testdata/egress_passthrough_entry.jsonl.golden` exist and match.
- [ ] Rotation test proves count preservation across the flip; `.1` is replaced not
      accumulated.
- [ ] `0600` asserted via `os.stat`.

#### Manual
- [ ] JSONL lines are compact (one object per line, trailing `\n`), keys in the
      documented order.

---

## Phase 2 — Wire the writer into `enforcer.py` (the four logging points)

### Changes

**`internal/proxy/enforcer/enforcer.py`:**

- `import audit` (alongside `policy`, `responses`).
- `__init__`: add `self._audit: Optional[audit.AuditLog] = None` and
  `self._audit_path: str = ""`.
- `load()`: add a second option:
  ```python
  loader.add_option(
      name="creance_audit_log",
      typespec=str,
      default="",
      help="Path to the egress.jsonl audit log the enforcer appends to ('' disables).",
  )
  ```
- `configure(updated)`: when `"creance_audit_log" in updated`, set
  `self._audit_path = ctx.options.creance_audit_log`; close any existing
  `self._audit`; set `self._audit = audit.AuditLog(self._audit_path)` if the path is
  non-empty else `None`. (Disabled-when-empty mirrors `creance_policy`.)
- `done()`: also `self._audit.close()` if set.
- `request(flow)`: compute `result = policy.decide(...)` as today, then **before** the
  allow early-return, stash the decision for the `response` hook:
  ```python
  flow.metadata["creance_audit"] = {
      "decision": result.decision,
      "rule": result.matched.to_dict() if result.matched is not None else None,
  }
  ```
  Keep the existing allow-return / soft/hard 403 synthesis unchanged.
- **New `response(flow)` hook** — the single logging point for intercepted requests
  (fires for real *and* addon-synthesized responses, per research #1):
  ```python
  def response(self, flow):
      if self._audit is None:
          return
      rec = flow.metadata.get("creance_audit")
      if rec is None:
          return  # CONNECT / passthrough flows never set this
      self._audit.write(audit.request_entry(
          audit.now_iso(),
          flow.request.method,
          flow.request.pretty_url,
          rec["decision"],
          rec["rule"],
          flow.response.status_code,
      ))
  ```
- `http_connect(flow)`: in the denied-passthrough branch (after synthesizing
  `hard_deny`), also log a host-only entry:
  ```python
  if self._audit is not None:
      self._audit.write(audit.passthrough_entry(
          audit.now_iso(), host, policy.DECISION_HARD_DENY))
  ```
- `tls_clienthello(data)`: in the clean-passthrough branch (where
  `data.ignore_connection = True`), also log a host-only allow entry:
  ```python
  if self._audit is not None:
      self._audit.write(audit.passthrough_entry(
          audit.now_iso(), sni, policy.DECISION_ALLOW))
  ```
- Update the module docstring: remove "audit logging (AC-0018)" from the out-of-scope
  list (it's now implemented) and add a one-line note that the audit log path arrives
  via the `creance_audit_log` option (Go wiring deferred to AC-0020).

**No double-logging** (reasoned in research): `request`/`response` fire only for
intercepted HTTP flows; CONNECT and ignored passthrough flows never reach the
`response` hook (no metadata), and a denied-passthrough CONNECT short-circuits before
`tls_clienthello`, so each scenario logs exactly once.

**`internal/proxy/enforcer/test_enforcer.py`** additions (extend the existing
`taddons`-based suite):

- Update the `addon` fixture to also `tctx.configure(a, creance_audit_log=str(...))`
  pointing at a `tmp_path` file; add a helper to read the JSONL lines back.
- `test_intercept_allow_logs_entry`: build a flow to `react.dev` with a tokened path
  (`/x?api_key=SEKRET`), `addon.request(flow)`, then set `flow.response =
  tutils.tresp(...)` (status 200) and `addon.response(flow)`; assert one line with
  `decision=="allow"`, `status==200`, `method`, and the url with `api_key=REDACTED`
  and **`SEKRET` not present** anywhere in the file.
- `test_intercept_soft_deny_logs_entry`: not-allowlisted host; `request` synthesizes
  403; call `addon.response(flow)`; assert `decision=="soft-deny"`, `rule is None`,
  `status==403`.
- `test_intercept_hard_deny_logs_entry`: `w3schools.com`; assert
  `decision=="hard-deny"`, `rule=={"list":"deny_always","index":...}`, `status==403`.
- `test_passthrough_clean_logs_host_only`: `addon.tls_clienthello(_clienthello(
  "api.anthropic.com"))`; assert a line `{ts, host, decision:"allow"}` with **no**
  `method`/`path`/`url`/`status` keys.
- `test_passthrough_denied_logs_host_only`:
  `addon.http_connect(_https_flow("tunnel-blocked.example","/"))`; assert a host-only
  `hard-deny` entry (no path/method/status).
- `test_audit_disabled_when_option_empty`: an enforcer configured with
  `creance_audit_log=""` writes nothing (no file created) — guards the no-op path.

### Verification

- `make test-enforcer` green (new + existing addon tests).
- `make test` and `go build ./...` still green (Go untouched, but run to confirm the
  commit gate).

### Success criteria

#### Automated
- [ ] `make test-enforcer` passes including the new hook tests.
- [ ] All four scenarios log exactly one entry of the right shape; soft-deny `rule`
      is null; passthrough entries omit path/method/status.
- [ ] Sensitive query token (`SEKRET`) is absent from the log file.
- [ ] Empty `creance_audit_log` writes nothing.
- [ ] `make test` + `go build ./...` green.

#### Manual
- [ ] Reading the docstring, a new contributor can see where the audit path comes
      from and that AC-0020 wires it.

---

## Phase 3 — Integration probes + final verification

### Changes

**`internal/proxy/enforcer/test_integration.py`:**

- `running_proxy`: write an `audit_path = os.path.join(tmp, "egress.jsonl")`, add
  `"--set", f"creance_audit_log={audit_path}"` to the `mitmdump` argv, and expose
  `audit_path` on `_Proxy`. Add a small `_read_audit(proxy)` returning parsed JSONL
  lines (tolerate a brief delay; the write-through handle flushes per entry).
- Extend existing probes (or add focused ones) to assert audit entries appear:
  - After `test_soft_deny_403` / `test_hard_deny_403`: an entry with the matching
    `decision` and `status==403` is present.
  - After `test_allow_forwards_upstream`: an `allow` entry with `status==200`.
  - **New `test_passthrough_logs_host_only`** (under the `egress` fixture): drive a
    passthrough host and assert the audit file has a host-only entry (`host` present,
    `path`/`method`/`status` absent) — directly satisfies AC verification step 4.

### Final verification (AC-level, run everything that could be affected)

- `make test` (Go unit/script, race) — green.
- `go build ./...` — green.
- `make lint` — green.
- `make test-enforcer` — green (pure + writer + addon hooks).
- `make test-enforcer-integration` — green *if* local egress + mitmdump available;
  otherwise note it skipped (mirrors AC-0017's integration posture). Run it and
  report the actual outcome.

### Success criteria

#### Automated
- [ ] AC step 1 (schema golden + redacted/sensitive absent): covered by Phase 1+2.
- [ ] AC step 2 (rotation, count preserved): Phase 1 writer test.
- [ ] AC step 3 (`0600`): Phase 1 writer test.
- [ ] AC step 4 (live entries + host-only passthrough): Phase 3 integration test
      (or documented-skip if no egress).
- [ ] AC step 5 (C4 out-of-tree path): already covered by
      `state_test.go:215-246`; the addon writes only to its configured path
      (Phase 1 dir-creation test).
- [ ] `make test`, `go build ./...`, `make lint`, `make test-enforcer` all green.

#### Manual
- [ ] Ticket acceptance criteria boxes ticked; both ticket research questions
      answered in the ticket Notes (rotation atomicity = non-issue on one asyncio
      thread; redaction = URL query scrub, no headers map).

---

## Testing strategy summary

| AC criterion | Test |
|---|---|
| Entry fields; passthrough host-only | `test_audit.py` goldens + `test_enforcer.py` hook tests |
| Sensitive data redacted before write | `scrub_url` table test + `SEKRET`-absent assertion |
| File mode `0600` | `test_audit.py` stat assertion |
| Rotation at 500 MB, no entries lost | `test_audit.py` tiny-threshold rotation test (count preserved) |
| Written under out-of-tree state dir only | `state_test.go` (path) + `test_audit.py` dir-creation |
| Live behaviour + host-only passthrough | `test_integration.py` (`@integration`) |

## Rollout / risk notes

- Pure stdlib only (`json`, `os`, `urllib.parse`, `datetime`) — no new dependency, no
  `requirements.txt` change.
- The `response` hook now runs for every intercepted flow; it is O(1) and append-only,
  negligible overhead.
- `os.replace` rotation is atomic on one filesystem; the single asyncio thread means
  no write interleaving (research-confirmed) — no lock needed.
- If `make test-enforcer-integration` can't run (no egress in this environment), the
  unit + writer + hook tests still fully cover AC steps 1–3 and 5; step 4's
  host-only-passthrough is additionally unit-covered in Phase 2.
