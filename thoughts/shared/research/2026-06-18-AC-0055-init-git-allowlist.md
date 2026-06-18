---
date: 2026-06-18
ticket: AC-0055
title: "Research — init auto-allowlists the project's Git remotes"
branch: main
commit: 82437e14716a6b765fab65dc7446102e76ba8b26
status: complete
---

# AC-0055 Research: `init` auto-allowlists the project's Git remotes

## Research question

How should `agent-creance init` detect the project's own configured git remotes
and write visible static allow entries (repo clone/fetch host, repo-scoped forge
API host, and forge content/CDN companion hosts) into the committed config —
reusing the existing forge→content-host expansion — with an init-time choice of
read-only vs. push access?

## TL;DR / key findings

1. **The forge expansion the ticket wants to reuse is NOT where the ticket assumes.**
   The ticket says init can "call the same expansion as `allow <repo-url>`". In
   the current tree, `allow <repo-url>` does **no** forge expansion — `ruleFromURL`
   (`internal/cli/mutate.go:46`) builds exactly **one** rule from host+path. The
   real expansion machinery lives in `internal/generator/forge.go`
   (`repositoryRules`, `normalizeRepoURL`, the `forges` table), is **package-private**,
   and returns `generator.Rule` (wrapping `policy.Rule`) — a *different type* from
   the `config.Rule` that init writes. design.md:192 describes `allow <repo-url>`
   expanding to companions as **intent, not yet wired**.

2. **The forge table has no API host.** `forges` (`forge.go:23-37`) maps a repo
   host to web/raw/codeload/pages/release-CDN companions — there is **no
   `api.github.com` entry**. The ticket explicitly wants `api.github.com/repos/<org>/<repo>/`.
   So "reuse the table" cannot, by itself, produce the API rule. The API host must
   come from somewhere new (a separate forge→API map, or an extension of `forges`
   that would also change dependency-generator output). **→ checkpoint question.**

