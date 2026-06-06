---
date: 2026-06-06
ticket: AC-0017
title: "Plan — enforcer.py decision engine (WP-3.1)"
status: ready
branch: main
research: thoughts/shared/research/2026-06-06-AC-0017-enforcer-decision-engine.md
tags: [plan, enforcer, mitmproxy, policy, python, parity, WP-3.1]
---

# Implementation Plan: AC-0017 — `enforcer.py` decision engine (WP-3.1)

## Overview

Build `internal/proxy/enforcer/` — the Python mitmproxy addon that enforces the
compiled `policy.json` at runtime. It must return the three response types
(allow / soft-deny 403 / hard-deny 403) with the documented JSON bodies and
`X-Cage-Reason` header, implement per-host `mode` (incl. `passthrough` CONNECT
tunneling without TLS termination), and hot-reload on `policy.json` mtime change —
all **provably consistent** with the Go matcher (`internal/policy`) by replaying
the shared decision-vector corpus from the Python side (cross-cutting C1).

The decision algorithm already exists in Go and is the reference implementation;
this ticket ports it to Python without forking the logic — the single source of
truth for `(ruleset, request) → {decision, mode, matched_rule}` remains the
corpus at `internal/policy/testdata/decision-vectors/`, consumed by **both** sides.

## Current state

- No `internal/proxy/` directory; **no Python anywhere in the repo**. This ticket
  creates the first `.py` files and the first non-Go test runner.
- Go reference matcher: `internal/policy/match.go`, `glob.go`, `policy.go:21-44`
  (authoritative prose contract). Corpus: 18 vectors at
  `internal/policy/testdata/decision-vectors/`, already consumed by
  `internal/policy/vectors_test.go` and `internal/policy/render/render_vectors_test.go`.
- The three 403 wire bodies + `X-Cage-Reason` exist **only as prose** in
  `docs/design.md:249-284` — no code emits them yet. (The Go `render` package
  produces a *different*, operator-facing `explain` JSON and is **not** a template.)
- Gating spike **S1 (AC-0001) is Done** → `api.anthropic.com` = `intercept`;
  passthrough is therefore **optional/not load-bearing**, but still in scope.
- mitmproxy pinned to **12.0.1** (`internal/buildinfo/buildinfo.go:36`).
- `make test` = `go test -race ./...`; `make test-integration` =
  `+ -tags=integration`. No CI pipeline exists. Golden `-update` pattern at
  `internal/policy/render/render_test.go` + `Makefile:64-74`.

## Desired end state

- `internal/proxy/enforcer/enforcer.py` (addon) + `policy.py` (pure matcher +
  policy.json loader) + `responses.py` (403 body builders), plus a pytest suite.
- `make test-enforcer` creates a repo-local venv, installs `mitmproxy==12.0.1` +
  `pytest`, runs the Python suite green — including 100% of the shared corpus.
- The three 403 bodies are golden-tested byte-for-byte (Python `--update` mirror
  of the Go pattern).
- A Go-side anti-fork guard asserts the corpus is not duplicated, so "Go and
  Python consume the identical files" holds without a CI system.
- `docs/design.md` soft-deny copy updated to the agreed wording; design and
  goldens are consistent.
- Integration test (gated S1, `make test-integration`) starts a real mitmproxy
  with the addon and asserts allow / soft / hard / passthrough via `curl -x`.
- `make test` (Go), `go build ./...`, `make lint` all green; AC-0017 marked Done.

### Decisions locked at the planning checkpoint

1. **403 `how_to_proceed` copy** — no "route around" framing.
   - Soft-deny: *"Not on the project allowlist. Ignore this resource if you can
     find the needed information elsewhere or can work reliably without it. If you
     think the information is important and would contribute significantly to your
     success, prompt the user and ask them to add the resource to the allowlist."*
   - Hard-deny (unchanged from design): *"Permanently blocked. Do NOT ask the user
     to allow it. Do NOT retry. Find an alternative source."*
2. **Test tooling** — `pytest` in a repo-local venv (`.venv-enforcer/`),
   `mitmproxy==12.0.1`, driven by `make test-enforcer`.
3. **Default workflow** — `make test` and the pre-commit hook stay Go-only;
   `make test-enforcer` is a separate, explicit target.
