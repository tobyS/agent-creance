# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`agent-creance` is a macOS-only Go CLI that runs a coding agent inside an isolated, egress-filtered cage by composing `agent-safehouse` (filesystem/process isolation) and `mitmproxy` (TLS-terminating egress allowlist). The full design — architecture, threat model, config schema, commands, roadmap — lives in @docs/design.md. Read it before making non-trivial changes.

## Module & layout

- Module path: `github.com/tobyS/agent-creance`.
- `cmd/agent-creance/main.go` is a thin entrypoint; all logic lives under `internal/`.
- `internal/cli` is the composition root: the `App` struct holds injected dependencies and `cli.Main()` wires the real ones. Commands take `*App`, not globals — this is what makes them testable.

## Commands

- `make test` — fast, hermetic unit + script tests with the race detector (the default check; keep it green).
- `make test-integration` — slow tests that touch real tools, behind the `integration` build tag.
- `make lint` — `go vet` + `golangci-lint` (install the linter with `make tools`).
- `make build` / `make run ARGS="doctor"` — build/run with version metadata stamped via ldflags.
- `make golden` — regenerate golden files; always review the resulting diff.
- **At the end of every ticket, run `make build`** so `bin/agent-creance` reflects the final commit — the user tests with this binary.

## Development runs inside an agent-creance cage

Development on this project is regularly performed by an agent running **inside an
agent-creance cage itself** (dogfooding). The cage is egress-filtered and isolates
filesystem/process; the host-side proxy and `agent-safehouse` sandbox live outside
it. So hermetic work (`make test`, golden, testscript, pure logic, config) runs
fine in-cage, but some operations cannot:

- **Spawning the host-side proxy / sandbox** — `make test-integration` and anything
  that stands up a real `mitmproxy` or `agent-safehouse` (the cage's own egress
  already routes through such a proxy; you can't nest it).
- **Real external services that aren't plain allowlistable HTTPS, or that are the
  thing under test** — e.g. real GitHub GraphQL, GitHub App / OAuth token minting.

When the current development task needs something executed **outside** the cage:

1. **Batch it, best effort.** Collect all out-of-cage operations you can foresee for
   the task into one set rather than asking for repeated breakouts.
2. **Ask the user to close the cage** for that batch, and state exactly what will be
   run while it's down.
3. **Tell the user when the cage can be re-activated** — i.e. once the out-of-cage
   batch is done and you're back to hermetic work.

Plan the work so in-cage (hermetic) and out-of-cage (integration / real-service)
phases are separated, minimizing how often the cage must be toggled.

## Testing conventions (follow these — the project is built around them)

- **Never call the OS directly from logic packages.** Process execution, keychain, clock, filesystem go through interfaces in `internal/sysdep`, injected at construction. Tests use the fakes in `internal/sysdep/sysdeptest`. New side-effecting dependencies get a new interface there, not a direct `os/exec`/keychain call inline.
- **Pure logic → table-driven tests** (see `internal/prereq/version_test.go`).
- **Generated artifacts (`.sb`, `policy.json`, reports) → golden-file tests** with a `-update` flag (see `internal/prereq/report_test.go`). Golden files live in `testdata/`.
- **CLI behavior → testscript** `.txtar` files under `internal/cli/testdata/script/` (rogpeppe/go-internal). Keep them hermetic: stub external tools on `PATH`; use `$CREANCE_BIN` (set in the test's `Setup`) to build a minimal PATH that finds the CLI but not host tools.
- External tools (`agent-safehouse`, `mitmproxy`, `security`) are **never** invoked in unit tests — only in `//go:build integration` tests.

## Gotchas

- Security-critical runtime state (compiled policy, audit log, lock file) is kept **out of the source tree** on purpose (in `~/.cache/agent-creance/`), because the cage mounts `./` read-write. Don't move these in-tree.
- Tested-against versions of external tools are constants in `internal/buildinfo`; bump them per release when re-tested.
- `git commit` flow: this repo follows the user's global convention — write the message to `.claude-commit` (gitignored) and run `git commit -F .claude-commit`. Don't chain commands with `&&`.
