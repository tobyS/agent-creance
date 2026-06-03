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