4. **CI** — deferred. Parity guaranteed by (a) the Python suite reading the
   canonical `internal/policy/testdata/decision-vectors/` directly and (b) a Go
   anti-fork guard test. A real CI workflow is a flagged follow-up.

## What we are NOT doing (out of scope — separate tickets)

- **AC-0018 (WP-3.2)** audit JSONL writer (same `enforcer.py` file, later).
- **AC-0019 (WP-3.3)** `go:embed` + extraction to the state dir.
- **AC-0020 (WP-3.4)** lock file / port allocation / refcount / teardown.

We do **not** wire policy.json discovery into the CLI/lifecycle; the addon takes
the policy path as a mitmproxy option (`--set creance_policy=<path>`), which
AC-0020 will supply.

---

## Module layout

```
internal/proxy/enforcer/
  enforcer.py            # mitmproxy addon: hooks, policy load, mtime poll
  policy.py              # PURE: Rule/RuleSet/Request, decide(), glob — no mitmproxy import
  responses.py          # PURE: build the three 403 JSON bodies + header values
  conftest.py            # pytest --update flag, repo-root/corpus path fixtures
  test_vectors.py        # replays internal/policy/testdata/decision-vectors/ (no mitmproxy)
  test_policy.py         # focused matcher unit tests (host/path glob edge cases)
  test_responses.py      # golden tests for the 403 bodies (--update mirror)
  test_enforcer.py       # addon hook tests via taddons/tflow/tutils (needs mitmproxy)
  test_integration.py    # real mitmdump + curl; marked, gated S1
  testdata/
    soft_deny_body.json.golden
    hard_deny_body.json.golden
  requirements.txt       # mitmproxy==12.0.1, pytest  (pinned)
```

Rationale: `policy.py`/`responses.py` are import-clean of mitmproxy so the corpus
and golden tests run without it; only the addon + addon tests import mitmproxy.
(AC-0019 will embed the directory; flagged there.)

---

## Phase 1 — Python scaffold, pure matcher, corpus parity green

### Changes

1. **`policy.py`** — faithful port of the Go matcher. Implements:
   - Dataclasses/dicts for `Rule` (`host`, `paths`, `methods`, `mode`, `reason`;
     ignore `source`/`lower_trust`), `RuleSet` (`allow`, `deny_always`),
     `Request` (`host`, `path`, `method`), `MatchedRule` (`list`, `index`),
     `Result` (`decision`, `mode`, `matched_rule`).
   - **Critical nil-vs-empty distinction:** absent `paths`/`methods` (key missing
     or JSON `null`) → `None` ("any" / host-wide); present `[]` → matches nothing.
     Decode preserving this (`dict.get("paths")` returns `None` when absent).
   - `decide(ruleset, request)` exactly per Go `match.go:15-42`:
     best allow → `is_passthrough = best_allow.mode == "passthrough"` → deny
     eligibility (`paths is None` only when passthrough) → best deny → hard-deny
     else allow else soft-deny (`mode=""`, `matched_rule=None`).
   - `best_match` + `_more_specific` reproducing the 6-field tuple
     `(host_rank, path_scoped, literal_segs, total_segs, method_scoped, canonical)`
     — first five larger-wins, `canonical` **smaller-wins**, ties keep earlier index.
     `canonical = host + "|" + ",".join(paths or []) + "|" + ",".join(methods or []) + "|" + (mode or "")`.
   - `match_host` (`*` / `*.suffix` incl. leading dot, apex miss; case-insensitive),
     `match_path` (trim slashes → split; prefix-by-default; whole-segment `**`
     crosses `/`; `*` within segment; `?` literal), `match_method` (verbatim
     membership), `match_segment_glob` (two-pointer backtracking, `*` only).
   - `decision`/`mode` string constants matching Go (`allow`/`soft-deny`/`hard-deny`,
     `intercept`/`passthrough`).
   - `load_policy(path)` → `RuleSet` from `policy.json` (`{version, allow,
     deny_always}`; ignore `input_hash`; tolerate missing lists as `[]`).
2. **`conftest.py`** — `--update` pytest option (mirrors the Go `-update` golden
   flag), plus fixtures resolving the repo root and the canonical corpus dir
   (`<repo>/internal/policy/testdata/decision-vectors`, computed from `__file__`).
