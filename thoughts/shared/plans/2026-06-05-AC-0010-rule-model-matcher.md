---
date: 2026-06-05
ticket: AC-0010
title: "Plan: Rule model & matcher + decision-vector corpus (WP-2.1)"
status: ready
research: thoughts/shared/research/2026-06-05-AC-0010-rule-model-matcher.md
tags: [plan, policy, matcher, decision-vectors, C1, WP-2.1]
git_commit: cf00cc4
branch: main
---

# Plan: AC-0010 — Rule model & matcher + decision-vector corpus (WP-2.1)

## Overview

Create `internal/policy`: a pure, language-neutral egress matcher that, given an
in-memory rule set and a request `(host, path, method)`, returns the decision
(`allow` / `soft-deny` / `hard-deny`), the carried `mode`, and the matched rule.
Ship it alongside the `testdata/decision-vectors/` JSON corpus — the single
guardrail (cross-cutting **C1**) that the future Python `enforcer.py` (AC-0017)
must also satisfy. The matcher is small and pure; the value is in the *exact,
documented* semantics and the corpus that pins them.

## Decisions (resolved at the question checkpoint)

1. **Path semantics = prefix-by-default.** A path pattern matches a **prefix** of
   the request path's segments: once all pattern segments are consumed, any
   remaining request segments are "under" the prefix and still match. A trailing
   `/` on a pattern is just normalized away (prefix semantics already covers the
   subtree). `*` matches a run of non-`/` within one segment; `**` (whole segment)
   matches zero-or-more segments. This satisfies every design example
   (`/repos/org/repo/` covers its subtree; `/@*` covers `/@user/article`;
   `**/.env` matches `.env` at root and at any depth).
2. **Most-specific-wins tie-break.** When several rules in the same list match, the
   reported rule and carried `mode` come from the most specific one, by the
   ordering `host (exact > *.suffix > *)`, then `path (constrained > host-wide;
   more literal segments > fewer; more total segments > fewer)`, then `method
   (constrained > any)`, with a deterministic canonical-string fallback for exact
   ties. Order-independent — required because `allow`/`deny_always` lists union
   additively across `include:` files.
3. **Matcher models the passthrough blind spot.** A *path-scoped* `deny_always` is
   suppressed when the request's most-specific matching `allow` is `passthrough`
   (the real proxy never sees the path at CONNECT time — `docs/design.md:242`). A
   *host-level* `deny_always` (no paths) still produces a hard-deny on a
   passthrough host (the tunnel is refused). Encoded in the corpus so Go `explain`
   and Python `enforcer.py` agree with runtime reality.
