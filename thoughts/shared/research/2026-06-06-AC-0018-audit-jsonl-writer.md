---
date: 2026-06-06
ticket: AC-0018
title: "Research — Audit JSONL writer + rotation (WP-3.2)"
status: complete
git_commit: 18e74f0b6e24b0510e9282bca3da3627eedb5dad
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0018 — Audit JSONL writer + rotation (WP-3.2)

## Research question

How should the mitmproxy enforcer record every egress decision to a `0600` JSONL
audit log (`egress.jsonl`) in the out-of-tree state dir — with sensitive headers
redacted, size-based rotation capping disk at ~1 GB/project, and never silently
dropping entries — building on the AC-0017 enforcer addon?

## Summary

The audit writer is a new piece of the **Python enforcer addon** (`internal/proxy/enforcer/`),
not a Go package. (`internal/audit` on the Go side is the *reader*, WP-3.5 / AC-0021 —
out of scope here.) The work is:

1. A new pure-ish module `audit.py` — entry construction + redaction (no mitmproxy
   import, so it is golden/unit-testable) plus an `AuditLog` writer class that owns
   the file handle, the `0600` mode, and rotation.
2. Wiring in `enforcer.py`: a new mitmproxy option `creance_audit_log` (mirroring
   `creance_policy`; Go wiring deferred to AC-0020), and four logging points across
   the existing hooks.

The four logging points map cleanly onto the existing hook structure, and three
mitmproxy behaviors (verified against the v12.2.3 source) make the design
deterministic:

- The `response` hook **fires for addon-synthesized 403s** (set in `request`), so a
  single `response` hook is the one place that logs all intercepted requests
  (allow / soft-deny / hard-deny) *with* their response status.
- `flow.metadata` (a `dict[str, Any]` on every flow) is the supported way to carry
  the decision from the `request` hook to the `response` hook.
- An **ignored (passthrough) connection produces no flow, no TCP hooks, and no byte
  accounting** available to the addon. Passthrough therefore can only be logged at
  the tunnel-decision point (`tls_clienthello` for the allowed tunnel,
  `http_connect` for a host-denied refusal), host-only, **with no byte counts** —
  which is stricter than `docs/design.md`'s "host + timestamp + byte counts" but
  exactly satisfies the ticket's "host-only (no path/method/status)".

The two ticket "Questions for Research/Planning" both resolve:

- **Rotation atomicity under concurrent writes from one addon process** — non-issue.
  mitmproxy hooks run on a single asyncio event loop (one thread); writes are
  serialized. Rotation is `os.replace`/`os.rename` within one directory (atomic on
  the same filesystem). No locking needed.
