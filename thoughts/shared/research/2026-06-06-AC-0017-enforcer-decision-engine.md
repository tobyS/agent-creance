---
date: 2026-06-06
ticket: AC-0017
title: "Research — enforcer.py decision engine (WP-3.1)"
status: complete
branch: main
commit: dfb0b33ade30559314722ddca45c30e1e4e1093e
tags: [research, enforcer, mitmproxy, policy, python, parity, WP-3.1]
---

# Research: AC-0017 — `enforcer.py` decision engine (WP-3.1)

## Research question

What does it take to implement `internal/proxy/enforcer/enforcer.py` — a mitmproxy
addon that enforces the compiled `policy.json` with the three response types
(allow / soft-deny 403 / hard-deny 403), per-host `mode` (incl. `passthrough`
CONNECT tunneling), and mtime hot reload — *provably consistent* with the Go
matcher (AC-0010) via the shared decision-vector corpus (cross-cutting C1)?

## Summary / TL;DR

- This is **greenfield**: there is no `internal/proxy/` directory and **no Python
  anywhere in the repo**. AC-0017 creates the first `.py` file, the first non-Go
  test runner, and (per the ticket) the first CI parity step.
- The **decision logic already exists in Go** (`internal/policy/match.go` +
  `policy.go`) and is the reference implementation. The Python addon must
  reproduce it **byte-for-byte**, validated by replaying the **18-vector corpus**
  at `internal/policy/testdata/decision-vectors/` from the Python side. "Do not
  fork the logic" = both languages are driven by the *same* JSON files.
- The **three 403 bodies + `X-Cage-Reason` header do not exist in any code yet** —
  they are specified only in prose at `docs/design.md:249-284`. The Python addon
  is the **first and only** implementation; the golden fixtures for them must be
  created here (the Go `render` package produces a *different*, operator-facing
  JSON shape and is **not** a template for the wire 403 bodies).
