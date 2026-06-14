# agent-creance

A single command to start a coding agent (Claude Code or any other) inside an
isolated, egress-filtered cage on macOS — composed from
[agent-safehouse](https://agent-safehouse.dev/) (filesystem + process isolation)
and [mitmproxy](https://mitmproxy.org/) (TLS-terminating egress allowlist),
configured by one `.agent-creance.yaml` file.

> **Status:** early development (pre-v0.1). The full design lives in
> [`docs/design.md`](docs/design.md). What's implemented today is the project
> skeleton plus the `version` and `doctor` (prerequisite/version-compatibility)
> commands.

## Requirements

- Go 1.26+
- macOS (v0.1 is macOS-only)
- For actually running a cage (not yet wired up): `agent-safehouse` and
  `mitmproxy` on `PATH`.

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
