---
date: 2026-06-05
ticket: AC-0010
title: "Rule model & matcher + decision-vector corpus (WP-2.1)"
status: complete
git_commit: a2ca3c050cea6276faf73153f249cc168198633b
branch: main
repository: github.com/tobyS/agent-creance
researcher: Claude (Opus 4.8)
tags: [research, policy, matcher, decision-vectors, C1, WP-2.1]
---

# Research: AC-0010 — Rule model & matcher + decision-vector corpus (WP-2.1)

**Ticket:** `thoughts/shared/tickets/AC-0010-rule-model-matcher.md`
**Depends on:** AC-0007 (config schema/loader — `internal/config`, done)
**Gates:** AC-0013 (compiler), AC-0015, AC-0017 (Python `enforcer.py`)
**Cross-cutting:** establishes **C1** (Go↔Python matcher parity)

## Research Question

Build the Go egress matcher in `internal/policy` that, given a compiled rule set and
a request `(host, path, method)`, returns the decision (`allow` / `soft-deny` /
`hard-deny`), the carried `mode`, and the matched rule — with precedence and
wildcard/glob semantics fully and unambiguously specified — plus a language-neutral
`testdata/decision-vectors/` corpus that the future Python `enforcer.py` (AC-0017)
must also satisfy. The corpus is the single guardrail against Go/Python drift.

## Summary

Everything the matcher consumes already exists and is stable:

- The **rule model is `internal/config.Rule`** (host, `*[]string` paths, `*[]string`
  methods, mode, reason) with `Egress.Allow` and `Egress.DenyAlways` lists. Validation
  already guarantees: every rule has a non-empty `Host`; `Mode` is always non-empty
  (`intercept` default applied at parse) and is exactly `intercept` or `passthrough`;
  a `passthrough` rule carries **no** paths/methods. The matcher can rely on all of
  these as invariants and does not re-validate.