3. **Read-only cannot be expressed by scoping the allow rule alone.** The ticket's
   AC says read-only is "enforced by scoping the rule (path/method)". Research of
   the matcher (`internal/policy/`) shows:
   - **Method scoping can't separate push from fetch** — both git smart-HTTP
     negotiation POSTs (`git-upload-pack`, `git-receive-pack`) are `POST`.
   - **An allow rule scoped to `/<org>/<repo>/` is a path *prefix* that already
     permits push** — it matches `/<org>/<repo>/git-receive-pack` too.
   - The matcher-correct way to block push while allowing fetch is an **extra
     `deny_always`** rule on the receive-pack path (deny shadows allow), and it only
     works on an **intercept** host (path-scoped deny is suppressed on passthrough).
   **→ checkpoint question** (this deviates from the ticket's stated mechanism).

4. **Remote-URL parsing already exists and is reusable** — `normalizeRepoURL`
   (`forge.go:81`) already handles HTTPS, scp-like `git@host:org/repo.git`,
   `ssh://`, `git+`, trailing `.git`/`/`. It extracts `(host, org, repo)`. This is
   exactly the parser the ticket's "Remote-URL parsing across forms" question asks
   for — no new parser needed, just (likely) exporting it.

5. **init is well-positioned mechanically.** init already has a confirm prompt
   (`confirm`, `init.go:183`), interactivity detection (`app.Terminal.IsInteractive()`),
   a `--no-setup` flag, a per-source import-gather pattern (`gatherImports`,
   `init_imports.go:22`), and a fresh-template renderer with a "generators block"
   pattern (`renderConfigTemplate`/`generatorsBlock`, `init.go:343`/`375`) to copy
   for a grouped/commented "git remotes" block. **No new sysdep seam is needed** if
   remotes are read from `.git/config` via `app.FS` (the ticket's preference).

6. **This very repo is an SSH remote** (`git@github.com:tobyS/agent-creance.git`),
   so the SSH-remote edge case (translate to HTTPS forge identity, note SSH
   transport unsupported) is the *primary* path here, not a corner case.

## How init works today (write path the feature extends)

`internal/cli/init.go`:

- `newInitCmd` (`init.go:27`) — flags `--force` (`:37`), `--no-setup` (`:39`); `RunE`
  → `runInit(ctx, app, ".", force, noSetup)` (`:33`).
- `runInit` (`init.go:53`): host-setup gate (skipped by `--no-setup`) → clobber
  guard → `gens := scanGenerators(app.FS, dir)` → (interactive only) `gatherImports`
  → `content := renderConfigTemplate(gens, allow, ports)` → review+`confirm` when
  imports contributed → `writeFileAtomic` → success line + `generatorsNote` →
  `maybeOfferAgentPrompt`.
- **Imports pattern to mirror** — `gatherImports` (`init_imports.go:22`) runs each
  detection source (`claudeimport.Project`, `portscan.Detect`) behind its own
  y/N `confirm`, appends accepted `config.Rule`s into an `allow` slice, and dedupes
  (`dedupeRulesByHost`, `init_imports.go:79` — **dedupes by host only**, which would
  wrongly collapse multiple path-scoped rules on one host; the forge set avoids this
  since each companion host is distinct, but the git-remote rules on a single host
  with different paths must not route through host-only dedupe).
- **Config text rendering** — `renderConfigTemplate` (`init.go:343`) assembles a
  fresh file from a `[]config.Rule`. The "generators block" (`generatorsBlock`,
  `init.go:375`) is the model for a grouped/commented section: section headers and
  comments are **string literals in init.go**; only the bare YAML items come from
  `config.RenderRule(r, indent)` (`internal/config/edit_hostservice.go:22`). A new
  "git remotes" block would be a new branch in `renderConfigTemplate` + `RenderRule`
  for the item bodies.
- **Prompt primitives already present** — `confirm(app, prompt)` (`init.go:183`,
  `[y/N]`, EOF→no), `app.Terminal.IsInteractive()` (`internal/sysdep/terminal.go:22`),
  and the non-interactive refusal pattern (`ensureHostSetup`, `init.go:149`). The
  push prompt reuses `confirm` directly.
- **Output reporting** — success line `✓ Wrote %s %s` with `generatorsNote`
  (`init.go:111-119`/`312`); the feature adds a sibling summary line for git rules.
- **Dependencies** — init uses `app.FS`, `app.Stdout`, `app.Stdin`, `app.Terminal`,
  `app.Paths`; it does **not** use `app.Commander`. Reading `.git/config` via
  `app.FS` preserves the "pure filesystem, no external tools" property.

## The forge machinery to reuse (`internal/generator/forge.go`)

- `forges` table (`forge.go:23-37`): per repo-host companion list. GitHub →
  `github.com`, `raw.githubusercontent.com`, `codeload.github.com`, `<org>.github.io`,
  `objects.githubusercontent.com` (host-wide, `lowerTrust`). GitLab → `gitlab.com`,
  `<org>.gitlab.io`. **No API host. Exact-host-match only** (`forges[host]`) — no
  suffix/enterprise matching, so `github.mycorp.com` won't match.
- `repositoryRules(repoURL, src) []Rule` (`forge.go:43`): known forge → one rule per
  companion (path templated to `/<org>/<repo>/`); unknown host → single bare rule
  `{Host, Paths:["/org/repo/"]}`. Returns `generator.Rule` (`generator/rule.go:31`,
  embeds `policy.Rule` + `Source` + `LowerTrust`).
- `normalizeRepoURL(raw) (host, org, repo, ok)` (`forge.go:81`): the reusable
  URL parser (HTTPS, scp `git@`, `ssh://`, `git+`, `.git`/`/` trimming). `ok=false`
  for <2 path segments. host lower-cased; org/repo verbatim.
- **Type bridge gap**: only `config → policy` conversion exists
  (`policy.RuleFromConfig`, `internal/policy/policy.go:154`). There is **no**
  `generator.Rule`/`policy.Rule` → `config.Rule` converter. Reusing `repositoryRules`
  to feed init's `[]config.Rule` requires either exporting `repositoryRules` +
  writing a small `policy.Rule → config.Rule` adapter, or adding a new exported
  `generator` entry point that returns config-shaped data. `LowerTrust` has no
  `config.Rule` home (it would be dropped when written to YAML).

## Matcher semantics that constrain read-only (`internal/policy/`, enforcer port)

The matcher exists in Go (`internal/policy/decide.go`, `glob.go`) and a byte-for-byte
Python port (`internal/proxy/enforcer/policy.py`); both take `(host, path, method)`.

- **Host** (`glob.go:10`): case-insensitive; `*` any; `*.suffix` (apex excluded);
  else exact. `github.com` matches only `github.com` (companions need their own rules).
- **Path** (`glob.go:30-63`): **prefix-by-segment**. Pattern segments must match a
  prefix of request segments; once pattern is exhausted, the rest is "under" it. So
  `Paths:["/org/repo/"]` matches `/org/repo/info/refs`, `/org/repo/git-upload-pack`,
  **and** `/org/repo/git-receive-pack`. `*` globs within a segment (never crosses `/`);
  `**` crosses segments; `?` is **literal** (not a wildcard).
- **`.git` subtlety**: segments are literal. `/org/repo/` (segs `org`,`repo`) does
  **not** match `/org/repo.git/...` (`repo` ≠ `repo.git`). git requests the path as
  configured in the remote URL — a remote stored **with** `.git` hits `/org/repo.git/…`
  and is **not** covered by a `/org/repo/` allow. So the written allow paths must
  cover **both** `/<org>/<repo>/` and `/<org>/<repo>.git/` (implementation detail —
  not a user decision, but a correctness must-have, verifiable in integration tests).
- **Method** (`glob.go:99`): nil methods → any; else verbatim **case-sensitive**
  membership. Present-but-empty `[]` → matches nothing. Both push and fetch POSTs are
  `POST`, so method scoping can't separate them.
- **Query string**: the enforcer feeds `flow.request.path` *including* the query
  (`enforcer.py:203`), so `?service=git-receive-pack` rides in the last segment as a
  literal — matchable but **untested by the cross-language corpus**; rely on the
  `git-receive-pack` POST path, not the query, for blocking.
- **Precedence** (`decide.go:15-42`): `deny_always` shadows `allow` (independent of
  list order; most-specific within each list). A request matching nothing →
  soft-deny. **Exception**: if the best-matching allow is `mode: passthrough`,
  path-scoped denies are suppressed — so a push-block deny only works on an
  **intercept** host (git over HTTPS to github.com is intercept by default — fine).

**Consequence for read-only:** the only matcher-correct enforcement is
`allow repo (+companions)` **plus** a `deny_always` on the push endpoint path
(`/<org>/<repo>/git-receive-pack` and `/<org>/<repo>.git/git-receive-pack`, or a glob
`**/git-receive-pack`). Method-only scoping on the allow rule cannot do it.

## design.md anchors

- **Forge content hosts** (design.md:185-192): the table description; line 192 states
  the table "is what `agent-creance allow <repo-url>` consults, so a manual repo allow
  expands to the same companion set" — **aspirational** vs. current `allow` code.
- **Own-repo example**: YAML uses `api.github.com` + `paths:["/repos/tobyS/this-project/"]`
  + `methods:[GET, POST]` (design.md:138-141); `policy show` renders
  `allow github.com /repos/tobyS/this-project/` (design.md:226). These are
  hand-authored `[explicit]` rules — exactly what this ticket automates.
- **S4 / AC-0004** (design.md:22): "git over SSH … unsupported in v0.1 — HTTPS
  remotes only"; git-over-HTTPS honors the proxy + `GIT_SSL_CAINFO`. No mention of
  `git-receive-pack`/`git-upload-pack`/push anywhere in the doc.
- **init** (design.md:404-414): manifest scan + interactive imports + confirm-before-write.
- **Method scoping** is documented on **allow** rules (design.md:141, 255) and
  **rejected on passthrough** (design.md:271-274). `deny_always` examples carry no
  `methods:`.

## Prior art (thoughts/)

- `thoughts/shared/{research,plans}/2026-06-07-AC-0029-init-command.md` — init scaffold.
- `thoughts/shared/{research,plans}/2026-06-07-AC-0030-allow-deny-commands.md` — allow/deny + `ruleFromURL`.
- `thoughts/shared/{research,plans}/2026-06-05-AC-0012-allowlist-generators.md` — the forge table origin.
- `thoughts/shared/{research,plans}/2026-06-10-AC-0038-init-bootstraps-host-setup.md` — `--no-setup`/host-setup gate.
- `thoughts/shared/{research,plans}/2026-06-15-AC-0051-init-setup-dx-imports.md` — `gatherImports` import pattern.
- `thoughts/shared/{research,plans}/2026-06-05-AC-0010-rule-model-matcher.md` — matcher semantics.

## Open questions for the checkpoint

1. **Read-only enforcement shape.** The ticket says "scope the allow rule"; research
   shows that's not expressible — read-only needs an extra `deny_always` on the
   receive-pack path, or a narrower read-only allow that breaks broad host coverage.
2. **Forge API host source.** The shared `forges` table has no API host; sourcing it
   either means a new feature-local forge→API map or extending the shared table
   (which would also grant every *dependency* repo API access).

## Files most relevant to implementation

- `internal/cli/init.go`, `internal/cli/init_imports.go` — write path + import pattern.
- `internal/cli/mutate.go`, `internal/cli/allow.go` — current (non-expanding) `allow`.
- `internal/generator/forge.go`, `internal/generator/rule.go` — forge table + parser to reuse/export.
- `internal/config/config.go`, `internal/config/edit.go`, `edit_hostservice.go` — `config.Rule`, `RenderRule`, `AppendRule`.
- `internal/policy/policy.go`, `decide.go`, `glob.go` — matcher semantics (read-only feasibility).
- `internal/proxy/enforcer/policy.py`, `enforcer.py` — the live matcher port (keep in sync if rules change).
- `internal/cli/testdata/script/init.txtar`, `internal/cli/init_test.go`, `testdata/init/` — test surfaces to update.
</content>
</invoke>
