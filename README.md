# agent-creance

A single command to start a coding agent (Claude Code or any other) inside an
isolated, egress-filtered cage on macOS — composed from
[agent-safehouse](https://agent-safehouse.dev/) (filesystem + process isolation)
and [mitmproxy](https://mitmproxy.org/) (TLS-terminating egress allowlist),
configured by one `.agent-creance.yaml` file.

> **Status:** early development (pre-v0.1), under active development. The v0.1
> core is implemented: `setup`, `init`, and `run` work end to end, along with the
> egress-policy commands (`allow`/`deny`/`policy`/`import`), `doctor`, `status`,
> and `logs`. The full design lives in [`docs/design.md`](docs/design.md).

## Requirements

- Go 1.26+ (to build/install)
- macOS (v0.1 is macOS-only)
- For running a cage: `agent-safehouse` and `mitmproxy` on `PATH`. `setup` and
  `run` check for these and tell you how to install any that are missing.

## Quickstart

You need Go 1.26+ and, to actually run a cage, `agent-safehouse` and `mitmproxy`
on your `PATH` (macOS only — see [Requirements](#requirements)). Install
`agent-creance` (see [Install](#install) below), then:

```sh
agent-creance setup          # once per machine: trust the mitmproxy CA, install
                             #   the skill, and scaffold the global config
cd your-project
agent-creance init           # once per project: write .agent-creance.yaml
agent-creance run            # start the cage and your agent inside it
```

`setup` is a one-time, per-machine step; `init` is one-time per project. If you
skip them, `run` won't fail with a stack trace — it refuses early with a pointer
to whichever command you still need. For the full command reference, see
[`docs/design.md`](docs/design.md).

## Install

Both methods below install the `agent-creance` binary into `go env GOPATH`/bin
(usually `~/go/bin`). Make sure that directory is on your `PATH` — if it isn't,
add it: `export PATH="$PATH:$(go env GOPATH)/bin"`.

```sh
# Quickest — no clone:
go install github.com/tobyS/agent-creance/cmd/agent-creance@latest

# From source (stamps the build version into `agent-creance version`):
git clone https://github.com/tobyS/agent-creance.git
cd agent-creance
make install
```

The `go install` build reports its version as `dev` (cosmetic — it doesn't affect
how the cage runs); the `make install` build stamps the real version.

## Egress baseline

`agent-creance setup` scaffolds a global config at `~/.config/agent-creance.yaml`
with the "always-allowed" baseline a caged Claude agent needs — the Claude Code
API/OAuth hosts plus Anthropic's public documentation hosts, so routine "how does
Claude Code X work" lookups don't get soft-denied. `setup` never modifies an
existing global config; it only writes the file when none is present.

If your global config predates the documentation hosts (added in AC-0048), add
them under `network.egress.allow` to read Anthropic's docs from inside the cage.
They are credential-free and scoped to `GET` (`code.claude.com` to `/docs`; the
two legacy hosts redirect to the already-allowed `platform.claude.com`):

```yaml
network:
  egress:
    allow:
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

## First-run config (init imports)

`agent-creance init` scaffolds a project `.agent-creance.yaml`. On an interactive
terminal it also offers to do the tedious allowlist/port wiring for you — each as
its own yes/no prompt, all auto-skipped when there is no TTY (so CI behaves as
before):

- **Import allowed web domains** from the project's Claude Code settings
  (`WebFetch(domain:…)` in `.claude/settings.json` / `settings.local.json`, plus
  `sandbox.network.allowedDomains`) as GET-only `intercept` rules.
- **Import MCP servers** from `.claude/settings*.json`, `.mcp.json`, and
  `~/.claude.json`: remote servers become `passthrough` allow rules; an MCP server
  bound to `localhost` becomes a `host_services` port.
- **Detect dev ports** from `docker-compose.yml`, `package.json` scripts,
  `Procfile`, and `.env`.

It then shows the resulting config and asks you to confirm before writing.
`agent-creance setup` does the same for the global baseline, seeding it from your
*global* `~/.claude` config when it first creates `~/.config/agent-creance.yaml`.

For everything that can't be inferred statically, `init` offers to print a prompt
you hand to your agent; the agent writes a config fragment (documentation hosts,
remaining ports) which you review and merge with:

```sh
agent-creance import agent-creance.suggested.yaml   # add --yes for non-interactive use
```

`import` strict-validates the fragment, shows the merged result, and writes only
on confirmation.

## Using GitHub (`gh`) in the cage

agent-creance can inject a GitHub token into the cage's requests so the agent
authenticates **without ever holding the token** — you register a reference
(`op://…`, `keychain://…`, or `env://…`), and the proxy resolves it host-side and
overwrites the `Authorization` header at egress. Register one with:

```sh
agent-creance credential add github --source op://Private/GitHub/token --bearer
```

Use a **fine-grained** PAT scoped to the one repo you want the agent to touch
(Repository access → that repo only; Metadata: Read, Issues: Read and write, and
Contents: Read). Then bind it to GitHub's REST API, scoped to your repo:

```yaml
network:
  egress:
    allow:
      - host: api.github.com
        paths: ["/repos/OWNER/REPO"]
        methods: [GET, POST, PATCH, PUT, DELETE]
        inject: github
env:
  # gh won't send a request when it thinks it's logged out; the proxy overwrites
  # this placeholder. It is NOT a secret and NOT a boundary.
  GH_TOKEN: "ghp_phantom_the_proxy_overwrites_this"
```

> ### ⚠ Do not casually open `api.github.com/graphql`
>
> `gh`'s porcelain (`gh issue`, `gh pr`, `gh repo`) runs over **GraphQL**, a
> single endpoint (`POST api.github.com/graphql`) whose target repo lives in the
> request body — which the egress filter cannot see. **A repo-scoped token does
> not help here:** it bounds *writes* and *private* reads, but every public repo
> is world-readable, so opening `/graphql` lets the agent read **any** public
> repo's issues, PRs, and files. That is an unbounded, attacker-controllable
> content channel — exactly the "read malicious content" vector the cage exists
> to close, and GitHub is where an attacker would plant it.
>
> **The safe way** is the scoped-REST config above: the agent works with issues
> via REST endpoints under `/repos/OWNER/REPO/…` (call them with
> `gh api /repos/OWNER/REPO/issues …` or `curl`, and tell your agent to prefer
> them in `CLAUDE.md`). `gh`'s porcelain commands won't work, and `gh auth
> status` reports a spurious "token invalid" (it pings an un-scopable root path) —
> but the agent cannot read repos you didn't allow. Only open `/graphql` if you
> understand and accept that it removes that guarantee.

The full model — the two auth axes, overwrite/fail-closed semantics, the `472`
refusal, and per-project scoping — is in
[`docs/design.md`](docs/design.md) under "Credential injection".

## Shell completion

`agent-creance` ships tab-completion scripts for bash, zsh, fish, and
PowerShell (generated by `agent-creance completion <shell>`). To try it in the
current session:

```sh
source <(agent-creance completion zsh)   # or: bash
```

To enable it for every new session (macOS, Homebrew paths):

```sh
# zsh — needs `autoload -U compinit; compinit` in ~/.zshrc
agent-creance completion zsh > $(brew --prefix)/share/zsh/site-functions/_agent-creance

# bash — needs the bash-completion package
agent-creance completion bash > $(brew --prefix)/etc/bash_completion.d/agent-creance
```

Start a new shell for the change to take effect. Run
`agent-creance completion <shell> --help` for fish/PowerShell and Linux paths.

## Development

```sh
make help     # list all tasks
make test     # fast unit + CLI tests, race detector on
make lint     # go vet + golangci-lint (run `make tools` once to install the linter)
make hooks    # install the git pre-commit hook (gofmt + vet + tests)
make build    # build ./bin/agent-creance with version metadata
make run ARGS="doctor"
```

### How the code is organized

- `cmd/agent-creance/` — tiny `main`, just calls into `internal/cli`.
- `internal/cli/` — cobra command tree and the `App` composition root.
- `internal/buildinfo/` — version metadata + tested-against tool versions.
- `internal/prereq/` — prerequisite detection and version-skew classification.
- `internal/sysdep/` — interfaces over the OS (the testability seam) and the
  real implementations; `sysdeptest/` holds the test fakes.

### Testing approach

Logic never touches the OS directly — it goes through `internal/sysdep`
interfaces so tests inject fakes. Pure logic is covered by table-driven tests,
generated artifacts by golden files (`-update` to regenerate), and end-to-end
CLI behavior by [testscript](https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript)
`.txtar` scenarios. Anything that shells out to the real `agent-safehouse` /
`mitmproxy` is gated behind the `integration` build tag (`make test-integration`).

## License

Apache-2.0.
