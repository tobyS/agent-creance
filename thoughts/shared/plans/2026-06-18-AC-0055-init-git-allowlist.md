---
date: 2026-06-18
ticket: AC-0055
title: "Plan — init auto-allowlists the project's Git remotes"
branch: main
research: thoughts/shared/research/2026-06-18-AC-0055-init-git-allowlist.md
status: draft
---

# AC-0055 Plan: `init` auto-allowlists the project's Git remotes

## Overview

Make `agent-creance init` detect the project's configured git remotes and write
visible, static allow entries into the committed `.agent-creance.yaml`: the repo
host (clone/fetch/push), the repo-scoped forge API host, and the forge
content/CDN companion hosts — reusing the existing forge expansion. Whether push
is permitted is an init-time choice (interactive prompt; non-interactive default
read-only; `--git-push` to preset). Read-only is enforced by an additional
`deny_always` rule on the `git-receive-pack` push endpoint.

## Decisions locked at the checkpoint

1. **Read-only = allow + `deny_always` on the push path.** Method scoping can't
   separate push from fetch (both `POST`); the `/<org>/<repo>/` allow prefix
   already permits push. So read-only writes a real `deny_always` entry targeting
   `git-receive-pack`; full access omits it. (Research §3.)
2. **API host = extend the shared `forges` table.** Add `api.github.com` (scoped
   `/repos/<org>/<repo>/`) to the GitHub forge list in `internal/generator/forge.go`.
   This also gives every detected *dependency* repo API access and changes
   generator goldens — accepted.

## Current state

- `internal/cli/init.go` builds a fresh config via `renderConfigTemplate(gens, allow, ports)`
  (`:343`); `generatorsBlock` (`:375`) is the model for a grouped/commented block;
  the deny side is only a `commentedDenyStub` (always-on placeholder). `confirm`
  (`:183`) + `app.Terminal.IsInteractive()` are the prompt primitives. init uses
  `app.FS` only — no `app.Commander`, no git seam.
- `internal/generator/forge.go`: `forges` table (no API host), `repositoryRules`
  (package-private, returns `generator.Rule`), `normalizeRepoURL` (already parses
  HTTPS / scp `git@` / `ssh://` / `git+` / `.git`). `allow <repo-url>` does **not**
  call any of this today.
- Matcher (`internal/policy/`): path = prefix-by-segment (so `/<org>/<repo>/` ≠
  `/<org>/<repo>.git/…` — both forms must be written); `deny_always` shadows `allow`
  on **intercept** hosts; method match is verbatim/case-sensitive.

## Desired end state

`agent-creance init` in a repo with remotes writes (grouped + commented) into the
config: per remote, an allow for the repo host (`/<org>/<repo>/` **and**
`/<org>/<repo>.git/`), the forge API host (`/repos/<org>/<repo>/`), and the forge
content companions; plus, unless push was granted, a `deny_always` on
`…/git-receive-pack` (both path forms). init prints what it added and what it
couldn't infer. No remotes → no git block, no error.

---

## Phase 1 — Extend the forge table with the API host; export the reuse surface

**Files:** `internal/generator/forge.go`, `internal/generator/forge_test.go`,
internal callers of `repositoryRules`/`normalizeRepoURL`, generator/compile goldens.

1. Add to the `github.com` entry in `forges`:
   `{host: "api.github.com", pathTmpl: "/repos/<org>/<repo>/"}`. (GitLab API is
   project-ID-scoped, not org/repo-path-scoped — leave GitLab without an API
   companion; note this in the comment.)
2. Export for reuse from `package cli`:
   - `func RepositoryRules(repoURL, src string) []Rule` (rename `repositoryRules`;
     keep an unexported alias if the dependency-walk caller is tidier that way).
   - `func NormalizeRepoURL(raw string) (host, org, repo string, ok bool)` (rename
     `normalizeRepoURL`). Update all in-package callers.
3. Update `forge_test.go` GitHub expectations (now include `api.github.com`).
4. Run `make golden`; review and commit any compile/render golden changes that
   reference GitHub-generated rules (the accepted blast radius).

**Verify:** `make test` green; `git diff` on goldens shows only the added
`api.github.com` rule wherever GitHub repos are generated.

## Phase 2 — `internal/gitremote` detection package

