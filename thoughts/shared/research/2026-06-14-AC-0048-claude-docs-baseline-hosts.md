---
date: 2026-06-14
ticket: AC-0048
title: "Research — Add Claude documentation hosts to the global egress baseline"
status: complete
branch: main
commit: 7b48b802207d163179f567cb57cb2d2f4c0b994a
repo: git@github.com:tobyS/agent-creance.git
---

# Research: AC-0048 — Add Claude documentation hosts to the global egress baseline

## Research question

The scaffolded global egress baseline (AC-0043) lets a caged Claude agent reach
the hosts Claude Code needs to *operate* but not the hosts it needs to *read
Anthropic's own documentation* — `code.claude.com/docs/...` fetches are
soft-denied, producing minutes of mirror-hunting thrash. How do we add the
Anthropic documentation host(s) to the scaffolded baseline, scoped as tightly as
practical, without modifying existing global configs and without adding GitHub
hosts?

## Summary of findings

1. **The fix is a single Go raw-string constant edit.** The scaffolded baseline
   is `globalConfigTemplate` in `internal/cli/setup.go:135-161` — not an embedded
   template file. Adding `code.claude.com` is an edit to that string plus a test
   assertion and a docs note.

2. **`code.claude.com` is the gap; `platform.claude.com` already covers the API
   docs.** Web research confirmed Anthropic docs are served from two hosts:
   `code.claude.com` (Claude Code + Agent SDK docs) and `platform.claude.com`
   (Claude/Anthropic API reference). `platform.claude.com` is *already* in the
   baseline as `passthrough`, so its `/docs` already work. Only `code.claude.com`
   is missing. The legacy hosts `docs.anthropic.com` and `docs.claude.com` are
   pure 301/302 redirectors to `platform.claude.com` and serve no content.

3. **Tight path+method scoping is practical and supported.** The policy matcher
   (`internal/policy/glob.go`) supports prefix-by-default path segments and a
   `methods` list. An intercept rule scoped to `paths: ["/docs/"]`,
   `methods: [GET]` covers every doc the agent fetches (`/docs/en/...`,
   `/docs/en/<page>.md`, `/docs/llms.txt`) and matches the ticket's "GET-only
   and/or docs paths if practical" guidance. Path/method scoping *requires*
   `intercept` mode (passthrough rejects paths/methods at compile time), which
   aligns with the ticket's "in `intercept` mode" acceptance criterion.

4. **Mintlify CDN asset hosts are NOT needed.** The docs are Mintlify-hosted and
   a *browser* would pull CSS/JS/fonts from `*.mintcdn.com` / `*.cloudfront.net`
   / `cdn.jsdelivr.net`. But the caged agent reads docs via HTTP GET of
   pages/markdown — it does not render them — so asset hosts are irrelevant to
   the use case. Adding them would violate the ticket's "credential-free
   Anthropic documentation hosts only / keep the baseline minimal" principle and
   introduce non-Anthropic third-party hosts. Excluded.

5. **Two genuine judgment calls remain for the checkpoint:** (a) whether to also
   add the legacy redirector hosts (`docs.anthropic.com`, `docs.claude.com`) for
   old links, vs. `code.claude.com` only; (b) where the "existing-config users:
   paste this snippet" documentation lives (design.md baseline section vs README).

## Detailed findings

### Where the baseline lives (the edit site)

- **`internal/cli/setup.go:135-161`** — `const globalConfigTemplate`, a Go
  raw-string YAML literal. This is the only place the baseline content exists;
  there is no `//go:embed`'d template. Current entries:
  - `passthrough`: `api.anthropic.com`, `claude.ai`, `platform.claude.com`
    (token-carrying hosts — tunneled raw so the proxy never sees tokens).
  - `intercept` (mode omitted → defaults to intercept): `downloads.claude.ai`,
    `raw.githubusercontent.com`.
  - Commented-out telemetry: `statsig.anthropic.com`, `statsig.com`, `sentry.io`.
  - Header comment notes it "Must parse under config.Parse … pinned by
    TestGlobalConfigTemplateParses" (the test is actually named
    `TestSetupScaffoldsGlobalConfig`).