- **Full redaction header list / query-string tokens** — partially a design
  decision (see Open Questions). The design names `Authorization`, `Cookie`,
  `X-Api-Key` "etc."; the AC test wording ("assert redacted headers are absent")
  implies a request-headers map is logged with the named headers stripped. Whether
  to also log a headers map at all (the design's field list omits headers) and
  whether to scrub query-string tokens needs a decision.

## Authoritative sources

- **Design**: `docs/design.md` — "Audit log" (lines 414–422); per-host modes /
  passthrough (211–247); out-of-tree rationale (290–299, 414–416); passthrough
  audit blind-spot in the threat model (68, 242).
- **Spec**: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`
  — WP-3.2 (226–231); component map (81–82); C4 out-of-tree (108–110); C1 matcher
  parity (94–101); C3 golden testing (106–107).
- **Ticket**: `thoughts/shared/tickets/AC-0018-audit-jsonl-writer.md`.

## Detailed findings

### What the design requires (the contract)

`docs/design.md:414-422`:

> mitmproxy writes a JSONL file at `egress.jsonl` in the project's state directory
> (`~/.cache/agent-creance/projects/<hash>/`), mode `0600`, with sensitive headers
> (`Authorization`, `Cookie`, `X-Api-Key`, etc.) filtered before logging. Each entry
> records timestamp, method, URL, decision (allow / soft-deny / hard-deny), matching
> rule, and response status. … `agent-creance logs` is how you read it.

Rotation, `docs/design.md:418`:

> When a write would push the file past 500 MB, the existing `.1` backup (if any) is
> deleted, the current file is renamed to `egress.jsonl.1`, and the next write begins
> a fresh current file. This caps disk use at roughly 1 GB per project (current +
> backup), and never silently drops entries.

Reader compatibility, `docs/design.md:420`: `logs --summary` reads `.1` then the
current file "as one logical stream", and `logs --follow` is rotation-aware via
`fsnotify`. The writer's naming (`egress.jsonl` + `egress.jsonl.1`) must stay
compatible with that reader (AC-0021).

WP-3.2, `technical-specification.md:226-231`:

> **WP-3.2 — Audit JSONL writer** (in `enforcer.py`). Per-request entry (timestamp,
> method, URL, decision, matching rule, status; host-only for passthrough), `0600`,
> sensitive-header redaction, 500 MB rotation (delete `.1`, rename current→`.1`,
> fresh current). *Done when:* entries match schema golden; redaction verified;
> rotation flips at threshold without dropping entries.

### Where the code lives and the AC-0017 enforcer it extends

The enforcer addon is the only Python in the stack, under `internal/proxy/enforcer/`:

- `enforcer.py` — the mitmproxy glue: `Enforcer` addon class, the `creance_policy`
  option, hot-reload poll loop, and the `http_connect` / `tls_clienthello` /
  `request` hooks. `enforcer.py:27` explicitly defers audit logging to AC-0018.
- `policy.py` — pure matcher (`decide`, `host_disposition`, decision constants). No
  mitmproxy import (keeps the C1 corpus + golden tests runnable without mitmproxy).
- `responses.py` — the three wire 403 bodies + `X-Cage-Reason`. Also pure.
- `conftest.py` — shared fixtures, the `--update` golden flag, the `integration`
  marker, `CORPUS_DIR`.
- `test_enforcer.py` / `test_policy.py` / `test_responses.py` / `test_vectors.py` /
  `test_integration.py` — the addon-hook, matcher, golden, corpus-parity, and live
  probe suites.
- `requirements.txt` — pins `mitmproxy==12.2.3`, `pytest==8.3.4`.
- `Makefile` targets `test-enforcer` (`-m "not integration"`) and
  `test-enforcer-integration` (`-m integration`), both via `.venv-enforcer`.

The existing hooks (`internal/proxy/enforcer/enforcer.py:129-176`):

- `http_connect` — refuses a CONNECT only for a **passthrough host with a host-level
  deny** (synthesizes `responses.hard_deny`). Intercept hosts are allowed to CONNECT
  so TLS terminates.
- `tls_clienthello` — sets `data.ignore_connection = True` for a clean passthrough
  host (tunnel without TLS termination).
- `request` — for intercepted (TLS-terminated) requests: `policy.decide(...)` →
  allow (return, forward) / soft-deny / hard-deny (synthesize 403). The matched rule
  is `result.matched` (a `MatchedRule(list, index)`); the hard-deny reason is
  `self._ruleset.deny_always[result.matched.index].reason`.

### The out-of-tree state path (C4) — already resolved on the Go side

`internal/state/state.go` computes `~/.cache/agent-creance/projects/<hash>/` and
exposes `Layout.EgressJSONL()` → `<root>/egress.jsonl` (`state.go:164`, const
`egressJSONLName` at `state.go:40`). The C4 invariant for this path is **already
tested**: `internal/state/state_test.go:215-246` (`TestAccessorsAreRootedAtProjectsHash`)
asserts `EgressJSONL` lives directly under `Root`, and
`TestCacheRootHonoursXDGThenFallsBackToHome` (`state_test.go:110-138`) asserts
`Root == <cache>/agent-creance/projects/<hash>`.

The Python addon does **not** compute this path — it receives it, like the policy
path, as a mitmproxy option set by the Go launcher (AC-0020). So the addon's only
C4 obligation is to write exactly where it is pointed (no path derivation, no
fallback into the tree).

### mitmproxy 12.2.3 behaviors that fix the design (source-verified)

1. **`response` fires for addon-set responses.** When `request` sets `flow.response`,
   the HTTP layer emulates `responseheaders` + `response` for the synthesized
   response (`mitmproxy/proxy/layers/http/__init__.py`, the `elif self.flow.response:`
   branch: *"response was set by an inline script…"*). So a single `response` hook
   logs allow (real status), soft-deny (403), and hard-deny (403) alike.
2. **`flow.metadata: dict[str, Any]`** exists on the base `Flow` (`mitmproxy/flow.py`,
   `Flow.__init__`) and is the idiomatic per-flow state bag carried across hooks.
   Used to stash the decision/matched-rule in `request`, read back in `response`,
   and to mark which flows we logged (so the `response` hook skips CONNECT flows and
   anything that didn't pass through our `request` hook).
3. **Ignored connections vanish from the addon.** Setting `ignore_connection = True`
   makes `TCPLayer.__init__` set `self.flow = None`; `tcp_start` / `tcp_message` /
   `tcp_end` are all guarded by `if self.flow:` and never fire
   (`mitmproxy/proxy/layers/tcp.py`). **No byte counts are available** for a
   passthrough tunnel. → passthrough logging happens at the *decision* point, host
   only, no bytes.
4. **`http_connect` fires for every CONNECT**, with `flow.request.host` /
   `flow.request.port` populated from the CONNECT authority.

### The four logging points

| Scenario | Hook | Entry shape |
|---|---|---|
| Intercepted allow / soft-deny / hard-deny | `response` (decision stashed in `request`) | full: ts, method, URL, decision, matched rule, status |
| Clean passthrough (tunnelled) | `tls_clienthello` (where `ignore_connection=True`) | host-only: ts, host, decision=allow, mode=passthrough |
| Host-denied passthrough (CONNECT refused) | `http_connect` (where `hard_deny` synthesized) | host-only: ts, host, decision=hard-deny, matched rule |

Guard against double-logging: the `response` hook logs **iff** the `request` hook
stashed an audit record in `flow.metadata`. Since `request` only fires for real
intercepted HTTP requests (never for the CONNECT flow), this cleanly excludes the
passthrough/CONNECT flows that are logged elsewhere.

### Redaction

The design names `Authorization`, `Cookie`, `X-Api-Key` and "etc." The AC's test
step 1 ("assert redacted headers are absent") implies the entry carries a
request-headers map from which the named headers are stripped/replaced. Header
matching must be **case-insensitive** (HTTP header names are). The exact superset
beyond the three named, whether to log a headers map at all, and whether to also
scrub query-string tokens from the logged URL are decisions — see Open Questions.

### Rotation mechanics (resolved)

- Single asyncio thread ⇒ writes serialized ⇒ no concurrent-write race; no lock.
- Check-before-write: if `current_size + len(line_bytes) > threshold`, rotate first,
  then write to a fresh file (so the threshold is never exceeded and no entry is
  split or dropped).
- Rotation = (1) unlink `egress.jsonl.1` if present; (2) `os.rename`/`os.replace`
  `egress.jsonl` → `egress.jsonl.1`; (3) open a fresh `egress.jsonl` (`0600`).
- Track size in memory (seed from `os.path.getsize` at open), reset on rotation —
  avoids a `stat` per entry.
- Threshold is a constant (500 MB) but should be injectable so the rotation test can
  set a tiny value (mirrors the integration test's policy-as-fixture pattern).

### Testing conventions to follow

- **Pure logic (entry build + redaction) → golden + table tests** with `--update`
  (mirrors `test_responses.py` / `conftest.py`'s `update_golden`). A golden
  `egress_entry.json.golden` (or `.jsonl.golden`) pins the schema (AC step 1).
- **Writer mechanics (mode, rotation) → unit tests** using `tmp_path`: assert
  `oct(os.stat(p).st_mode & 0o777) == 0o600`; write past a tiny threshold and assert
  `egress.jsonl.1` exists, current is fresh, and the union of both files preserves
  every entry (AC steps 2–3).
- **Addon hooks → `test_enforcer.py`-style** tests with `taddons.context`, asserting
  an entry is written for each of the four scenarios and that redacted headers are
  absent (AC step 1).
- **Integration → `test_integration.py`** (`@integration`): drive the live proxy with
  curl, then assert entries appear and a passthrough request logs host-only (AC step
  4). The integration test must set the new `creance_audit_log` option.
- mitmproxy is never imported by the pure module; the addon-hook test
  `importorskip("mitmproxy")`s, matching `test_enforcer.py`.

## Code references

- `internal/proxy/enforcer/enforcer.py:27` — audit logging deferred to AC-0018.
- `internal/proxy/enforcer/enforcer.py:61-72` — `creance_policy` option pattern to mirror.
- `internal/proxy/enforcer/enforcer.py:129-176` — the three hooks + the four scenarios.
- `internal/proxy/enforcer/policy.py:112-133` — `MatchedRule`/`Result` (matched-rule field source).
- `internal/proxy/enforcer/responses.py:55-60` — deterministic-JSON `_encode` convention to reuse.
- `internal/proxy/enforcer/conftest.py:20-47` — `--update` golden flag + fixtures.
- `internal/proxy/enforcer/test_responses.py:27-44` — golden assert/update helper pattern.
- `internal/proxy/enforcer/test_integration.py:74-130` — live-proxy fixture (extend for audit).
- `internal/state/state.go:40,164` — `egress.jsonl` name + `EgressJSONL()` path.
- `internal/state/state_test.go:215-246` — existing C4 path-rooting test.
- `Makefile:62-79` — enforcer venv + test targets.

## Open questions (for the planning checkpoint)

1. **Headers in the entry + redaction scope.** The design's field list (ts, method,
   URL, decision, rule, status) omits headers, yet the redaction AC + test
   ("redacted headers are absent") implies a redacted request-headers map *is*
   logged. Decision needed: (a) log a redacted request-headers map (redact the
   documented set; what superset beyond `Authorization`/`Cookie`/`X-Api-Key`?), vs
   (b) log only the design's listed fields and satisfy "redaction" by scrubbing
   sensitive query-string parameters from the logged URL, vs (c) both. Also: redact
   = replace with a sentinel (e.g. `"REDACTED"`) or drop the key entirely?
2. **Disabled when option unset.** Confirm that audit logging is a no-op when
   `creance_audit_log` is empty (matches the `creance_policy` default-"" pattern and
   keeps unrelated unit tests untouched) — assumed yes unless told otherwise.
