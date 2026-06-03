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
