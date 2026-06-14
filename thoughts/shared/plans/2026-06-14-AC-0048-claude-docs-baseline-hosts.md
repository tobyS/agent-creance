---
date: 2026-06-14
ticket: AC-0048
title: "Add Claude documentation hosts to the global egress baseline"
status: ready
research: thoughts/shared/research/2026-06-14-AC-0048-claude-docs-baseline-hosts.md
branch: main
---

# AC-0048 — Add Claude documentation hosts to the global egress baseline

## Overview

Extend the scaffolded global egress baseline (AC-0043) so a freshly set-up cage
lets a Claude agent read Anthropic's own documentation out of the box. Today
`code.claude.com/docs/...` fetches are soft-denied, sending research subagents
mirror-hunting. The fix adds three Anthropic-owned, credential-free documentation
hosts to the `globalConfigTemplate` constant, scoped tightly to GET, plus a test
assertion and a README adoption note for users whose global config predates the
change.

## Decisions resolved at the question checkpoint

- **Host set:** add `code.claude.com` **and** the legacy redirector hosts
  `docs.anthropic.com` and `docs.claude.com` (so older doc links resolve without
  thrash). `platform.claude.com` is already in the baseline (passthrough) and
  already serves the API docs — unchanged.
- **Adoption docs:** the paste-this-snippet note for existing-config users goes
  in **README**.

Resolved by research (not asked):