3. **`test_vectors.py`** — load every `*.json` in the canonical corpus dir,
   strict-decode (reject unknown top-level keys, mirroring `DisallowUnknownFields`),
   run `decide()`, assert `decision`/`mode`/`matched_rule` equal `expected`.
   **Fail if the corpus is empty** (mirror `vectors_test.go:64`). One parametrized
   case per file so failures name the vector.
4. **`test_policy.py`** — focused unit tests for the glob/host edge cases the
   corpus may under-cover (e.g. `a**b` not crossing `/`, `?` literal, `/v1` vs
   `/v1/`, suffix apex miss), ported from `match_test.go`.
5. **`requirements.txt`** — `mitmproxy==12.0.1`, `pytest` (version-pinned).
6. **`Makefile`** — add target (doc-commented so `make help` lists it):
   ```makefile
   ## test-enforcer: Python mitmproxy addon tests (repo-local venv; pinned mitmproxy)
   .PHONY: test-enforcer
   test-enforcer:
   	python3 -m venv .venv-enforcer
   	.venv-enforcer/bin/pip install -q -r internal/proxy/enforcer/requirements.txt
   	.venv-enforcer/bin/pytest -q internal/proxy/enforcer -m "not integration"
   ```
   Add `.venv-enforcer/` to `.gitignore`.