- The gating spike **S1 (AC-0001) is Done** — decision `intercept`, nothing pins.
  So the `passthrough` path is **optional, not load-bearing**, but it is still in
  scope (two corpus vectors exercise it and AC #2 requires it).
- mitmproxy is pinned to **12.0.1** (`internal/buildinfo/buildinfo.go:36`). Modern
  hooks: `request` (synthetic 403), `http_connect` (deny-at-CONNECT 403),
  `tls_clienthello` + `data.ignore_connection` (cert-preserving passthrough),
  `running()` + `asyncio.create_task` (mtime poll). The old `next_layer`/`TCPLayer`
  passthrough pattern is **deprecated** — do not use it.
- Scope is **only** decision + response emission + modes + hot reload. Audit
  logging (AC-0018), Go embed/extract (AC-0019), and lifecycle/lock (AC-0020) are
  separate tickets — even though AC-0018 lands in the *same* `enforcer.py` file.

## The exact decision algorithm to reproduce (the C1 contract)

Reference: `internal/policy/match.go:15-90`, `internal/policy/glob.go`,
`internal/policy/policy.go:21-44` (authoritative prose contract). The matcher is a
pure function `RuleSet.Decide(Request) -> Result`. **There is no "default
decision" field — soft-deny is the fall-through.**

### `policy.json` shape (what the addon reads)

`Compiled` (`policy.go:98-102`) serializes flat (embeds `RuleSet`):

```json
{ "version": 1, "input_hash": "…", "allow": [ <Rule>… ], "deny_always": [ <Rule>… ] }
```

- `version` — `CompiledVersion = 1` (`policy.go:91`), the cross-language contract.
- `input_hash` — **the enforcer ignores it** (`policy.go:96-97`).
- `Rule` (`policy.go:72-80`): `host` (required string), `paths` ([]string,
  omitempty), `methods` ([]string, omitempty), `mode` (omitempty), `reason`
  (omitempty), `source`/`lower_trust` (**ignored by Decide**, `policy.go:70-71`).
  - **`paths` nil/absent ≠ empty `[]`.** nil = "host-wide, matches any path";
    present list = "matches if ANY pattern matches"; empty `[]` = matches nothing.
    This nil-vs-present distinction also drives the passthrough blind spot.
  - **`methods` nil/absent** = any method; present = verbatim membership.

### `Decide` order of evaluation (`match.go:15-42`)

1. **Best allow**: `bestMatch(allow)` → most-specific matching allow rule.
   `isPassthrough = allowOK && allow[idx].Mode == "passthrough"`.
2. **Deny eligibility**: all deny rules eligible, **unless** `isPassthrough`, in
   which case only host-level denies (`r.Paths == nil`) are eligible — path-scoped
   denies are suppressed because the proxy never sees the path on a passthrough host.
3. **Best deny**: `bestMatch(deny_always, eligible)`. If any matches → **hard-deny**
   immediately (deny shadows allow), carrying that deny's `mode` and
   `matched_rule = {list:"deny_always", index}`.
4. Else if an allow matched → **allow** with that allow's mode + `{list:"allow", index}`.
5. Else → **soft-deny** with `mode = ""` and `matched_rule = null`.

### Most-specific selection — `bestMatch` / `moreSpecific`

Scan all rules in list order; a later rule replaces the current best only if
**strictly** more specific. Ties keep the **earlier index**. A rule matches iff
host AND method AND path all match (`ruleMatches`, `match.go:69-91`); for a
multi-path rule the **most specific matched pattern** is chosen and fed into
scoring.

Specificity tuple (`glob.go:117-176`), larger wins per field, first difference decides:

1. `hostRank` — exact `2` > `*.suffix` `1` > `*` `0`.
2. `pathScoped` — `1` if `r.Paths != nil` else `0`.
3. `literalSegs` — count of matched-pattern segments with no `*`.
4. `totalSegs` — number of matched-pattern segments.
5. `methodScoped` — `1` if `r.Methods != nil` else `0`.
6. `canonical` — **smaller string wins** (deterministic final tie-break):
   `host + "|" + ",".join(paths) + "|" + ",".join(methods) + "|" + mode`
   (uses the rule's full path/method lists, not the matched pattern).

### Host matching — `matchHost` (`glob.go:10-24`)

Case-insensitive (both sides lowercased). Three cases in order: `"*"` → any;
`"*.suffix"` → `host.endswith(pattern[1:])` (suffix **includes the leading dot**, so
apex `medium.com` does NOT match `*.medium.com`, and `amedium.com` does NOT match);
else exact equality.

### Path matching — `glob.go:26-97`

- **Normalize** (`splitPath`): trim all leading/trailing `/`, split on `/`. Root
  `"/"` → no segments. So `/v1` and `/v1/` are identical.
- **Prefix-by-default** (`matchSegments`): when the pattern is exhausted →
  **match** regardless of remaining path (`/repos/org/repo/` covers
  `/repos/org/repo/blob/main`).
- **`**`** is the only token that crosses `/`; special **only as a whole segment**
  (glued `a**b` degrades to `a*b` — no slash crossing).
- **`*`** matches any run within one segment; **`?` is literal** (no single-char
  wildcard); no other metacharacters.

### Decision-vector corpus (the parity guardrail)

18 files at `internal/policy/testdata/decision-vectors/`. Schema (Go loader
`vectors_test.go:18-29`, strict `DisallowUnknownFields`):

```json
{
  "name": "<string>",
  "ruleset": { "allow": [<Rule>…], "deny_always": [<Rule>…] },
  "request": { "host": "…", "path": "…", "method": "…" },
  "expected": {
    "decision": "allow|soft-deny|hard-deny",
    "mode": "intercept|passthrough|\"\"",
    "matched_rule": { "list": "allow|deny_always", "index": <int> } | null
  }
}
```

The Go consumers locate the dir at `internal/policy/testdata/decision-vectors/`
(`vectors_test.go:36`) and `../testdata/decision-vectors` from the render package
(`render_vectors_test.go:37`). The suite **fails if the corpus is empty**
(`vectors_test.go:64`) — the Python suite must apply the same "run every file"
discipline. Coverage spans every host mode, all path semantics, all three
decisions, and the two passthrough edge cases:
`passthrough_host_deny_enforced.json` (host-level deny → hard-deny) and
`passthrough_suppresses_path_deny.json` (path deny suppressed → allow/passthrough).

**Python reproduction checklist** (from the agent analysis):
- nil vs `[]` for `paths`/`methods`; lowercase host+pattern before matching.
- Path: trim slashes, split, root→no segments; `**` whole-segment only; `*` within
  segment; `?` literal; prefix-by-default.
- Selection: 6-field tuple, first five larger-wins, `canonical` smaller-wins, ties
  keep earlier index.
- `Decide` order: best allow → derive passthrough → filter denies → best deny →
  hard-deny / allow / soft-deny. soft-deny = `mode:""`, `matched_rule:null`.

## The three wire responses (NOT yet implemented anywhere)

Authoritative spec: `docs/design.md:249-284`. **No Go code emits `X-Cage-Reason`,
`403`, or `agent_cage_*`** — the Python addon is the sole implementation, and its
golden fixtures must be authored in this ticket.

**Allowed** — forward upstream unchanged; **no header**, no body injection.

**Soft-deny** — HTTP 403, header `X-Cage-Reason: soft-deny`, body:

```json
{
  "error": "agent_cage_not_allowlisted",
  "url": "https://docs.somelib.io/v2/auth/",
  "host": "docs.somelib.io",
  "path": "/v2/auth/",
  "method": "GET",
  "how_to_proceed": "Not on the project allowlist. If this is genuinely important...",
  "allow_command_suggestion": "agent-creance allow 'docs.somelib.io/v2/auth/'"
}
```

Field order: `error` (literal `agent_cage_not_allowlisted`), `url`, `host`, `path`,
`method`, `how_to_proceed`, `allow_command_suggestion`. The suggestion is
`agent-creance allow '<host><path>'` (single-quoted host+path).

**Hard-deny** — HTTP 403, header `X-Cage-Reason: hard-deny`, body:

```json
{
  "error": "agent_cage_hard_deny",
  "url": "https://w3schools.com/...",
  "reason": "Known low-quality source. Use MDN or official docs instead.",
  "how_to_proceed": "Permanently blocked. Do NOT ask the user to allow it. Do NOT retry. Find an alternative source."
}
```

Field order: `error` (literal `agent_cage_hard_deny`), `url`, `reason` (verbatim
from the matched `deny_always` rule's `reason` field), `how_to_proceed`.

**Load-bearing literals** = the two `error` enum strings + the two `X-Cage-Reason`
values (the skill activates on the `agent_cage_` prefix / the header,
`design.md:284`). The `how_to_proceed` text in the design doc is **truncated with
`...`** for soft-deny and uses a placeholder URL for hard-deny — so the *exact*
final copy is an open decision (see Open Questions) that gets pinned into the
golden fixtures here.

**The Go `render` package is not a template.** `render.ExplainJSON`
(`render.go:157-188`) produces an operator-facing `{request, decision, mode,
matched}` shape for `policy explain --json` — a *different* structure from the wire
403 body. Don't model the Python bodies on it. (Render does establish the project's
JSON convention if we want to mirror it: `json.MarshalIndent(v, "", "  ")` +
trailing `\n`, source-order keys, Go default HTML escaping.)

## Per-host modes (intercept vs passthrough)

Design: `docs/design.md:211-247`. Mode is a per-rule field; the **effective mode is
the most-specific matching allow's mode** (`most_specific_mode.json`: a path-scoped
`intercept` beats a host-wide `passthrough`).

- **intercept** (default): TLS-terminated, matched on host+path+method.
- **passthrough**: raw CONNECT tunnel, **no TLS termination** — the agent's client
  validates the *real upstream cert*, not mitmproxy's CA. **Host-granularity only**:
  `paths`/`methods` on a passthrough rule are rejected at compile time (compiler's
  job, not the enforcer's). Host allow/deny is still decided at CONNECT, so a
  **host-level `deny_always` still refuses the tunnel**; **path-scoped denies cannot
  apply** (blind spot). A passthrough host is **always an allow**, never a 403.

S1 (AC-0001) resolved `api.anthropic.com → intercept`, so passthrough is optional
in practice — but still implemented and tested via the corpus + integration probe.

## mitmproxy 12.0.1 addon API (how to wire it)

Web research against docs.mitmproxy.org + mitmproxy `main` (stable across 10.x/11.x/12.x):

- **Synthetic 403** in `def request(flow)`: `flow.response =
  http.Response.make(403, body_bytes, {"Content-Type": "application/json",
  "X-Cage-Reason": "soft-deny"})`. Setting `flow.response` short-circuits upstream.
- **Deny at CONNECT** in `def http_connect(flow)`: set a non-2xx `flow.response`
  (returns the error and aborts the connection). Cleaner than `flow.kill()`. At
  this stage you have the CONNECT authority (`flow.request.host`/`.port`) but not
  yet SNI.
- **Cert-preserving passthrough** in `def tls_clienthello(data)`: read
  `data.client_hello.sni`; if the host should be passthrough, set
  `data.ignore_connection = True`. **The old `next_layer`/`TCPLayer` pattern is
  deprecated** (mitmproxy #4567) — do not use it.
- **Request fields** in `request`: `flow.request.pretty_host` (preferred for
  allowlist — prefers Host/authority header over a bare IP), `flow.request.path`
  (includes query), `flow.request.method`. Cross-check `pretty_host` against SNI
  for HTTPS since the client controls the Host header.
- **mtime hot reload**: start an `asyncio.create_task(self._poll_loop())` in
  `def running()` (hold the task reference or it's GC'd); cancel in `def done()`.
  Poll `os.path.getmtime(policy_path)` every ~1-2s and reload on change. Do **not**
  poll inside `request`.
- **Testing addons**: instantiate the addon and call hooks directly; build flows
  with `mitmproxy.test.tflow.tflow(req=tutils.treq(host=…, port=…, method=…,
  path=…))` inside `taddons.context(addon)`; assert `flow.response.status_code` /
  `.headers["X-Cage-Reason"]` / `.content`. For passthrough, construct
  `tls.ClientHelloData` and assert `data.ignore_connection`.

## Build / test / CI wiring (all net-new)

- `make test` = `go test -race ./...`; `make test-integration` = `+ -tags=integration`
  (`Makefile:48-56`). External tools only invoked under `//go:build integration`
  (precedent: `internal/profile/live_integration_test.go`, skips when tool absent).
- **No CI pipeline exists** (no `.github/`, no `requirements.txt`/`pyproject.toml`,
  no virtualenv, no `.py`). The only gate is `.githooks/pre-commit` (Go-only:
  gofmt, vet, `go test -race`). A `make test-enforcer` (pytest) target and a CI
  parity step are both anticipated by the ticket but must be created here.
- **Golden `-update` pattern** to mirror (`render_test.go:16,43-53`): a package-level
  `-update` flag with the literal description `"regenerate golden files"` (the
  `make golden` target greps for that string, `Makefile:64-74`), an `assertGolden`
  helper that writes on `-update` else compares, files under sibling `testdata/`.
  The Python suite should offer an equivalent `--update` for its 403 golden bodies.
- **mitmproxy pin**: `12.0.1` (`internal/buildinfo/buildinfo.go:36`); agent-safehouse
  `1.4.2`. Only mitmproxy version reference in the repo — the addon targets 12.0.1.
- `help` auto-discovers `## ` doc-commented targets, so `## test-enforcer:` shows up
  in `make help` for free.

## Scope boundaries (from the spec, `…spec.md:218-249`)

In scope (WP-3.1): policy.json loading, three response types, per-host modes incl.
passthrough, mtime hot reload, pass C1 vectors from Python, golden 403 bodies.

**Out of scope** (separate tickets, even when same file):
- **AC-0018 (WP-3.2)** — audit JSONL writer in `enforcer.py` (redaction, rotation).
- **AC-0019 (WP-3.3)** — `go:embed` + extraction to state dir on first run.
- **AC-0020 (WP-3.4)** — lock file, port allocation, refcount, teardown.

## Open questions for the planning checkpoint

1. **Exact `how_to_proceed` / placeholder copy.** The design doc truncates the
   soft-deny `how_to_proceed` with `...` and uses a placeholder hard-deny URL.
   These strings become load-bearing golden fixtures. Pin them now (proposal:
   expand to a complete, agent-actionable sentence consistent with `design.md:269,
   282`), or keep the design doc's literal partial text? Whatever we choose is what
   the goldens enforce.
2. **Test runner & layout.** Confirm `pytest` (vs stdlib `unittest`) and where the
   Python tests live (`internal/proxy/enforcer/` alongside `enforcer.py`, e.g.
   `test_enforcer.py` + `test_vectors.py`). And how `make test-enforcer` provisions
   mitmproxy — a repo-local venv, or assume a system mitmproxy 12.0.1 on PATH?
   (The addon imports `mitmproxy.*`, so the test env needs it installed.)
3. **`make test` integration.** Should `make test-enforcer` be folded into the
   default `make test` / the pre-commit hook (making Python a hard local dep), or
   kept as a separate target invoked explicitly + in CI? (Repo is macOS-only; Go
   contributors may not have Python/mitmproxy set up.)
4. **CI bootstrap.** The ticket's verification step 2 wants a CI step asserting Go
   and Python consume the identical corpus files. There is no CI at all today —
   does AC-0017 introduce the first CI workflow (e.g. GitHub Actions), or is "CI"
   satisfied for now by a Makefile target + the pre-commit hook, deferring real CI?
5. **`url` reconstruction for the 403 body.** In `request`, reconstruct the `url`
   as `flow.request.pretty_url` (scheme://host/path) — confirm that matches the
   design's `https://host/path` form, including how query strings are represented
   (the design examples have no query).

## Key references

- Ticket: `thoughts/shared/tickets/AC-0017-enforcer-decision-engine.md`
- Spec: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`
  (C1 §94-101, component table §75-88, Phase 3 WPs §218-249, S1 gate §121, §393)
- S1 spike (resolved `intercept`): `thoughts/shared/tickets/AC-0001-spike-ca-trust.md`,
  `thoughts/shared/research/2026-06-04-s1-ca-trust.md`
- Go matcher (reference impl): `internal/policy/match.go`, `internal/policy/glob.go`,
  `internal/policy/policy.go:21-44`
- Corpus: `internal/policy/testdata/decision-vectors/` (18 vectors),
  `internal/policy/vectors_test.go`, `internal/policy/render/render_vectors_test.go`
- Wire responses spec: `docs/design.md:249-284`; modes: `docs/design.md:211-247`;
  policy.json/embed: `docs/design.md:286-299`
- Render (operator JSON, NOT the wire body): `internal/policy/render/render.go`
- Golden `-update` pattern: `internal/policy/render/render_test.go`; `Makefile:64-74`
- mitmproxy pin: `internal/buildinfo/buildinfo.go:36` (12.0.1)
- mitmproxy API: https://docs.mitmproxy.org/stable/api/events.html ,
  https://docs.mitmproxy.org/stable/api/mitmproxy/http.html ,
  examples `http-reply-from-proxy.py`, `tls_passthrough.py` (contrib, current)
