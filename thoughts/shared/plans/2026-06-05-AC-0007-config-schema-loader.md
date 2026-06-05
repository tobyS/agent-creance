---
date: 2026-06-05
ticket: AC-0007
topic: "Config schema & loader (WP-1.2) — implementation plan"
status: ready
branch: main
git_commit: 1da3b78
research: thoughts/shared/research/2026-06-05-AC-0007-config-schema-loader.md
---

# AC-0007 — Config schema & loader (WP-1.2): Implementation Plan

## Overview

Build `internal/config`, a pure package that turns the bytes of one
`.agent-creance.yaml` document into a validated, typed `*Config`. It decodes
**strictly** (unknown keys are errors), applies the `mode` default, validates the
schema (notably: a `passthrough` rule must not carry `paths`/`methods`), and returns
a stable, human-readable, golden-tested error on failure. It performs **no**
filesystem access and does **not** resolve `include:` files — those are AC-0008.

## Decisions (from research + the planning checkpoint)

1. **`host_services` is typed `[]HostService{Label string, Port int}`** (confirmed at
   checkpoint). Each `label:port` string is parsed and the port range-validated
   (1–65535) in this package. The `127.0.0.1` address-forcing stays in AC-0014.
2. **Strict / fail-closed parsing** (confirmed at checkpoint): `yaml.v3`
   `Decoder.KnownFields(true)`. A misspelled/unknown key is a validation error.