- **Mode/scoping:** `intercept` (passthrough forbids paths/methods at compile
  time). `code.claude.com` is scoped to `paths: ["/docs/"]`, `methods: [GET]`
  (all docs live under `/docs`; prefix-by-default segment matching covers
  `/docs/en/...`, `/docs/llms.txt`, `/docs/en/<page>.md`). The two redirector
  hosts get host-wide `methods: [GET]` (no path scope) because legacy links they
  301/302 to `platform.claude.com` are NOT all under `/docs` — path-scoping them
  would defeat their purpose. Mode is written explicitly as `intercept` on all
  three (it is also the default, but explicit prevents a future "simplify to
  passthrough" that would be a compile error).
- **Asset hosts (Mintlify CDN):** excluded — the caged agent reads docs via GET,
  it does not render them in a browser, so CSS/JS/font hosts are irrelevant and
  would breach the minimal/Anthropic-only principle.
- **GitHub hosts:** excluded — explicit product decision (project-filtered only).

## Current state

- `internal/cli/setup.go:135-161` — `const globalConfigTemplate` (Go raw-string
  YAML). Current entries: passthrough `api.anthropic.com` / `claude.ai` /
  `platform.claude.com`; intercept (mode omitted) `downloads.claude.ai` /
  `raw.githubusercontent.com`; commented-out telemetry hosts.
- `internal/cli/setup_test.go:238-270` — `TestSetupScaffoldsGlobalConfig` parses
  the scaffolded template and asserts the `api.anthropic.com` passthrough rule.
  No exact rule-count assertion, so additive hosts don't break it.
- `internal/config/validate.go:28-44` — intercept rules may carry paths/methods;
  passthrough rules may not.
- `internal/policy/glob.go` — `matchPath` (prefix-by-default segments, `**`
  supported), `matchMethod` (nil = any, set = verbatim membership).
- `README.md` — high-level only; no configuration/baseline section yet.
- No golden file captures the raw template; `internal/policy/compile/testdata/policy.golden`
  is a compiled fixture from unrelated test configs and does NOT change.

## Desired end state

- `agent-creance setup` on a fresh machine writes a global config whose
  `network.egress.allow` includes `code.claude.com` (intercept, GET, `/docs/`)
  and the two legacy redirector hosts (intercept, GET, host-wide).
- A caged agent can fetch `https://code.claude.com/docs/...` (allowed by an
  intercept rule) and follow `docs.anthropic.com` / `docs.claude.com` redirects
  through to the already-allowed `platform.claude.com`.
- README documents the addition and gives the exact snippet existing-config
  users paste to adopt it.
- `make test`, `make lint`, `make build` green.

## What we are NOT doing

- Not adding Mintlify CDN/asset hosts, GitHub hosts, or telemetry hosts.
- Not modifying existing global configs or adding a `doctor` staleness check
  (both excluded by AC-0043 and reaffirmed by this ticket).
- Not changing `platform.claude.com` (already passthrough-allowed).
- Not touching the compiled-policy golden (unrelated fixture).

---

## Phase 1 — Add the docs hosts to the scaffolded baseline + test

### Changes

**`internal/cli/setup.go`** — extend `globalConfigTemplate` with a new block
after the `raw.githubusercontent.com` entry (before the commented telemetry
block). Update the constant's doc comment to mention the docs hosts (AC-0048).

New YAML block:

```yaml
      # Anthropic's public documentation (credential-free, read-only). Scoped to
      # GET so the cage opens these hosts for reading docs only, never writes.
      # code.claude.com serves the Claude Code + Agent SDK docs under /docs;
      # docs.anthropic.com and docs.claude.com are legacy hosts that redirect to
      # platform.claude.com (already allowed above), so they are host-wide GET.
      # (AC-0048)
      - host: code.claude.com
        mode: intercept
        paths: ["/docs/"]
        methods: [GET]
      - host: docs.anthropic.com
        mode: intercept
        methods: [GET]
      - host: docs.claude.com
        mode: intercept
        methods: [GET]
```

**`internal/cli/setup_test.go`** — in `TestSetupScaffoldsGlobalConfig`, after the
existing `api.anthropic.com` assertion, add assertions over the parsed config:
- a `code.claude.com` rule exists with `Mode == config.ModeIntercept`, its
  `Paths` contains `/docs/`, and its `Methods` contains `GET`;
- `docs.anthropic.com` and `docs.claude.com` rules exist, each
  `Mode == config.ModeIntercept` with `Methods` containing `GET`.

Use a small host→`*config.Rule` lookup over `cfg.Network.Egress.Allow` to keep
the assertions readable.

### Success criteria

#### Automated
- [x] `make test` passes (`TestSetupScaffoldsGlobalConfig` proves the template
      still parses+validates and the new rules are present and correctly scoped).
- [x] `make lint` passes (`go vet` + `golangci-lint`).
- [x] `go build ./...` succeeds.

#### Manual
- [x] Reading `globalConfigTemplate`, the three new entries are present, scoped
      as specified, and the comment explains the GET-only / redirector rationale.

---

## Phase 2 — Document baseline adoption in README

### Changes

**`README.md`** — add a short `## Egress baseline` section (after `## Requirements`)
that:
- states `agent-creance setup` scaffolds the global baseline at
  `~/.config/agent-creance.yaml` and never modifies an existing one;
- notes it now includes Anthropic's documentation hosts so caged agents can read
  the docs out of the box;
- gives the exact snippet existing-config users paste under
  `network.egress.allow` to adopt the docs hosts (the same three entries from
  Phase 1, without the inline comments or with a trimmed comment).

Keep it concise and consistent with the README's terse, pre-v0.1 tone.

### Success criteria

#### Automated
- [x] `make test` still passes (docs-only change; sanity only).

#### Manual
- [x] README has the new section; the snippet is copy-pasteable and matches the
      template's hosts/scoping exactly.

---

## Phase 3 — Final verification, build, ticket close

### Changes
- Run the full check suite.
- `make build` so `bin/agent-creance` reflects the final commit (the user tests
  with this binary).
- Set the ticket `**Status:** Done` and add a Notes & Updates entry.

### Success criteria

#### Automated
- [x] `make test` green.
- [x] `make lint` green.
- [x] `make build` produces `bin/agent-creance`.

#### Manual
- [x] Ticket `AC-0048` status is `Done` with an implementation note.
- [ ] (Optional, user) live cage run: `code.claude.com/docs/...` returns an
      intercept allow (visible in the audit log / `policy explain`).

---

## Testing strategy

- **Unit (table/assertion):** `TestSetupScaffoldsGlobalConfig` is the single
  source of truth — it parses the real scaffolded template through
  `config.Parse` (strict keys + validation) and asserts the new rules. This
  covers both "template still valid" and "docs hosts present + correctly scoped".
- **No new golden files.** The template has no golden snapshot; the compiled
  policy golden is built from unrelated test configs and is untouched.
- **No integration test needed.** End-to-end allow behavior is the matcher's job
  (already covered by `internal/policy` decision-vector tests); the live-run
  check is an optional manual confirmation, not a CI gate.

## References

- Research: `thoughts/shared/research/2026-06-14-AC-0048-claude-docs-baseline-hosts.md`
- Ticket: `thoughts/shared/tickets/AC-0048-claude-docs-baseline-hosts.md`
- Prior art: `thoughts/shared/plans/2026-06-12-AC-0043-global-claude-baseline.md`
- Edit site: `internal/cli/setup.go:135-161`; test: `internal/cli/setup_test.go:238-270`