**Files:** new `internal/gitremote/gitremote.go` + `gitremote_test.go`.

1. `type Remote struct { Name, URL string }` and
   `func Detect(fsys sysdep.FileSystem, dir string) ([]Remote, error)`:
   - Read `<dir>/.git/config` via `fsys.ReadFile`. Absent (`fs.ErrNotExist`) or
     `.git` not a regular config file → return `nil, nil` (no remotes, no error).
   - Parse the git-config INI subset: track `[remote "<name>"]` sections, capture
     their `url = …` value (ignore `pushurl`/other keys for v0.1). Trim quotes/
     whitespace. Return remotes in file order, deduped by name.
   - Pure parsing, no OS calls beyond `fsys` — preserves init's "no external
     tools, no new sysdep seam" property (uses the existing `app.FS`).
2. Table-driven tests: single origin, multiple remotes (origin+upstream+fork),
   SSH `git@` url, no `[remote]` sections, missing file, quoted/odd whitespace,
   `[remote "x"]` with `pushurl` only (skip — no `url`).

**Verify:** `make test` green; parser handles every form in the table.

## Phase 3 — Build git-remote rules (config.Rule) from detected remotes

**Files:** new `internal/cli/init_gitremotes.go` (+ test), reusing Phase 1/2.

1. `gatherGitRemoteRules(app *App, dir string, allowPush bool) (allow, deny []config.Rule, notes []string, err error)`:
   - `remotes, _ := gitremote.Detect(app.FS, dir)`; none → return all-nil.
   - For each remote: `host, org, repo, ok := generator.NormalizeRepoURL(r.URL)`.
     - `!ok` → add a note ("couldn't parse remote <name> (<url>)"); skip.
     - `genRules := generator.RepositoryRules(r.URL, "")`; convert each
       `generator.Rule` → `config.Rule` (`Host`; `Paths` = `&paths` when non-empty;
       drop `LowerTrust`/`Source`). Add a `Reason: "project git remote (<name>)"`.
     - **`.git` coverage:** for the rule whose `Host == host` (the repo web host),
       set `Paths = ["/<org>/<repo>/", "/<org>/<repo>.git/"]` (segment matching is
       literal, so both forms are required for git smart-HTTP).
     - **SSH remote:** handled transparently (NormalizeRepoURL parses `git@`); add a
       one-time note that SSH *transport* stays unsupported (HTTPS remotes only).
     - **Unknown/self-hosted forge** (host not in `forges`): `RepositoryRules`
       returns a single bare repo-host rule; add a note that API/CDN companions
       couldn't be inferred. (Enterprise-host family detection is out of scope.)
   - **Read-only deny:** when `!allowPush`, for each remote's repo host add a
     `config.Rule{Host: host, Paths: ["/<org>/<repo>/git-receive-pack",
     "/<org>/<repo>.git/git-receive-pack"], Reason: "read-only: push (git-receive-pack) blocked"}`.
   - Dedupe by full rule identity (host + path-set), **not host-only**, so multiple
     remotes on the same host with distinct repos coexist. (Don't route these
     through `dedupeRulesByHost`.)

**Verify:** unit tests over a fake FS with `.git/config` fixtures assert the exact
allow/deny `config.Rule` sets for: GitHub HTTPS, GitHub SSH, unknown forge,
read-only vs `allowPush`, two remotes.

## Phase 4 — Wire into `runInit` (flag, prompt, render, report)

**Files:** `internal/cli/init.go`.

1. **Flag:** add `--git-push` (bool, default false) on `newInitCmd`; thread into
   `runInit(... gitPush bool)`.
2. **Push decision** (compute `allowPush`): `--git-push` set → true (no prompt);
   else if `app.Terminal.IsInteractive()` and remotes exist → `confirm(app,
   "Allow the agent to push to your git remote(s)? (write access)")`; else → false
   (read-only default).
3. **Build rules** (runs **always**, not gated on TTY): call
   `gatherGitRemoteRules(app, dir, allowPush)`. Append the allow rules into init's
   `allow` slice (kept grouped — see render) and collect the deny rules.