- The **decision model is fixed by design** (`docs/design.md` "Network refusal
  handling"): the three outcomes are exhaustive — `deny_always` match → hard-deny;
  else `allow` match → allow (carrying the matched rule's mode); else → soft-deny.
  `deny_always` unconditionally shadows `allow`. There is no "default" knob.
- The **test idioms are established**: table-driven tests (testify `require`),
  golden-file tests with a `-update` flag (`make golden`), fixtures under `testdata/`.
  What does *not* yet exist in the repo: any directory-iterating fixture loader, any
  `encoding/json` usage, and any zero-fixtures guard. AC-0010 introduces all three.

The matcher itself is small. The genuine design decisions are two glob/precedence
semantics questions the ticket already flagged, plus one the design doc surfaces
implicitly (passthrough × path-scoped deny). These are the checkpoint questions.

## Detailed Findings

### The rule model the matcher consumes (`internal/config`)

`internal/config/config.go:51-89` — the types are done and validated:

```go
type Egress struct {
	Generators []string
	Allow      []Rule
	DenyAlways []Rule
}

type Rule struct {
	Host    string    `yaml:"host"`
	Paths   *[]string `yaml:"paths"`   // nil = key omitted (≠ empty slice)
	Methods *[]string `yaml:"methods"` // nil = key omitted
	Mode    string    `yaml:"mode"`    // always "intercept" or "passthrough"
	Reason  string    `yaml:"reason"`
}

const (
	ModeIntercept   = "intercept"
	ModePassthrough = "passthrough"
)
```

Invariants the matcher may assume (enforced in `internal/config/validate.go:20-43`
and `config.go:176-182`, so the matcher must **not** re-check them):

- `Host` is non-empty for every rule.
- `Mode` is non-empty; it is one of the two constants (parse applies the `intercept`
  default and rejects any other value).
- A `passthrough` rule has `Paths == nil` **and** `Methods == nil`.
- `Paths`/`Methods` are pointers: `nil` means "key omitted" (rule is host-wide for
  that dimension); a non-nil slice is the set to match against. The matcher must
  nil-check before dereferencing.

**Open design point — does `internal/policy` reuse `config.Rule` or define its own
type?** `config.Rule` carries yaml tags and the `*[]string` ergonomics chosen for
strict decoding. The matcher and the JSON decision-vector corpus want plain
`[]string` and json tags. Recommendation (see Open Questions): the matcher operates
on a small `policy`-owned rule/ruleset type that the corpus decodes into directly,
and a later trivial adapter converts `config.Egress` → `policy.RuleSet`. This keeps
the language-neutral corpus shape decoupled from the yaml mirror and avoids leaking
`*[]string` into json. WP-2.4 (the compiler, AC-0013) is what will produce a
`policy.RuleSet` from `config` + generator output, so a policy-owned type is the
natural seam.

### The decision model (fixed by design)

From `docs/design.md:250-283` ("Network refusal handling") and `:153`:

- **Allowed** — matched an `allow` rule and no `deny_always` shadowed it. Carries the
  matched allow's `mode`.
- **Soft-deny** — *not in the allowlist*. The implicit default for any request that
  matches neither list. "anything not in `allow:` and not in `deny_always:` produces
  a soft-deny. There is no separate 'default' knob — the categories are exhaustive."
- **Hard-deny** — matched a `deny_always` rule. Carries that rule's `reason`.

Precedence (ticket AC criterion + `docs/design.md:254`): **`deny_always` shadows
`allow` unconditionally.** So the matcher checks deny first; a deny match wins even
over a more-specific allow. Algorithm skeleton:

1. Evaluate `deny_always`. If any rule matches `(host, path, method)` → **hard-deny**,
   report that rule (+ `reason`).
2. Else evaluate `allow`. If any matches → **allow**, report that rule and its `mode`.
3. Else → **soft-deny**, no matched rule.

The decision is invariant under which matching rule is reported (any allow → allow;
any deny → hard-deny). Tie-breaks only affect *which* rule and *which* mode is
reported — that is exactly the second open question.

### Matching dimensions (host / path / method)

**Host** (AC criterion 1): exact, `*.suffix`, and `*` (whole) wildcards.
- Exact: `api.github.com` matches only `api.github.com`.
- `*.suffix`: `*.medium.com` matches `foo.medium.com`, `a.b.medium.com`. Open
  question: does `*.medium.com` also match the bare apex `medium.com`? (`.gitignore`
  /DNS convention says **no** — `*.` requires at least one label. Recommend: no.)
- `*`: matches any host (used by the secrets-path deny: `host: "*"`, `docs/design.md:139`).

**Path** (AC criterion 1): prefix + `*`/`**` globs. See the glob spec below. A rule
with `Paths == nil` matches any path (host-wide, Mode B). A rule with a non-nil paths
list matches if **any** listed pattern matches the request path.

**Method** (AC criterion 1): set-membership. `Methods == nil` → any method. A non-nil
list matches if the request method ∈ list. (Methods are case-sensitive uppercase by
HTTP convention; the config examples use `GET`, `POST`. Recommend documenting that
the corpus uses canonical uppercase and the matcher compares verbatim — normalization,
if any, is a documented decision.)

### Glob semantics for path matching (the crux — web research)

Authoritative convention (from `.gitignore`(5) and `bmatcuk/doublestar`, corroborated):

- **`*` never crosses `/`** — matches any run of non-`/` characters within one segment.
- **`**` as a whole path component matches zero or more segments, including zero.**
  `.gitignore` states it verbatim: a leading `**/foo` is "the same as pattern `foo`"
  — i.e. `**/.env` matches `.env` at the **root** (zero leading segments) *and*
  `a/b/.env` at any depth. `a/**/b` matches `a/b` (zero), `a/x/b`, `a/x/y/b`.
- **`**` only has special meaning as a standalone segment.** Glued to other characters
  (`a**b`) it degrades to a single `*`. Recommendation: **reject** glued `**` rather
  than silently degrade, to remove ambiguity (a `ValidatePattern`/compile-time check).
- Go stdlib `path.Match` is **insufficient**: it has no `**` and its `*` does not cross
  `/`. This is why `doublestar` exists.

**Build vs. library decision.** `bmatcuk/doublestar` (MIT, zero deps, mature) is a fine
library but implements far more than needed (filesystem glob, brace alternation,
char classes, escaping, OS-separator handling). The decisive factor is **cross-language
parity**: AC-0017's Python side must match byte-for-byte, and replicating doublestar's
exact edge cases in Python (where `fnmatch`/`glob`/`pathlib` all differ) is more work
than mirroring a small hand-authored spec. The pattern set here is tiny (prefix, `*`
segment wildcard, `**` any-depth). **Recommendation: hand-roll** a ~30–40 line
segment matcher and pin every edge with corpus vectors. This aligns with the project's
"minimal dependencies, auditable security tool" stance.

**Proposed precise path-match rule** (to document in the package header and corpus):

1. Operate on the URL path only (host/query already split off).
2. Strip a single leading `/`, then split pattern and path on `/` into segments (apply
   the same trailing-slash rule to both — recommend: a trailing `/` yields a trailing
   empty segment; pin it with a vector).
3. Match segment lists: a pattern segment of exactly `**` consumes zero-or-more input
   segments (the only token that crosses `/`); any other segment matches a single
   input segment with `*` = "any run of non-`/`". Match is **anchored and total** (the
   whole path must be consumed, like `path.Match`).
4. Consequences (all to be encoded as vectors): `/repos/org/repo/` matches exactly
   that path; for a subtree the rule must spell `/repos/org/repo/**`; `/@*` matches
   `/@scope` but not `/@scope/pkg`; `**/.env` matches `.env` and `a/b/.env`; bare `**`
   matches any path including root. **Note a tension with the generator design**
   (`docs/design.md:164-165`): generated rules use *prefix* scoping like
   `/repos/tobyS/this-project/` and call it "covers web view + .git operations" — i.e.
   the design's mental model treats a trailing-slash path as a **prefix** match, not an
   exact one. This is the first open question: prefix-by-default vs explicit `/**`.

### Precedence / tie-break when multiple rules match

The ticket flags this ("how is 'most specific rule wins' defined when multiple allows
match?"). Findings:

- For the **decision**, tie-break is irrelevant (any allow → allow).
- For the **reported rule and carried `mode`**, it matters: a host-wide
  `mode: intercept` allow (Mode B) and a path-scoped allow on the same host could both
  match, with different modes; `policy explain` must report a single deterministic
  answer and the proxy must enforce a single mode.
- Options: (a) **most-specific-wins** with a defined specificity order (exact host >
  `*.suffix` > `*`; then longer/more-literal path > shorter; nil paths = least
  specific) — deterministic regardless of file/union order, which matters because
  `allow` lists union additively across includes (`docs/design.md:151`), so
  declaration order is ill-defined; (b) **first-match in list order** — simpler but
  order-dependent and therefore fragile under union. Recommendation: **most-specific-
  wins**, documented, because union additivity makes order non-deterministic.
- The same question applies to multiple `deny_always` matches (which `reason` to
  report). Recommend the same specificity rule for symmetry.

### Passthrough × path-scoped deny (surfaced by the design doc)

`docs/design.md:242`: on a `passthrough` *allowed* host, a `deny_always` with a
**path** "will not be caught" because the proxy only sees the CONNECT host, never the
path — but a **host-level** `deny_always` (no path) on a passthrough host **is**
enforced (tunnel refused). This inverts the simple "deny always wins" precedence for
the narrow case of a path-scoped deny against a passthrough host.

This is a real semantic decision for the matcher and corpus (third open question):
should the language-neutral matcher model this passthrough blind-spot (a path-scoped
deny does *not* produce hard-deny when the host's matching allow is passthrough), or
should the matcher remain the pure intercepting-proxy decision function (full
host+path+method) and treat the passthrough blind-spot purely as a runtime property of
`enforcer.py`? Both Go `explain` and Python `enforcer.py` must agree either way, so
whichever is chosen must be in the corpus.

### Decision-vector corpus shape (C1)

Requirements from the ticket AC + spec C1 (`spec:94-101`):
- Location: `internal/policy/testdata/decision-vectors/`, one JSON file per vector (or
  per group), plain JSON, no Go-specific encoding (`jq '.'` over each exits 0).
- Each vector: `ruleset` (allow + deny_always) + `request` (host, path, method) →
  `expected` `{decision, mode, matched_rule}`.
- `matched_rule` must be language-neutral. Recommended encoding: `null` for soft-deny,
  else `{"list": "allow"|"deny_always", "index": N}` (index into the corresponding
  list) — unambiguous across languages and robust to identical hosts. (Alternative:
  echo the matched host string; less precise when two rules share a host.)
- Coverage required by AC: allow, host-wide allow (Mode B), soft-deny default,
  hard-deny by host, hard-deny by path glob, passthrough host, and wildcard edge cases
  (`*.suffix`, `*` host, `**` at root vs depth, `*` not crossing `/`).
- The Go table test must: iterate **every** file in the dir, assert matcher output ==
  expected, and **fail if zero vectors are found** (guard against an empty corpus
  silently passing). This directory-iteration + zero-guard pattern is **new to the
  repo** — no existing test enumerates a directory (confirmed: zero `os.ReadDir`/
  `WalkDir`/`Glob` uses in the tree).

### Code conventions to match

- **Package header**: `// Package policy <verb>...` multi-paragraph, stating what it
  deliberately does *not* do and citing ticket IDs / `docs/design.md` (mirror
  `internal/config/config.go:1-16`).
- **Table tests**: anonymous `cases` slice, first field `name`, `t.Run(tc.name, ...)`,
  testify `require` (`internal/config/validate_test.go:54-133`).
- **Golden + `-update`**: `var update = flag.Bool("update", false, "regenerate golden
  files")`; `filepath.Join("testdata", ...)`; write `0o644` then early return;
  failure hint `"missing golden file; run with -update to create it"`
  (`internal/prereq/report_test.go:21-48`). Likely unused for this ticket unless the
  matched-rule/explain rendering is golden-tested; the corpus is data-driven, not
  golden.
- **JSON decoding** (new to repo): mirror the `config` "fail closed" stance — use
  `json.Decoder` with `DisallowUnknownFields()` (the json analog of yaml
  `KnownFields(true)`), snake_case tags. A mistyped corpus key should error, not
  silently drop.
- **No OS in logic packages**: the matcher is pure; the *test* reads `testdata/` via
  `os`/`io/fs` directly (tests may touch the filesystem — only logic packages route
  through `sysdep`). No `sysdep` interface is needed here.

## Code References

- `internal/config/config.go:51-89` — `Egress` / `Rule` / mode constants (matcher input).
- `internal/config/validate.go:20-43` — invariants the matcher may assume.
- `internal/config/config.go:176-182` — mode defaulting (`Mode` always non-empty).
- `internal/config/errors.go` — `ValidationError` accumulation idiom (model for any
  pattern-validation errors).
- `internal/prereq/version_test.go:10-37` — canonical table test.
- `internal/config/validate_test.go:13-49,54-133` — golden `-update` + table-with-testify.
- `internal/prereq/report_test.go:21-48` — single-golden form.
- `docs/design.md:153,212-246,250-283` — decision model, modes, refusal handling.
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:94-101,165-169`
  — C1 mandate, WP-2.1 "done when".

## Architecture Insights

- The matcher is the **first half of C1**; its public surface and the corpus shape are
  a de-facto contract the Python addon must implement. Keep the matcher pure and the
  corpus encoding dumb-simple JSON so Python can consume it with stdlib `json`.
- A `policy`-owned `RuleSet` type (not `config.Egress`) is the right seam: the compiler
  (AC-0013) unions config + generated + session-overlay rules into a `policy.RuleSet`,
  and the corpus decodes straight into it. Decoupling from the yaml mirror avoids
  `*[]string` and yaml tags leaking into the language-neutral layer.
- "Most-specific-wins" is the order-independent choice that survives `include:` union
  additivity; "first-match" would make `explain` depend on file merge order.

## Open Questions (for the checkpoint)

1. **Path semantics: prefix-by-default or exact + explicit `/**`?** The generator
   design (`docs/design.md:164-165`) treats `/<org>/<repo>/` as a *prefix* covering
   everything under it; the clean glob spec treats a trailing-slash path as an exact
   match unless `/**` is appended. Which is the rule the matcher implements (and the
   corpus encodes)? (Recommend: trailing-`/` path = prefix match — i.e. an implicit
   trailing `**` — to match the generator's stated behavior, while still supporting
   explicit `*`/`**`. Needs confirming because it changes every path vector.)

2. **Tie-break when multiple rules match: most-specific-wins (recommended) or
   first-in-list?** Affects which `mode`/`reason` `explain` and the proxy report.
   (Recommend most-specific-wins for order-independence under union.)

3. **Passthrough × path-scoped deny: does the matcher model the blind spot?** Should a
   path-scoped `deny_always` be suppressed when the host's matching allow is
   `passthrough` (mirroring `docs/design.md:242`), or is that purely a runtime concern
   left out of the shared matcher? Whatever is chosen must be encoded in the corpus so
   Go and Python agree.

4. **Minor, lower-stakes (recommend defaults, will proceed unless told otherwise):**
   does `*.medium.com` match the apex `medium.com`? (recommend **no**); are HTTP
   methods compared case-sensitively against canonical uppercase? (recommend **yes**,
   verbatim); reject glued `**` like `a**b` at validate time? (recommend **yes**).