7. **Go anti-fork guard** — `internal/policy/corpus_parity_test.go` (or extend
   `vectors_test.go`): walk the module root and assert exactly one directory named
   `decision-vectors` exists, so the Python side cannot silently fork the corpus.
   (CI-free substitute for the ticket's "both consume identical files" step.)

### Success criteria

**Automated**
- [ ] `make test-enforcer` passes; `test_vectors.py` runs **all 18** vectors green
      and fails if the corpus dir is empty.
- [ ] `make test` (Go) green, incl. the new anti-fork guard test.
- [ ] `go build ./...` and `make lint` green (Go unaffected).

**Manual**
- [ ] Temporarily break one matcher branch in `policy.py` → the corresponding
      vector fails (proves the corpus actually binds the Python logic). Revert.

---

## Phase 2 — 403 response bodies + goldens + design.md sync

### Changes

1. **`responses.py`** — pure builders returning `(status, headers, body_bytes)`:
   - `soft_deny(url, host, path, method)` → 403, `X-Cage-Reason: soft-deny`,
     body keys in order: `error` (`agent_cage_not_allowlisted`), `url`, `host`,
     `path`, `method`, `how_to_proceed` (the agreed copy), `allow_command_suggestion`
     (`agent-creance allow '<host><path>'`).
   - `hard_deny(url, reason)` → 403, `X-Cage-Reason: hard-deny`, body keys in
     order: `error` (`agent_cage_hard_deny`), `url`, `reason` (verbatim from the
     matched `deny_always` rule), `how_to_proceed` (design copy).
   - JSON encoding: `json.dumps(..., indent=2)` + trailing `\n`, key order via an
     ordered dict (insertion order), `ensure_ascii=False`. (Document that the wire
     bodies are independent of the Go `render` serializer; we choose 2-space indent
     to match the project's operator-JSON convention.)
   - `Content-Type: application/json` header on both.
2. **`test_responses.py`** — golden tests: build each body with fixed inputs,
   compare byte-for-byte to `testdata/*.json.golden`; write goldens when `--update`
   is passed. Also assert the `X-Cage-Reason` header value and status 403.
3. **Generate goldens** — run the suite with `--update`; review the two files.
4. **`docs/design.md`** — replace the truncated soft-deny `how_to_proceed`
   (`design.md:264`) with the agreed copy; align the surrounding prose
   (`design.md:269`) so it no longer says "route around silently" — reflect
   "ignore if alternatives exist / work without it; otherwise prompt the user."
   Leave the hard-deny body/prose unchanged. Keep the JSON examples in sync with
   the goldens (same field order, same strings).

### Success criteria

**Automated**
- [ ] `make test-enforcer` green incl. `test_responses.py`.
- [ ] Goldens are byte-stable on a second run without `--update`.

**Manual**
- [ ] `docs/design.md` soft-deny example body matches `soft_deny_body.json.golden`
      field-for-field; prose no longer contradicts the new copy.

---

## Phase 3 — The mitmproxy addon (hooks, policy load, hot reload)

### Changes

1. **`enforcer.py`** — addon class `Enforcer` exporting `addons = [Enforcer()]`:
   - `load(loader)` — `add_option("creance_policy", str, "", "Path to compiled
     policy.json")`.
   - `configure(updated)` — when `creance_policy` set, load the policy
     (`policy.load_policy`) into `self._ruleset` and record its mtime.
   - `running()` — start `self._task = asyncio.create_task(self._poll())` (hold the
     reference). `_poll()` loops `await asyncio.sleep(1)`, compares
     `os.path.getmtime(path)`, reloads on change (atomic attribute swap). Tolerate
     `FileNotFoundError`.
   - `done()` — cancel `self._task`.
   - `request(flow)` — for **intercepted** hosts: build a `Request` from
     `pretty_host` / `path` / `method`, run `decide()`:
     - `allow` → return (forward upstream untouched).
     - `soft-deny` → `flow.response = http.Response.make(403, body, headers)` from
       `responses.soft_deny(pretty_url, host, path, method)`.
     - `hard-deny` → `responses.hard_deny(pretty_url, reason)` where `reason` is the
       matched `deny_always` rule's `reason` (look it up via `matched_rule.index`).
   - `http_connect(flow)` — enforce host-level deny for **passthrough** hosts (which
     can't be TLS-terminated): if the host is passthrough-allowed **and** a
     host-level `deny_always` (`paths is None`) matches → return a 403 hard-deny on
     the CONNECT response (refuses the tunnel; the body is still plain HTTP here).
     Intercepted hosts: let CONNECT proceed so TLS terminates and `request` applies
     full host+path+method rules (this is how soft-deny/hard-deny *bodies* reach the
     agent over HTTPS).
   - `tls_clienthello(data)` — if the SNI host is passthrough-allowed (and not
     host-level denied) → `data.ignore_connection = True` (tunnel, real upstream
     cert preserved). Otherwise do nothing (terminate normally).
   - **Connect-stage helper** `host_disposition(ruleset, host)` in `policy.py`:
     returns `(mode, host_level_denied)` for a host using **host-rank-only**
     specificity over rules whose host matches (we have no path at CONNECT).
     `mode == "passthrough"` iff the most host-specific matching allow is
     passthrough; `host_level_denied` iff a `paths is None` deny matches. Documented
     as the deliberate host-granularity rule (design.md:238-243): a host that needs
     path-level intercept is treated as intercept (TLS terminated). Mixed configs
     (e.g. `most_specific_mode`) resolve to intercept at the connect stage.
2. **`test_enforcer.py`** — addon hook tests with `taddons.context(addon)` +
   `tflow.tflow(req=tutils.treq(...))`:
   - allow host → `request` leaves `flow.response is None`.
   - soft-deny host → 403, `X-Cage-Reason: soft-deny`, body == golden.
   - hard-deny (host-level) → 403, `X-Cage-Reason: hard-deny`, body carries the
     rule's `reason`.
   - `http_connect` for a passthrough host with a host-level deny → 403.
   - `http_connect` for an intercepted (even non-allowlisted) host → not refused
     (so soft-deny can be returned post-termination).
   - `tls_clienthello` for a passthrough-allowed host → `data.ignore_connection`;
     for an intercept host → not set. (Construct `tls.ClientHelloData`/`ClientHello`
     per mitmproxy's test patterns.)
   - hot reload: write a policy file, `configure`, then rewrite it with a new mtime
     and invoke `_poll` once (or call the reload path directly) → a previously
     soft-denied host now allows.

### Success criteria

**Automated**
- [ ] `make test-enforcer` green incl. `test_enforcer.py` (all hook paths + reload).
- [ ] `make lint` / `go build ./...` / `make test` (Go) still green.

**Manual**
- [ ] `mitmdump -s internal/proxy/enforcer/enforcer.py --set creance_policy=<fixture>`
      starts without import/addon errors (smoke).

---

## Phase 4 — Integration test (real mitmproxy + curl), gated S1

### Changes

1. **`test_integration.py`** — `@pytest.mark.integration`. Starts
   `.venv-enforcer/bin/mitmdump -p 0 -s enforcer.py --set creance_policy=<fixture
   policy.json>` as a subprocess, discovers the chosen port, trusts the generated
   CA, then via `curl -x http://127.0.0.1:<port>`:
   - allowed host → upstream response, no `X-Cage-Reason`.
   - soft-denied host → 403 + `X-Cage-Reason: soft-deny` + JSON body.
   - hard-denied host → 403 + `X-Cage-Reason: hard-deny` + reason.
   - passthrough host → tunnels; TLS validates against the **real** upstream cert
     (not mitmproxy's CA).
   - hot reload: rewrite the fixture policy mid-run → a previously denied host
     becomes allowed within ~1s without restart.
   - Skip with a clear message if `curl`/network/CA prerequisites are unavailable
     (mirror the Go integration skip convention).
2. **`Makefile`** — add `## test-enforcer-integration:` (venv + `pytest -m
   integration`) **and** fold it into `make test-integration` so the ticket's
   "run via make test-integration" wording holds:
   ```makefile
   test-integration: <existing go step>
   	$(MAKE) test-enforcer-integration
   ```
   (Integration is the slow/explicit target, so adding the Python dep there is
   acceptable — unlike the fast `make test`.)

### Success criteria

**Automated**
- [ ] `make test-enforcer-integration` (and `make test-integration`) pass locally
      with mitmproxy 12.0.1, or skip cleanly when prerequisites are absent.

**Manual**
- [ ] Observe the four `curl -x` cases by hand once; confirm the passthrough host's
      cert chain is the real upstream's, not mitmproxy's CA.

---

## Phase 5 — Final verification + close ticket

### Changes

1. Run the full battery: `go build ./...`, `make test`, `make lint`,
   `make test-enforcer`, and `make test-integration` (the last may skip live bits).
2. Re-check every AC-0017 acceptance criterion against the implementation.
3. **Flag follow-ups** in the ticket Notes: real CI workflow (deferred here);
   AC-0019 will embed the `internal/proxy/enforcer/` directory (note the multi-file
   layout); the incidental `mcp-proxy.anthropic.com` baseline host from the S1 note.
4. Mark **AC-0017 Done** in `thoughts/shared/tickets/AC-0017-enforcer-decision-engine.md`.

### Success criteria

**Automated**
- [ ] `go build ./...` clean.
- [ ] `make test` (race) green.
- [ ] `make lint` green.
- [ ] `make test-enforcer` green (18/18 vectors, goldens, addon hooks).
- [ ] `make test-integration` green or cleanly skipped.

**Manual**
- [ ] All AC-0017 acceptance criteria ticked.
- [ ] Ticket marked Done; follow-ups recorded.

---

## Testing strategy summary

- **Parity (C1):** `test_vectors.py` replays the canonical corpus (no fork; Go
  anti-fork guard enforces single source). Any matcher change requires corpus
  updates on the Go side, which the Python side then re-validates.
- **Golden bodies:** `test_responses.py` with a `--update` mirror of the Go pattern.
- **Addon behavior:** `test_enforcer.py` via mitmproxy's `taddons`/`tflow`/`tutils`.
- **End-to-end:** `test_integration.py` (real mitmdump + curl), gated S1, under
  `make test-integration`.
- **Pure-logic isolation:** `policy.py`/`responses.py` import no mitmproxy, so the
  parity and golden suites run without it; only addon tests need the venv's mitmproxy.

## Risks / notes

- **Passthrough at the connect stage** is the subtlest part (no path/method
  visible). The chosen rule — host-granularity, intercept-on-ambiguity — is
  consistent with `design.md:238-243` and is **not load-bearing** (S1 → intercept).
  The request-level matcher remains the corpus-bound source of truth; the connect
  helper is covered by dedicated addon tests, not the corpus.
- **`url` field** reconstructed via `flow.request.pretty_url`; design examples have
  no query string — preserve query if present (it's part of `path`/`pretty_url`).
- **Embedding (AC-0019):** the multi-file package is a deliberate testability
  choice; AC-0019 embeds the directory (trivial vs a single file). Flagged.