4. **Render:** extend `renderConfigTemplate` to take git-remote allow rules and
   deny rules. Emit a grouped, commented block like `generatorsBlock`:
   - allow side: a `# Project git remotes — repo, API, content hosts` comment
     header above the git-remote allow items (via `config.RenderRule(r, 6)`),
     distinct from import-derived allow items.
   - deny side: when deny rules exist, render a real `deny_always:` block (comment
     header `# Read-only: block push to project git remotes`) instead of the
     `commentedDenyStub`; otherwise keep the stub.
5. **Report:** after the success line, print a summary: remotes detected, push vs
   read-only, and any `notes` (unparsed remotes, SSH-transport caveat, uninferable
   companions).
6. Keep behavior when there are no remotes identical to today (no git block, stub
   deny, no error).

**Verify:** `make build`; manual `agent-creance init` in a scratch repo (see
manual checks below).

## Phase 5 — Tests: testscript + goldens

**Files:** `internal/cli/testdata/script/init.txtar` (or a new
`init_gitremotes.txtar`), `internal/cli/init_test.go` + `testdata/init/` goldens.

1. Hermetic testscript cases (write a `.git/config` fixture in the test dir; these
   are pure-FS so no real git needed):
   - GitHub HTTPS origin, non-interactive (`--no-setup`) → read-only block written
     (allow repo+`.git`+api+companions, deny `git-receive-pack`); assert on output.
   - `--git-push` → no deny block; allow includes repo host.
   - GitHub SSH `git@` origin → same HTTPS rules + SSH-transport note.
   - Unknown/self-hosted host → bare repo-host allow + "couldn't infer companions"
     note, no api/cdn.
   - Two remotes (origin + upstream) → both expanded, deduped correctly.
   - No `.git` / no remotes → unchanged output (regression guard).
2. Update/extend `init_test.go` golden render for a config containing git-remote
   allow + deny blocks. `make golden`; review diff.

**Verify:** `make test` and `make test` (race) green; goldens reviewed.

## Phase 6 — Docs + ticket close

**Files:** `docs/design.md`, ticket `thoughts/shared/tickets/AC-0055-init-git-allowlist.md`.

1. design.md:
   - "Forge content hosts" (≈185-192): add `api.github.com/<org>/<repo>` (scoped
     `/repos/…`) to the GitHub companion list; note the table now feeds dependency
     generators *and* init's own-remote allowlisting.
   - "The `init` command" / Commands block (≈404-414): document remote detection,
     the repo/API/content allow entries, the `--git-push` flag, the read-only
     default + `deny_always` push block, and the SSH-transport / unknown-forge notes.
   - Adjust the own-repo example commentary: the `api.github.com /repos/tobyS/this-project/`
     rule is now auto-written by init rather than hand-added.
2. Append a dated note to the ticket's `## Notes & Updates` and set
   `**Status:** Done` + `**Updated:** 2026-06-18` once all phases verify.

---

## Success criteria

**Automated** (from profile.md):
- `make test` (race) green.
- `make lint` (`go vet` + `golangci-lint`) clean.
- `make build` produces `bin/agent-creance`.
- Goldens regenerated and reviewed (`make golden` diff limited to the intended
  `api.github.com` additions + the new init render blocks).

**Manual:**
- In a scratch GitHub HTTPS repo: `agent-creance init` (decline push) writes the
  repo/API/content allow block + a `git-receive-pack` `deny_always`, and reports it.
- Re-run with `--git-push`: no deny block; with `--no-setup` non-interactive:
  read-only by default.
- SSH-remote repo: HTTPS forge/API/content hosts written, SSH-transport note shown.
- Unknown-host remote: bare host allow + "couldn't infer companions" note.
- Repo with no remotes: output unchanged from today; no error.

## Risks / notes

- **Golden blast radius** (accepted): extending `forges` changes every
  GitHub-repo generator output. Keep the diff reviewable; `make golden` then read it.
- **`.git` path duplication** is load-bearing — segment matching is literal, so
  both `/<org>/<repo>/` and `/<org>/<repo>.git/` forms are required for the repo
  host (allow and deny).
- **Read-only scope is git-receive-pack only** — REST API write methods are not
  restricted (consistent with the ticket's definition of "push"); document it.
- **deny only bites on intercept hosts** — github.com over HTTPS is intercept by
  default, so the push block is effective; record this assumption in design.md.
- Enterprise/self-hosted forge *family* detection stays out of scope (exact-host
  match only) — such hosts get the bare-host fallback.
</content>
