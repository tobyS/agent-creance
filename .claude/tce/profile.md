# Project Profile

> Read by the tce workflow commands and research agents at runtime. `/tce:init`
> seeds this file and fills it in; keep it accurate. If the stack, layout, or
> commands change, update this file (or re-run `/tce:init`).

## Tech stack

Go 1.26 CLI, single module (`github.com/tobyS/agent-creance`) — not a monorepo.
macOS-only. Composes `agent-safehouse` (filesystem/process isolation) and
`mitmproxy` (TLS-terminating egress allowlist) at runtime. Key libraries: spf13/cobra
(command tree), stretchr/testify (assertions), rogpeppe/go-internal/testscript (CLI
tests). Lint via golangci-lint. No datastore; security-critical runtime state lives
out-of-tree in `~/.cache/agent-creance/`.

## Commands

Always run from the repo root (`/Users/toby/code/work/agent-creance`).

- **Test:** `make test` (= `go test -race ./...`; fast, hermetic). Slow real-tool
  tests: `make test-integration` (= `go test -race -tags=integration ./...`).
- **Typecheck:** `go build ./...` (Go has no separate typecheck step).
- **Lint/format:** `make lint` (= `go vet ./...` + `golangci-lint run`); format with
  `make fmt` (= `gofmt -s -w .`).
- **Golden files:** `make golden` (= `go test ./... -update`); always review the diff.

## Code map (where things live)

The research agents (`codebase-locator` / `codebase-analyzer` / `codebase-pattern-finder`)
read this to know where to look.

| Kind of code | Location(s) |
|--------------|-------------|
| Entry point (CLI main) | `cmd/agent-creance/main.go` (thin; calls into `internal/cli`) |
| CLI commands + composition root | `internal/cli/` (`App` struct in `cli.go`; commands in `version.go`, `doctor.go`) |
| Application / business logic | `internal/prereq/` (prerequisite detection, version-skew classification, report rendering) |
| Build / version metadata | `internal/buildinfo/` (version info + tested-against tool versions) |
| OS abstraction (testability seam) | `internal/sysdep/` (interfaces + real impls); fakes in `internal/sysdep/sysdeptest/` |
| Unit / table tests | co-located `*_test.go` (e.g. `internal/prereq/version_test.go`) |
| Golden fixtures | `internal/prereq/testdata/` |
| CLI behavior tests (testscript) | `internal/cli/testdata/script/*.txtar` |
| Build/dev tasks | `Makefile`; lint config `.golangci.yml`; git hooks `.githooks/` |
| Design doc | `docs/design.md` (architecture, threat model, config schema, roadmap) |

## Conventions

- **Never call the OS directly from logic packages.** Process execution, keychain,
  clock, filesystem go through interfaces in `internal/sysdep`, injected at
  construction. New side-effecting deps get a new interface there + a fake in
  `sysdeptest/`, not an inline `os/exec`/keychain call.
- Commands take `*App` (injected deps), not globals — that is what makes them testable.
- **Pure logic → table-driven tests.** **Generated artifacts (`.sb`, `policy.json`,
  reports) → golden-file tests** with a `-update` flag. **CLI behavior → hermetic
  testscript** `.txtar` files (stub external tools on `PATH`; use `$CREANCE_BIN`).
- External tools (`agent-safehouse`, `mitmproxy`, `security`) are **never** invoked in
  unit tests — only under the `integration` build tag.
- Security-critical runtime state (compiled policy, audit log, lock file) is kept
  **out of the source tree** (`~/.cache/agent-creance/`) because the cage mounts `./`
  read-write. Don't move these in-tree.
- Tested-against external tool versions are constants in `internal/buildinfo`; bump per
  release when re-tested.
- **Commit flow:** write the message to `.claude-commit` (gitignored) and run
  `git commit -F .claude-commit`. Never chain shell commands with `&&` (breaks the
  permission allowlist and triggers prompts); run `cd` and the command as separate calls.
- Read `docs/design.md` before non-trivial changes.

## Preferred research sources

The `web-search-researcher` agent prioritizes these when doing web lookups for this
project's stack. List authoritative docs as `URL — description`:

- `https://go.dev/doc/` — Go language & toolchain reference
- `https://pkg.go.dev` — Go package documentation (stdlib + dependencies)
- `https://pkg.go.dev/github.com/spf13/cobra` — cobra CLI framework
- `https://pkg.go.dev/github.com/rogpeppe/go-internal/testscript` — testscript (CLI tests)
- `https://pkg.go.dev/github.com/stretchr/testify` — testify assertion/mocking library
- `https://docs.mitmproxy.org` — mitmproxy (TLS-terminating egress filtering)
- `https://agent-safehouse.dev` — agent-safehouse (filesystem/process isolation)

(General sources like MDN are always available; list the *stack-specific* ones here.)