3. **No `UnmarshalYAML`; defaults via a separate pass.** `KnownFields(true)` is
   defeated by any custom `UnmarshalYAML` (go-yaml #642), so the pipeline is: strict
   decode → `applyDefaults()` → `validate()`. The `host_services` `label:port`
   parsing also happens in this post-decode pass (the YAML field decodes as
   `[]string`, then is parsed into `[]HostService`), to keep `KnownFields` intact.
4. **Pointer fields `*[]string` for rule `Paths`/`Methods`** so "omitted" is
   distinguishable from "empty list" — the passthrough check tests presence (`!= nil`),
   not length.
5. **Reformat yaml/validation errors into our own stable messages** (golden-tested),
   never golden the raw `yaml.v3` strings (they embed Go type names like
   `config.Config` and are brittle). A `ValidationError` type aggregates issues.
6. **Filesystem-free `Parse([]byte)` boundary.** Keeps "Depends on: none" true and the
   package perfectly hermetic; AC-0008 adds path/include handling atop it with the
   `FileSystem` seam from AC-0009. Tests read fixtures with `os.ReadFile` (test code).
7. **All top-level sections optional.** A project config may carry only deltas; the
   global baseline may be `include:`-only. No required-field validation.
8. **Validation scope (v0.1):** passthrough⊕paths/methods; `mode ∈ {intercept,
   passthrough}`; per-rule `host` non-empty; `host_services` `label:port` format +
   port range. **Not** validated: HTTP method names, glob syntax, path semantics.

## Current state

- No `internal/config` package exists. No YAML parsing anywhere in the repo.
- `gopkg.in/yaml.v3 v3.0.1` is in `go.mod:18` as `// indirect` only — must be promoted
  to a direct `require` (it stays the same version; `go mod tidy` reclassifies it).
- Patterns to mirror:
  - `internal/state/state.go` — small struct, `fmt.Errorf("pkg: …: %w", err)` errors,
    white-box stdlib tests.
  - `internal/prereq/report.go` + `report_test.go` — pure `Render(...) string` via
    `strings.Builder` with byte-stable constants; the `var update = flag.Bool(...)`
    golden idiom with `testdata/*.golden` and `require.Equal(want, got)`.
  - `internal/prereq/version_test.go` — table-driven `t.Run` tests.

## Desired end state

- `internal/config` package with:
  - Typed structs: `Config`, `Agent`, `Safehouse`, `Network`, `HostService`, `Egress`,
    `Rule`.
  - `func Parse(data []byte) (*Config, error)` — strict decode → defaults → validate.
  - A `ValidationError` type rendering one or more issues into a stable, type-name-free
    message.
- Tests:
  - `testdata/example.yaml` (copied from `docs/design.md`) parses with **no error**
    under strict mode + field-value assertions (the round-trip / no-loss criterion).
  - Table-driven validation tests covering: `mode` default, passthrough+paths error,
    passthrough+methods error, bad `mode` value, empty host, bad `host_services` port,
    unknown top-level key, unknown nested key.
  - Golden error tests for representative invalid fixtures (`testdata/*.golden`).
- `go build ./...`, `go test -race ./...`, `make lint`, `make golden` (no unexpected
  diff) all green.
- **No `cli.App` wiring** — `internal/config` has no command consumer yet (the `run`/
  `init` commands that call it arrive in later phases; don't wire unused deps).

## What we are NOT doing

- No `include:` resolution or config merge (AC-0008).
- No filesystem reading / path canonicalisation / global implicit include (AC-0008 +
  the AC-0009 `FileSystem` seam).
- No generator execution or policy/Seatbelt compilation (Phase 2).
- No `127.0.0.1` address-forcing for host services (AC-0014).
- No CLI command, no `agent-creance init` template writing (later phases).

---

## Phase 1 — Schema types, strict loader, and the example round-trip

### Changes

**`go.mod` / `go.sum`** — promote `gopkg.in/yaml.v3` to a direct dependency
(`go mod tidy` after the first import; verify it drops the `// indirect` comment).

**`internal/config/config.go`** — the typed schema + `Parse`:

```go
// Package config parses one .agent-creance.yaml document into a validated, typed
// Config. It is pure: it never touches the filesystem and does not resolve include:
// directives (that is AC-0008). Decoding is strict — unknown keys are errors.
package config

type Config struct {
	Agent     Agent             `yaml:"agent"`
	Safehouse Safehouse         `yaml:"safehouse"`
	Include   []string          `yaml:"include"`
	Network   Network           `yaml:"network"`
	Env       map[string]string `yaml:"env"`
}

type Agent struct {
	Command []string `yaml:"command"`
	Workdir string   `yaml:"workdir"`
}

type Safehouse struct {
	AddDirsRW []string `yaml:"add_dirs_rw"`
	AddDirsRO []string `yaml:"add_dirs_ro"`
	Enable    []string `yaml:"enable"`
}

type Network struct {
	HostServices []HostService `yaml:"host_services"`
	Egress       Egress        `yaml:"egress"`
}

// HostService is one "label:port" entry. Label is cosmetic; the address is forced
// to 127.0.0.1 downstream (AC-0014). Parsed from the raw string in applyDefaults.
type HostService struct {
	Label string
	Port  int
}

type Egress struct {
	Generators []string `yaml:"generators"`
	Allow      []Rule   `yaml:"allow"`
	DenyAlways []Rule   `yaml:"deny_always"`
}

// Rule is one egress allow/deny entry. Paths/Methods are pointers so an omitted key
// (nil) is distinct from an empty list — required by the passthrough validation.
type Rule struct {
	Host    string    `yaml:"host"`
	Paths   *[]string `yaml:"paths"`
	Methods *[]string `yaml:"methods"`
	Mode    string    `yaml:"mode"`
	Reason  string    `yaml:"reason"`
}
```

`HostService` needs custom decode handling, but we must keep `KnownFields(true)`
working on the **outer** structs. Approach: decode `host_services` as a raw
`[]string` into an unexported shadow field on `Network` (or decode into a parallel
`rawConfig` mirror), then parse to `[]HostService` in `applyDefaults`. The plan's
concrete choice: give `Network` a private `rawHostServices []string` with the
`yaml:"host_services"` tag and make `HostServices` (no tag, `yaml:"-"`) the parsed
result — `KnownFields` only inspects tagged fields, so the `[]string` field absorbs
the YAML and `HostServices` is populated in the defaults pass. (If `yaml:"-"` proves
awkward with `KnownFields`, fall back to a top-level `rawConfig` struct mirroring the
schema with `host_services []string`, decoded strictly, then mapped to `Config`.)

`Parse`:

```go
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, reformat(err) // *yaml.TypeError / parse error -> ValidationError
	}
	if err := cfg.applyDefaults(); err != nil { // mode default + host_services parse
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}
```

For Phase 1, `validate()` may be a stub returning nil (filled in Phase 2);
`applyDefaults()` sets `Rule.Mode = "intercept"` where empty and parses
`host_services`. An empty document (`Decode` returns `io.EOF`) parses to a zero
`Config` with no error — handle `io.EOF` as "empty config is valid".

**`internal/config/testdata/example.yaml`** — verbatim copy of the config block from
`docs/design.md:76–149` (comments may be trimmed; all fields kept).

**`internal/config/config_test.go`** (white-box `package config` for the loader/round
-trip; or black-box `package config_test` — match `state`'s white-box style since we
assert internal behaviour like defaults). Tests:
- `TestParse_ExampleRoundTrips`: read `testdata/example.yaml`, `Parse`, assert no
  error and spot-check fields (`Agent.Command`, `Safehouse.AddDirsRW`, `Include`,
  `Network.HostServices == [{mysql 3306} {redis 6379} {mailpit 1025}]`,
  `Egress.Generators`, `Allow[0].Host == "api.github.com"`,
  `*Allow[0].Methods == [GET POST]`, `Env["GIT_AUTHOR_NAME"]`).
- `TestParse_EmptyDocument`: empty bytes → zero `Config`, no error.
- `TestParse_DefaultsMode`: a minimal allow rule with no `mode` → `Mode == "intercept"`.

### Success criteria

#### Automated
- [x] `go build ./...` compiles.
- [x] `go test -race ./internal/config/...` passes.
- [x] `go mod tidy` leaves `gopkg.in/yaml.v3` as a direct require (no `// indirect`),
      and `git diff go.mod go.sum` is limited to that promotion.
- [x] `make lint` clean.

#### Manual
- [x] The example fixture is a faithful copy of `docs/design.md`'s config block.
- [x] `Network.HostServices` is the typed `{Label, Port}` form, not raw strings.

---

## Phase 2 — Validation + golden error messages

### Changes

**`internal/config/validate.go`** — `validate()` and the `ValidationError` type.

```go
// ValidationError aggregates one or more human-readable config problems. Its message
// is stable and free of Go type names so it can be golden-tested.
type ValidationError struct {
	Issues []string
}
func (e *ValidationError) Error() string { /* strings.Builder, byte-stable header + "  - " bullets */ }
```

`validate()` collects (not fail-fast — report all issues, like `prereq` accumulates):
- For each `Rule` in `Allow` and `DenyAlways`:
  - `Host` empty → issue.
  - `Mode` not in `{intercept, passthrough}` → issue (names the offending value).
  - `Mode == passthrough` && `Paths != nil` → issue ("passthrough rule for host X must
    not set paths").
  - `Mode == passthrough` && `Methods != nil` → issue (same for methods).
- `host_services` parse failures (bad format / port out of range) surface here too
  (the parse in `applyDefaults` records them, or `validate` re-checks) with a clear
  message naming the bad entry.

**`reformat(err error) error`** in `config.go` — type-assert `*yaml.TypeError`,
iterate `.Errors`, and translate each into our own wording (e.g. map
`field X not found in type config.Config` → `unknown key "X"`). Non-`TypeError`
parse errors get a generic `invalid YAML: <message>` wrapper. Output is a
`*ValidationError` so all error paths share one stable shape.

**`internal/config/validate_test.go`** — table-driven (`version_test.go` style) for
the boolean validation logic, plus golden tests for rendered messages:
- `var update = flag.Bool("update", false, "regenerate golden files")` (once).
- Fixtures under `testdata/`: `passthrough_with_paths.yaml`,
  `passthrough_with_methods.yaml`, `bad_mode.yaml`, `empty_host.yaml`,
  `bad_host_service_port.yaml`, `unknown_top_key.yaml`, `unknown_nested_key.yaml`.
- For each: `Parse` → assert error → `require.Equal(string(golden), err.Error())`
  with the `update`-write branch. Golden files: `testdata/<name>.golden`.
- A `TestValidate` table test (so `go test ./internal/config -run TestValidate` is
  meaningful per the ticket's verification step 3) covering the pass/fail booleans
  without golden coupling.

### Success criteria

#### Automated
- [x] `go test -race ./internal/config/...` passes (table + golden).
- [x] `go test ./internal/config -run TestValidate` green.
- [x] Golden files regenerate with no *unexpected* diff. NOTE: repo-wide
      `make golden` (= `go test ./... -update`) has a **pre-existing** defect — the
      custom `-update` flag is only defined in packages with golden tests, so the
      `cli`/`state`/`sysdep` binaries reject it (`internal/state`, from AC-0006 and
      untouched here, fails identically). Verified via the scoped
      `go test ./internal/config -update`; all `testdata/*.golden` are new (no
      existing golden modified). The Makefile papercut is surfaced to the user,
      out of scope for AC-0007.
- [x] `go test -race ./...` (full suite) green.
- [x] `make lint` clean.

#### Manual
- [x] `mode: passthrough` with `paths` and with `methods` each produce a clear,
      host-named error (design:241).
- [x] An unknown top-level key and an unknown nested key both error (strict policy;
      nested-key case proves KnownFields penetrates list-element Rule structs).
- [x] Golden messages are human-readable and contain **no** Go type names.
- [x] Error messages would let an operator fix the config without reading source.

---

## Phase 3 — Close-out

### Changes
- Tick the ticket's Acceptance Criteria and Verification boxes in
  `thoughts/shared/tickets/AC-0007-config-schema-loader.md`; set **Status: Done**.
- Answer the ticket's two "Questions for Research/Planning" inline (strict mode chosen;
  `host_services` parsed-and-validated here, address-forcing deferred to AC-0014).
- Tick this plan's success-criteria boxes; update the status file.

### Success criteria

#### Automated
- [x] Full `make test`, `go build ./...`, `make lint` green at HEAD.

#### Manual
- [x] Ticket marked Done with both open questions answered.

---

## Testing strategy

- **Pure logic (defaults, validation booleans, host_services parsing)** → table-driven
  white-box tests (`version_test.go` idiom).
- **Rendered error messages** → golden-file tests with the `update` flag
  (`report_test.go` idiom); fixtures + `.golden` under `testdata/`.
- **The full schema / round-trip** → `example.yaml` copied from the design, parsed
  under strict mode (strict decode is itself the no-silent-loss proof) + field
  assertions.
- **No OS calls** in the package → it imports neither `os` nor `os/exec`; `bytes` +
  `gopkg.in/yaml.v3` + `fmt`/`strings`/`strconv` only. (Test files may import `os` to
  read fixtures.)

## Verification commands (from `.claude/tce/profile.md`)

- `go build ./...`
- `go test -race ./internal/config/...` and `go test -race ./...`
- `go test ./internal/config -run TestValidate`
- `make golden` then review `git diff`
- `make lint`

## References

- Research: `thoughts/shared/research/2026-06-05-AC-0007-config-schema-loader.md`.
- Ticket: `thoughts/shared/tickets/AC-0007-config-schema-loader.md`.
- `docs/design.md:76–153` (the configuration), `:212–248` (per-host modes).
- Spec WP-1.2: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md:142`.
</content>