- **`internal/cli/setup.go:104-126`** — `scaffoldGlobalConfig`: resolves the
  global path via `config.NewLoader(...).GlobalPath()` (same resolution the
  compiler reads), skips if any file already exists (the AC-0043 "never modify
  existing config" invariant), else writes the template atomically. No change
  needed here.

### Config schema and modes

- **`internal/config/config.go:89-101`** — `Rule{Host, Paths *[]string,
  Methods *[]string, Mode, Reason}`. `Paths`/`Methods` are pointers so "set but
  empty" is distinguishable from "omitted". Modes: `ModeIntercept = "intercept"`,
  `ModePassthrough = "passthrough"`.
- **`internal/config/config.go:206-212`** — `defaultRuleModes`: an omitted
  `mode` defaults to `intercept`.
- **`internal/config/validate.go:28-44`** — passthrough rules that set `paths`
  or `methods` are a compile-time error. So path/method scoping is only legal
  under `intercept`. This is exactly why the new docs rule must be `intercept`,
  not `passthrough`.

### Path / method matching semantics (scoping is safe)

- **`internal/policy/glob.go:30-63`** — `matchPath` uses *prefix-by-default
  segment* semantics: pattern `/docs` (segments `[docs]`) matches any request
  path whose first segment is `docs` (`/docs/en/overview`, `/docs/llms.txt`,
  …). A trailing `**` is also supported but unnecessary for a prefix. Existing
  golden rules use the trailing-slash prefix form (`/repos/tobyS/x/`, `/v2/`),
  which is the idiom to follow → `paths: ["/docs/"]`.
- **`internal/policy/glob.go:99-111`** — `matchMethod`: a nil list matches any
  method; a set list requires verbatim membership. `methods: [GET]` is correct
  for doc reads (Claude's WebFetch/curl issue GET).
- **Conclusion:** `host: code.claude.com`, `mode: intercept`, `paths: ["/docs/"]`,
  `methods: [GET]` is the tight, practical rule. It is fully audited (intercept)
  and still subject to path-based `deny_always`.

### Anthropic documentation hosts (web research, 2026-06-14)

| Host | Role | Confirmed |
|------|------|-----------|
| `code.claude.com` | Claude Code + Agent SDK docs (`/docs/en/...`) | yes — the soft-denied gap |
| `platform.claude.com` | Claude/Anthropic API reference, tool-use, model docs (`/docs/...`) | yes — **already in baseline (passthrough)** |
| `docs.anthropic.com` | 301 → `platform.claude.com/docs` (no content) | yes |
| `docs.claude.com` | 301 → `platform.claude.com/docs` (no content) | yes |
| `api.anthropic.com` | API *endpoint*, not docs — already in baseline | n/a |
| `*.mintcdn.com`, `*.cloudfront.net`, `cdn.jsdelivr.net`, `fonts.googleapis.com` | Mintlify CDN assets (browser rendering only) | inferred — **not needed for agent GET use case; excluded** |

Docs are purely public, GET-based content; no credentials are needed to read
them (API keys are only for `api.anthropic.com`).

### Tests and golden files affected

- **`internal/cli/setup_test.go:238-270`** — `TestSetupScaffoldsGlobalConfig`
  parses the scaffolded template (so the new rule must keep it parsing/valid) and
  asserts the `api.anthropic.com` passthrough rule. Add an assertion that a
  `code.claude.com` intercept rule scoped to GET `/docs/` is present.
- **No raw-template golden file exists** — the template is validated by parsing,
  not by a `.golden` snapshot. `internal/policy/compile/testdata/policy.golden`
  is a *compiled* fixture built from test-only configs (`api.anthropic.com`,
  `api.github.com`, …), unrelated to the scaffolded baseline; it does **not**
  change from this edit.
- **`internal/cli/testdata/script/setup_help.txtar`** — `setup --help` output
  only; does not assert baseline hosts; no change needed.

### Documentation surfaces

- **`docs/design.md:156`** — describes the global "always-allowed baseline".
- **`docs/design.md:248-271`** — the three enforcement modes (intercept host-wide
  = "Mode B", passthrough = "Mode C") and the passthrough credential-privacy
  rationale. The new rule is a "Mode B" intercept with path/method scoping.
- **`docs/design.md:381-389`** — the `setup` command docs, already noting the
  baseline scaffold (AC-0043). The natural home for the "existing configs: paste
  this snippet to adopt the docs host" note.
- **`README.md`** — high-level only; no baseline-host content today.

## AC-0043 lineage (what this builds on)

- `thoughts/shared/tickets/AC-0043-global-claude-baseline.md` — the baseline
  scaffold ticket. Established: setup writes the visible file (rejected a
  compiler-built-in baseline), never modifies an existing config, passthrough for
  token-carrying hosts. Host list sourced from
  `code.claude.com/docs/en/network-config`.
- Companion refusal-visibility cluster (same 2026-06-12 session): AC-0047
  (470/471 visible refusals), AC-0049 (skill recognizes body-blind 403s), AC-0050
  (self-describing reason phrases). AC-0048 *reduces how often* refusals happen;
  that cluster makes the remaining ones legible.

## Open questions for the checkpoint

1. **Host set.** `code.claude.com` only (minimal; covers the observed gap;
   `platform.claude.com` already covers API docs; modern Claude agents use these
   hosts) — vs. also adding the legacy redirectors `docs.anthropic.com` /
   `docs.claude.com` so older doc links resolve without thrash. Trade-off:
   future-proofing old links vs. baseline minimality (both redirectors are
   Anthropic-owned and credential-free, so still within the ticket's spirit).
2. **Adoption-doc location.** design.md baseline/`setup` section (where the
   baseline is already documented) vs. README (more discoverable to new users)
   vs. both.

Resolved by research (no checkpoint needed):
- **Mode/scoping:** `intercept`, `paths: ["/docs/"]`, `methods: [GET]` — matcher
  confirmed to support it; matches the AC's tight-scoping directive.
- **Asset hosts:** excluded — not needed for the agent GET use case; would breach
  the minimal/Anthropic-only principle.
- **GitHub hosts:** excluded — explicit product decision (project-filtered only).