4. **Minor defaults** (recommended, applied): `*.medium.com` does **not** match the
   apex `medium.com`; HTTP methods compared verbatim against canonical uppercase
   (case-sensitive); hosts compared case-insensitively (lowercased); `?` has no
   special meaning (literal); glued `**` inside a segment (`a**b`) degrades to `*`
   semantics — **no pattern-validation/rejection pass in this ticket** (that is the
   compiler's job, AC-0013); the matcher simply treats only a whole-segment `**`
   as the cross-segment wildcard.

## Current state

- `internal/policy` does not exist.
- The rule model is `internal/config` (`Egress`, `Rule` with `*[]string`
  Paths/Methods, `Mode` always non-empty, mode constants) — done and validated
  (`internal/config/config.go:51-89`, `validate.go:20-43`).
- Test idioms exist (table tests with testify `require`; golden `-update`;
  fixtures under `testdata/`), but **no** directory-iterating fixture loader, **no**
  `encoding/json` usage anywhere, and **no** zero-fixtures guard. This ticket
  introduces all three.

## Desired end state

- `internal/policy` exposes:
  - `RuleSet{ Allow, DenyAlways []Rule }` and `Rule{ Host, Paths, Methods []string,
    Mode, Reason string }` (json-tagged, plain slices — decoupled from the yaml
    mirror), plus a `Request` and a `Result{ Decision, Mode string; Matched
    *MatchedRule }`.
  - `func (RuleSet) Decide(Request) Result` implementing the documented algorithm.
  - `func FromConfig(config.Egress) RuleSet` — the bridge so a later `policy
    explain` / compiler (AC-0013) can build a `RuleSet` from config. Small, tested.
- `internal/policy/testdata/decision-vectors/*.json` — the language-neutral corpus
  covering all AC cases.
- A corpus-driven table test that loads **every** `*.json` file, asserts
  `Decide` output equals `expected`, and **fails if zero vectors are found**.
- `go build ./...`, `make test`, `make lint` green; `jq '.'` over each vector
  exits 0.

## What we are NOT doing

- No Python implementation (AC-0017 reuses this corpus).
- No reading of a compiled `policy.json` from disk (the compiler is AC-0013); this
  ticket matches an in-memory `RuleSet`.
- No pattern-syntax validation/rejection pass (AC-0013) — the matcher degrades
  malformed `**` rather than erroring.
- No `policy show` / `policy explain` CLI wiring (WP-2.6).
- No new external module dependency — the glob matcher is hand-rolled (per research:
  cross-language parity is cheaper to guarantee with a small authored spec than by
  mirroring `bmatcuk/doublestar`'s edge cases in Python).

## Implementation approach

The matcher is pure (no `sysdep` seam needed — it touches no OS facility; the
*test* reads `testdata/` directly, which is allowed for tests). Two phases: the
matcher + unit tests first, then the corpus + corpus-driven test.

### Decision algorithm (the spec to implement and document in the package header)

Given `RuleSet` and `Request{Host, Path, Method}`:

1. **Find the most-specific matching `allow`** → `(allowRule, allowMode)` or none.
   `isPassthrough = matched && allowMode == passthrough`.
2. **Evaluate `deny_always`:**
   - Among matching deny rules, split host-level (Paths == nil) vs path-scoped.
   - If any **host-level** deny matches → **hard-deny** (report the most-specific
     such deny). Applies even on passthrough hosts.
   - Else if **not** `isPassthrough` and any **path-scoped** deny matches →
     **hard-deny** (report the most-specific such deny).
   - Else path-scoped denies are suppressed (passthrough) → fall through.
3. **If not hard-deny:** if an `allow` matched → **allow** (mode = `allowMode`,
   report `allowRule`); else → **soft-deny** (mode = `""`, no matched rule).

`mode` in the result = the matched rule's `Mode` (allow or deny); `""` for
soft-deny. `Matched` = `{List: "allow"|"deny_always", Index: N}` or `nil`.

### Matching primitives

- **Host** (`matchHost(pattern, host)`, both lowercased): `*` → any; `*.suffix`
  (pattern starts `*.`) → `strings.HasSuffix(host, pattern[1:])` (apex excluded
  naturally); else exact equality.
- **Path** (`matchPath(pattern, path)`): normalize each by trimming leading/trailing
  `/`, split on `/`, empty string → no segments. Then segment prefix-match:
  ```
  matchSegments(pat, path):
    if len(pat) == 0: return true            // prefix satisfied
    if pat[0] == "**":
        return matchSegments(pat[1:], path)              // ** consumes 0
            or (len(path) > 0 and matchSegments(pat, path[1:]))  // ** consumes 1+
    if len(path) == 0: return false
    return matchSegmentGlob(pat[0], path[0]) and matchSegments(pat[1:], path[1:])
  ```
  `matchSegmentGlob` is a classic single-segment wildcard match where `*` matches
  any (possibly empty) run of characters (no `/` can appear — segments are already
  split); everything else (including `?`) is literal. A rule's `Paths == nil`
  matches any path; a non-nil list matches if **any** pattern matches.
- **Method** (`matchMethod(methods, m)`): `nil` → any; else `m ∈ methods` (verbatim).

### Specificity (most-specific-wins)

`specificity(rule, matchedPattern)` → comparable tuple, higher wins:
`(hostRank, pathConstrained, literalSegs, totalSegs, methodConstrained)` where
`hostRank` = exact 2 / `*.suffix` 1 / `*` 0; `pathConstrained` = 0 if Paths nil
else 1; `literalSegs`/`totalSegs` from the matched pattern; `methodConstrained` =
0 if Methods nil else 1. Pick the max among matching rules; exact ties broken by a
canonical `host|paths|methods|mode` string (smallest wins) for determinism.

---

## Phase 1 — Core types, matcher, unit tests

### Changes

**`internal/policy/policy.go`** (package doc + types)
- Package header in the repo style (`// Package policy ...`): state that it is a
  pure language-neutral matcher, the three-outcome model, that `deny_always`
  shadows `allow`, the prefix/glob path spec, most-specific-wins, the passthrough
  blind-spot rule, and that the `testdata/decision-vectors/` corpus is the C1
  contract shared with Python (AC-0017). Cite `docs/design.md` and AC-0010.
- Types: `Rule` (json tags `host`/`paths`/`methods`/`mode`/`reason`, plain
  `[]string`), `RuleSet` (`allow`/`deny_always`), `Request{Host, Path, Method}`,
  decision constants (`DecisionAllow = "allow"`, `DecisionSoftDeny = "soft-deny"`,
  `DecisionHardDeny = "hard-deny"`), mode constants re-exported or referencing
  `config.ModeIntercept`/`ModePassthrough` (use string literals `"intercept"`/
  `"passthrough"` locally to keep `policy` decoupled from `config` types except via
  `FromConfig`), `MatchedRule{List string; Index int}`, `Result{Decision, Mode
  string; Matched *MatchedRule}`.
- `func FromConfig(e config.Egress) RuleSet` — convert, dereferencing the
  `*[]string` Paths/Methods (nil stays nil), copying Mode/Reason/Host. Small.

**`internal/policy/match.go`** (the matcher)
- `func (rs RuleSet) Decide(req Request) Result` — the algorithm above.
- Internal helpers: `bestMatch(rules []Rule, req Request, wantPathScoped *bool)`
  returning `(index int, matched bool)` selecting the most-specific matching rule
  (optionally filtered to host-level vs path-scoped for the deny split), and
  `ruleMatches(r Rule, req Request) (matchedPattern string, ok bool)`.

**`internal/policy/glob.go`** (matching primitives + specificity)
- `matchHost`, `matchPath` (+ `matchSegments`, `matchSegmentGlob`, `splitPath`),
  `matchMethod`, `specificity` / specificity comparison.

**`internal/policy/match_test.go`** (table-driven, testify `require`)
- Host table: exact hit/miss, `*.suffix` hit/apex-miss/multi-label, `*` any,
  case-insensitivity.
- Path/glob table: prefix coverage (`/repos/org/repo/` vs subtree and parent),
  `*` within-segment and non-crossing-`/`, `**` at root / at depth / consuming
  middle, bare `**` matches root, `/@*` covers subtree, normalization
  (leading/trailing slash), glued `**` degrades.
- Method table: nil = any, membership hit/miss, case-sensitivity.
- `Decide` scenarios (small inline RuleSets): allow, host-wide allow (nil paths),
  soft-deny default, host deny shadows allow, path-glob deny, most-specific mode
  selection (host-wide `intercept` + path-scoped `passthrough` on same host →
  reports most-specific), passthrough suppresses path-deny but not host-deny.
- `FromConfig` round-trip: a `config.Egress` (built via `config.Parse`) converts to
  the expected `RuleSet` (nil paths preserved, mode default present).

### Verification
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/policy/...` passes.
- [ ] `make lint` clean.

---

## Phase 2 — Decision-vector corpus + corpus-driven test

### Changes

**`internal/policy/testdata/decision-vectors/*.json`** — one file per vector (or a
small number of grouped files; one-per-case is clearest for failure messages).
Each file:
```json
{
  "name": "hard_deny_by_path_glob",
  "ruleset": {
    "allow": [ { "host": "*", "paths": ["/**"] } ],
    "deny_always": [ { "host": "*", "paths": ["**/.env"], "reason": "Secrets path." } ]
  },
  "request": { "host": "example.com", "path": "/config/.env", "method": "GET" },
  "expected": { "decision": "hard-deny", "mode": "intercept",
                "matched_rule": { "list": "deny_always", "index": 0 } }
}
```
Cover (AC criterion 3, exhaustively): allow (filtered path+method); host-wide allow
(Mode B, nil paths); soft-deny default (no match); hard-deny by host; hard-deny by
path glob (`**/.env`); passthrough host allow (`mode: passthrough`); passthrough ×
path-scoped deny **suppressed** (allow wins) vs passthrough × host-level deny
**enforced** (hard-deny); wildcard edge cases — `*.suffix` host (+ apex miss), `*`
host, `*` not crossing `/`, `**` at root vs depth, prefix coverage; most-specific
mode selection. `mode` is `"intercept"`/`"passthrough"`/`""` (soft-deny);
`matched_rule` is `{list,index}` or `null`.

**`internal/policy/vectors_test.go`** (corpus-driven)
- Define test-only structs for decoding a vector (`vector`, `vectorRuleSet`,
  `expectation`) — kept in the test, since the corpus is data the Python side reads
  independently, not a Go API.
- `os.ReadDir("testdata/decision-vectors")`, filter `*.json`; **fail if zero
  files**. For each: `os.ReadFile`, decode with `json.NewDecoder` +
  `DisallowUnknownFields()` (fail-closed, the repo's strict-decode stance applied to
  JSON), `t.Run(vector.Name, ...)`, run `rs.Decide(req)`, assert
  `decision`/`mode`/`matched_rule` (use `require.Equal`).
- Assert the matched-rule pointer/`null` equivalence cleanly (nil ⇔ JSON `null`).

### Verification
- [ ] `go test -race ./internal/policy/...` passes (every vector green).
- [ ] Temporarily emptying / renaming the corpus dir makes the test **fail** (zero-
      vectors guard works) — confirm manually, then restore.
- [ ] `jq '.' internal/policy/testdata/decision-vectors/<each>.json` exits 0 (plain
      JSON, no Go-specific encoding) — spot-check at least via a loop.
- [ ] `make test` (full suite) green; `make lint` clean.

---

## Success criteria

### Automated
- [ ] `go build ./...` compiles.
- [ ] `make test` (= `go test -race ./...`) passes, including the corpus-driven test.
- [ ] `make lint` (= `go vet ./...` + `golangci-lint run`) clean.
- [ ] Every file in `internal/policy/testdata/decision-vectors/` is valid JSON
      (`jq '.'` exits 0) with no Go-specific encoding.

### Manual
- [ ] Removing all vectors makes the corpus test fail (zero-vectors guard).
- [ ] The corpus covers each AC-listed case (allow, host-wide allow, soft-deny
      default, hard-deny by host, hard-deny by path glob, passthrough host, wildcard
      edge cases) and the passthrough blind-spot pair.
- [ ] The package header documents host/path/method/precedence/most-specific/
      passthrough semantics precisely enough for a Python re-implementation.

## Testing strategy

- **Unit (table-driven):** host, path/glob, method primitives, specificity, and
  `Decide` scenarios — `internal/policy/match_test.go`.
- **Corpus-driven (C1):** `internal/policy/vectors_test.go` iterates every JSON
  vector; this is the artifact AC-0017 reuses. New-to-repo directory-iteration +
  zero-guard pattern.
- **No golden files** here — the matcher's output is structured data asserted
  directly against the corpus, not rendered text.
- **No integration tests** — the matcher is pure; no external tool involved.

## References

- Research: `thoughts/shared/research/2026-06-05-AC-0010-rule-model-matcher.md`
- Ticket: `thoughts/shared/tickets/AC-0010-rule-model-matcher.md`
- `internal/config/config.go:51-89`, `validate.go:20-43` — rule model + invariants.
- `docs/design.md:153,212-246,250-283` — decision model, modes, refusal handling.
- Spec WP-2.1 / C1:
  `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:94-101,165-169`.
