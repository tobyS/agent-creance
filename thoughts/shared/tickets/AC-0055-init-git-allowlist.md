# AC-0055: `init` auto-allowlists the project's Git remotes (repo + API + content hosts)

**Status:** Done
**Estimated Complexity:** Medium
**Created:** 2026-06-18
**Updated:** 2026-06-19

## Problem Statement

`agent-creance init` seeds the egress policy from detected package manifests: its
generators already emit forge "companion hosts" (`github.com/<org>/<repo>/`,
`raw.githubusercontent.com/…`, `codeload.github.com/…`, `objects.githubusercontent.com`)
for the project's *dependencies'* repos, via a forge→content-host table that
`agent-creance allow <repo-url>` also consults (docs/design.md "Forge content
hosts"). But the project's **own** git remote is never auto-added — today you
hand-`allow` it (design.md shows `allow github.com /repos/tobyS/this-project/` as
an explicit, manually-added rule).

The result: a freshly-initialized project gives the caged agent egress to its
dependencies' forges but not to its *own* repository's clone/API/content hosts.
The agent can't fetch its own repo, use the forge API (`gh`, PR tooling), or push
until the user manually adds those rules — a predictable, repeatable gap that
`init` is well placed to close, since the forge table needed already exists.

## Desired Outcome

`init` detects **all configured git remotes**, identifies each remote's forge, and
writes **visible static allow entries into the committed config** (like the
generators block) — reusing the existing forge→content-host table — scoped to each
`<org>/<repo>`:

- the repo host for clone/fetch (and push, see below),
- the forge API host **scoped to the repo path** (`api.github.com/repos/<org>/<repo>/`),
- the forge content/CDN companion hosts (raw, codeload, release assets, …).

Whether write access (push) is granted is an **init-time choice**: the operator is
asked, defaulting to the safe (read-only) option when running non-interactively.

## User Stories / Use Cases

- As a developer initializing a project, I want my repo's clone/fetch/API hosts
  added automatically, so the caged agent can work with my own repository without
  me hand-writing allow rules.
- As a developer who works in forks, I want all my configured remotes (origin,
  upstream, fork) allowlisted, so a fork-and-PR workflow works out of the box.
- As a security-conscious operator, I want to decide at init whether the agent may
  push to my repo, so write access is a conscious choice rather than an automatic
  grant.
- As a developer on a self-hosted or SSH remote, I want init to do something
  sensible (add what it safely can) and tell me what it couldn't infer, rather
  than silently skipping or guessing wrong.

## Acceptance Criteria

- [x] `init` detects all configured git remotes and, for each, writes allow
      entries into the committed config: the repo host (clone/fetch), the forge
      API host scoped to `/repos/<org>/<repo>/`, and the forge content companion
      hosts — reusing the existing forge→content-host table.
- [x] The written entries are visible in the generated config (commented/grouped
      like the generators block) and reported in init's output.
- [x] Whether push (write) is allowed is an init-time prompt; non-interactive runs
      (`--no-setup`/CI, or a preset flag) default to **read-only**, with a flag to
      preset the choice (`--git-push`).
- [x] Read-only is enforced by scoping the rule (path/method) so git-receive-pack
      (push) is not permitted; full access additionally permits push to the repo.
      (Implemented as a `deny_always` on the receive-pack path — research found the
      allow rule alone can't express it; both POSTs share the repo prefix and method.)
- [x] An SSH remote (`git@host:org/repo.git`) is translated to its forge identity
      and gets the HTTPS forge/API/content hosts; init notes that SSH git
      *transport* itself remains unsupported (HTTPS remotes only, per v0.1).
- [x] An unknown/self-hosted forge host gets a bare allow for the remote host
      scoped to `<org>/<repo>` (clone/fetch/push as chosen); API/CDN companions are
      omitted and init notes they couldn't be inferred.
- [x] No configured remotes → nothing is added and init does not error.
- [x] `make test`, `make lint` pass; `make build` at the end.

## Out of Scope

- Broadening forge API access beyond the repo path — `gh`'s `/graphql` and `/user`
  calls will need a manual allow (accepted trade for repo-scoped API).
- Supporting git-over-SSH *transport* through the cage (unsupported in v0.1).
- A dynamic "git generator" that recomputes hosts each compile — these are static
  entries written once at init (a generator is a possible later evolution).
- Retrofitting already-initialized configs — this is `init`-time behavior; re-run
  needs `--force` (existing init semantics).

## Open Questions

None blocking — push is an init-time prompt, API is repo-scoped, and init adds all
configured remotes were all settled during authoring.

## Questions for Research/Planning

- [ ] Detect remotes by reading `.git/config` via `app.FS` vs. invoking `git`
      — init is currently "pure filesystem work, no external tools, no new sysdep
      seam" (init.go); reading `.git/config` preserves that.
- [ ] Exact push-control surface: prompt wording, the preset flag's name, and the
      non-interactive default (proposed: read-only).
- [ ] How read-only is expressed in a Rule (path scoping of
      `info/refs?service=git-receive-pack` / `git-receive-pack`, and/or method
      constraints) given git smart-HTTP's path/verb layout.
- [ ] Reuse surface of the existing forge→content-host table and the
      `allow <repo-url>` expansion: can init call the same expansion per remote?
- [ ] Remote-URL parsing across forms (HTTPS, SSH `git@`, `ssh://`, with/without
      `.git`, subgroups on GitLab) to extract host + `<org>/<repo>`.
- [ ] Enterprise/self-hosted forge detection (e.g. `github.mycorp.com`) — can the
      forge family be recognized beyond the canonical public hosts?

## References

- `internal/cli/init.go` — the init scaffold + generator detection this extends.
- `docs/design.md` "Forge content hosts" (≈ lines 176–183) — the forge→content-host
  table reused here; and the explicit `allow github.com /repos/tobyS/this-project/`
  example (≈ line 217) this automates.
- `internal/cli/mutate.go`, `internal/cli/allow.go` — `allow <repo-url>` expansion
  that consults the same forge table.
- `docs/design.md` S4/AC-0004 — HTTPS remotes only; git-over-SSH unsupported in v0.1.
- Related: AC-0029 (init command), AC-0038 (init bootstraps host setup), AC-0030
  (allow/deny + forge expansion).

## Implementation Plan

See `thoughts/shared/plans/2026-06-18-AC-0055-init-git-allowlist.md` (research:
`thoughts/shared/research/2026-06-18-AC-0055-init-git-allowlist.md`). Implemented in
six phases: forge-table API host + exported expansion; `internal/gitremote`
.git/config parser; `config.Rule` allow/deny builder; init wiring (`--git-push`,
prompt, grouped render, report); tests; docs.

## Notes & Updates

### 2026-06-18

- Created from "include the project's Git access (repo, API, etc.) in the
  allow-list at init".
- Decisions: (a) add **all configured remotes**, not just origin; (b) forge API
  is **repo-scoped** (accepting that `gh` /graphql,/user need a manual allow);
  (c) **push is an init-time prompt**, safe default read-only when non-interactive.
- Edge handling agreed: SSH remote → add HTTPS forge/API/content hosts, SSH
  transport stays unsupported; unknown/self-hosted forge → bare remote-host allow,
  no inferable companions; no remotes → no-op.
- Complexity Medium: the forge→content-host table and `allow <repo-url>` expansion
  already exist; the new work is remote detection (ideally via `.git/config` to
  keep init tool-free), the push prompt/flag, and the read-only rule scoping.

### 2026-06-19 — Done

- Implemented per the plan. Two research findings shaped the design and were
  confirmed at the checkpoint: (1) `allow <repo-url>` does **not** actually do forge
  expansion today — the machinery lives in `internal/generator` (now exported as
  `RepositoryRules`/`NormalizeRepoURL`); (2) read-only **cannot** be expressed by
  scoping the allow rule (method scoping can't separate push from fetch — both POST —
  and the `/<org>/<repo>/` prefix already permits push), so it is enforced by a
  generated `deny_always` on the `git-receive-pack` endpoint.
- Decisions taken at the checkpoint: read-only via allow + `deny_always`; the forge
  **API host was added to the shared `forges` table** (so dependency repos also gain
  `api.github.com` access — generator goldens updated accordingly).
- Detection reads `.git/config` via `app.FS` (new `internal/gitremote` package) — no
  external tools, no new sysdep seam, as intended.
- Commits: c065a62 (forge table/export), 6093488 (gitremote), dd685a3 (rule builder),
  6e5e695 (init wiring), 8051afd (tests), plus this docs commit.
