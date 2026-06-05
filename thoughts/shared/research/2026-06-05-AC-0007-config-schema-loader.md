---
date: 2026-06-05
ticket: AC-0007
topic: "Config schema & loader (WP-1.2) — internal/config"
status: complete
branch: main
git_commit: 585673c30b6fe0d6aafb35cf39adb84dad560494
researcher: Claude (tce:work)
---

# AC-0007 — Config schema & loader (WP-1.2): Research

## Research question

Design a new `internal/config` package that parses the full `.agent-creance.yaml`
schema into typed Go structs, validates structure, and rejects invalid configs —
notably `mode: passthrough` carrying `paths`/`methods` — with clear, golden-tested
error messages. What are the existing patterns to mirror, the exact schema, the
`yaml.v3` behaviours we must work around, and the open decisions for planning?

## Summary of findings

- **This is a pure-logic, hermetic parsing+validation ticket.** It turns YAML bytes
  into a validated typed struct. It does **not** resolve `include:` files, walk the
  filesystem, run generators, or compile anything — those are AC-0008 (include
  merge), Phase 2 (compiler), and the generators ticket respectively.
- **No config code exists yet.** `gopkg.in/yaml.v3 v3.0.1` is already in `go.mod` but
  only as an **indirect** dependency (pulled in by `rogpeppe/go-internal`); it must be
  promoted to a direct `require`. No `Unmarshal`/config-loading code exists anywhere.
- **`internal/state` is the structural template**: a small struct, constructed via
  `New(...)`, package-prefixed wrapped errors (`fmt.Errorf("config: …: %w", err)`),
  white-box `package config` tests with stdlib `testing`.
- **`internal/prereq` is the golden-test + rendering template**: `var update =
  flag.Bool("update", …)`, `testdata/*.golden`, `require.Equal(want, got)` in a
  black-box `package config_test`, a pure `Render(...) string` built with
  `strings.Builder` and byte-stable glyph/format constants.
- **The full schema and the canonical example config are fixed by `docs/design.md`**
  (lines 76–149 for the example, 212–248 for per-host modes). Reproduced in detail
  below.
- **`yaml.v3` requires deliberate choices** for our goals: plain `yaml.Unmarshal`
  silently ignores unknown keys; strict rejection needs `Decoder.KnownFields(true)`.
  Crucially, `KnownFields(true)` is **defeated by any custom `UnmarshalYAML`**
  (go-yaml issue #642), so we must **not** use `UnmarshalYAML` for defaults — apply
  defaults in a separate post-decode pass. The raw `yaml.v3` error strings embed our
  Go package/type names (`config.Config`), so they are **brittle to golden directly**
  — we should reformat them into our own stable, human-readable messages.

## The exact schema (from `docs/design.md`)

The single project file `.agent-creance.yaml` (and the same-schema global
`~/.config/agent-creance.yaml`). Canonical example, `docs/design.md:76–149`:

```yaml
agent:
  command: ["claude", "--dangerously-skip-permissions"]   # []string
  workdir: .                                              # string

safehouse:
  add_dirs_rw: [.]                                        # []string (paths, may contain ~)
  add_dirs_ro: [~/.config/git]                            # []string
  enable: [shell-init]                                    # []string

include:                                                  # []string (paths) — resolution is AC-0008
  - .agent-creance/team-shared.yaml

network:
  host_services:                                          # []string of "label:port"
    - mysql:3306
    - redis:6379
    - mailpit:1025

  egress:
    generators:                                           # []string (e.g. package_json, composer_json)
      - package_json
      - composer_json

    allow:                                                # []Rule (soft-allow)
      - host: api.github.com                              #   host:   string (may be a glob "*.medium.com" / "*")
        paths: ["/repos/tobyS/this-project/"]             #   paths:  []string (optional)
        methods: [GET, POST]                              #   methods:[]string (optional)
        # mode: intercept (default) | passthrough         #   mode:   string (optional, default "intercept")
        # reason: "…"                                     #   reason: string (optional)

    deny_always:                                          # []Rule (hard-deny)
      - host: w3schools.com
        reason: "Known low-quality source…"
      - host: "*.medium.com"
        paths: ["/@*"]
        reason: "…"
      - host: "*"
        paths: ["**/.env", "**/.git/config"]
        reason: "Secrets path."

env:                                                      # map[string]string
  GIT_AUTHOR_NAME: "Toby (caged)"
```

### Per-host enforcement modes (`docs/design.md:212–248`)

Every egress rule carries an optional `mode:` (default `intercept`):

- **`intercept`** (default) — TLS-terminated, matched on host + path + method (Mode
  A), or host-wide when `paths`/`methods` are omitted (Mode B). Fully audited.
- **`passthrough`** — raw CONNECT tunnel, no TLS termination, host-granularity only.

The hard validation rule this ticket must enforce (design:241):

> `paths`/`methods` on a passthrough rule are **meaningless and are rejected at
> compile time** (a clear error, not a silent ignore).

Note both `allow:` and `deny_always:` are lists of the same **Rule** shape (host,
paths, methods, mode, reason). `reason:` is most meaningful on `deny_always` (it's
surfaced in the hard-deny 403 body, design:272) but the schema is uniform.

## Conventions to mirror (with code references)

### Structural template — `internal/state`
- `internal/state/state.go:51-58` — single struct + `New(dep) *T` constructor, deps
  are interfaces, fields unexported.
- `internal/state/state.go:78,82,117` — error style: `fmt.Errorf("state: <context>:
  %w", err)`, package-name prefix, `%w` wrap, no double-prefixing of already-wrapped
  helper errors.
- `internal/state/state_test.go` — white-box `package state`, stdlib `testing`
  (`t.Errorf`/`t.Fatalf`), table-driven subtests, sentinel `errors.New("boom")` for
  error paths. **No testify** in white-box tests.

### Golden-file + rendering template — `internal/prereq`
- `internal/prereq/report_test.go:21` — `var update = flag.Bool("update", false,
  "regenerate golden files")` (declare **once** per test package; multiple golden
  tests share it).
- `internal/prereq/report_test.go:36-48` — the exact golden idiom: build `got`,
  `golden := filepath.Join("testdata", "<name>.golden")`, on `*update`
  `os.WriteFile(golden, []byte(got), 0o644)` then return, else `os.ReadFile` +
  `require.Equal(t, string(want), got)`. Black-box `package config_test`, uses
  testify `require`.
- `internal/prereq/report.go:8-14,20-48` — rendering: pure `Render(...) string` via
  `strings.Builder`, package-level byte constants so golden and code agree on exact
  bytes, small unexported field helpers, deterministic (sorted) output.
- `make golden` (= `go test ./... -update`) regenerates; always review the diff.

### Table-driven test template — `internal/prereq/version_test.go:10-37`
Anonymous case struct (`name` first, then inputs, then `want`), `for _, tc := range
cases { t.Run(tc.name, …) }`, plain `if got != want { t.Errorf("…= %v, want %v") }`.

### sysdep seam idiom (if we ever add file reading)
- `internal/sysdep/pathresolver.go` / `commander.go` — interface + empty-struct OS
  impl + compile-time `var _ Iface = (*Impl)(nil)` assertion + a scripted fake in
  `sysdep/sysdeptest/` with exported map fields, a `New…` constructor, and `*Err`
  fields to force failures. **There is currently no file-content I/O seam** — the
  `FileSystem` seam is explicitly deferred to WP-1.4/AC-0009 (sysdep seam
  extensions). See "Open decision 3" below for why this ticket should avoid needing
  it.

## `yaml.v3` behaviours that drive the design (v3.0.1, pinned & terminal)

`gopkg.in/yaml.v3` v3.0.1 (May 2022) is the **final release** (upstream archived
2025), so its error strings are frozen — but they embed our Go type names, so we
still shouldn't golden them raw.

1. **Strict unknown-key rejection** needs a `Decoder`, not `Unmarshal`:
   ```go
   dec := yaml.NewDecoder(bytes.NewReader(data))
   dec.KnownFields(true)
   var cfg Config
   err := dec.Decode(&cfg)
   ```
   Plain `yaml.Unmarshal` **silently ignores** unknown keys. `KnownFields(true)`
   yields a `*yaml.TypeError` whose `.Errors` entries read
   `line N: field <key> not found in type config.Config` (note: type name embedded).

2. **`KnownFields` and custom `UnmarshalYAML` do not compose** (go-yaml #642). If any
   struct implements `UnmarshalYAML`, unknown-key tracking inside it breaks. ⇒
   **Do not** use `UnmarshalYAML` for `mode` defaults. Instead: plain strict decode,
   then a separate `applyDefaults()` pass, then `validate()`.

3. **Absent vs. empty** is only distinguishable via **pointer fields**. For the
   passthrough rule ("`paths`/`methods` must NOT be set"), use `*[]string` so
   `Paths == nil` (omitted) is distinct from `Paths != nil && len == 0` (`paths: []`).
   Checking presence (`!= nil`) — not `len() > 0` — is what the validation needs.

4. **Error format:** `*yaml.TypeError` renders as `yaml: unmarshal errors:\n  line N:
   …`; parse/syntax errors render as `yaml: line N: …`. Line numbers are always
   present for type errors. Recommendation: type-assert to `*yaml.TypeError`, iterate
   `.Errors`, and **reformat** into our own type-name-free, human-readable, stable
   message for golden tests; only the prefix shape is safe to assert raw.

## Proposed package shape (for the plan)

```go
package config

// Config is the typed, validated representation of one .agent-creance.yaml document.
type Config struct {
    Agent     Agent              `yaml:"agent"`
    Safehouse Safehouse          `yaml:"safehouse"`
    Include   []string           `yaml:"include"`
    Network   Network            `yaml:"network"`
    Env       map[string]string  `yaml:"env"`
}
type Agent struct { Command []string; Workdir string }
type Safehouse struct { AddDirsRW, AddDirsRO, Enable []string }
type Network struct { HostServices []string; Egress Egress }   // see open decision 1 re host_services
type Egress struct { Generators []string; Allow, DenyAlways []Rule }
type Rule struct {
    Host    string
    Paths   *[]string   // pointer: distinguishes omitted from empty (passthrough check)
    Methods *[]string
    Mode    string      // defaulted to "intercept" in applyDefaults
    Reason  string
}

// Parse decodes (strict), applies defaults, validates, and returns the typed config
// or a human-readable error. It does NOT touch the filesystem or resolve includes.
func Parse(data []byte) (*Config, error)
```

Pipeline inside `Parse`: **strict decode** (`KnownFields(true)`) → **applyDefaults**
(`mode` → `"intercept"` where empty) → **validate** (passthrough-with-paths/methods,
unknown `mode` value, port range for host_services). Errors reformatted into a stable
`ValidationError`/multi-issue message that is golden-tested.

## Open decisions for the planning checkpoint

1. **`host_services` representation.** Design says label is cosmetic; the address is
   always forced to `127.0.0.1`; AC-0014 (Seatbelt compiler) is the consumer that
   needs the address. Should AC-0007 (a) keep `host_services` as raw `[]string` and
   only validate the `label:port` *format* (port numeric, 1–65535), deferring
   structured parsing/address-forcing to AC-0014; or (b) parse into a typed
   `[]HostService{Label string, Port int}` now (the "typed config so downstream never
   guesses shapes" developer story)? **Recommendation: (b) typed**, validated port
   range, no address (that's AC-0014's job). This is a struct-shape fork worth
   confirming.

2. **Strict (fail-closed) unknown-key handling.** The user story ("a typo produces a
   precise error") and the fail-closed posture of a security tool (a misspelled
   `deny_always` silently ignored is a *security hole*) both point to strict
   (`KnownFields(true)`). The only cost is forward-compat: a config authored for a
   newer agent-creance fails on an older binary. **Recommendation: strict.** The
   ticket explicitly lists this as a question, so confirm it.

3. **File-reading boundary (no new seam).** Because AC-0008 owns include resolution
   (and the `FileSystem` seam ships in WP-1.4/AC-0009), AC-0007 should expose
   `Parse(data []byte)` and stay filesystem-free — keeping "Depends on: none" true
   and the package perfectly hermetic. Tests read fixtures with `os.ReadFile` (test
   code may touch the FS). **This is the recommended boundary; not a user-judgment
   call** — noted here for completeness, will be stated in the plan.

4. **Error reformatting (decided, not a user call).** Reformat `*yaml.TypeError` and
   our own validation failures into stable, type-name-free messages and golden-test
   those, rather than golden-ing raw yaml.v3 strings. Stated in the plan.

5. **Validation scope (decided).** v0.1 validates: passthrough⊕paths/methods, `mode ∈
   {intercept, passthrough}`, host non-empty per rule, `host_services` port format.
   It does **not** validate HTTP method names, glob syntax, or path semantics (out of
   scope / later phases). All top-level sections are **optional** (a project config
   may carry only deltas; the global baseline may be `include:`-only).

## Acceptance-criteria → test mapping

- *Structs cover the full schema* → compile + the round-trip fixture (strict decode of
  the design example proves every field maps; a missing struct field would error).
- *Example round-trips with no loss* → `testdata/example.yaml` (copied from
  `docs/design.md`) parses under strict mode with **no error** + field-value
  assertions. (Strict mode is itself the no-silent-loss guarantee.)
- *`mode` defaults to `intercept`; passthrough+paths/methods errors* → table tests +
  a `passthrough_with_paths.yaml` fixture asserting the exact golden error.
- *Invalid configs → stable human-readable golden errors* → `testdata/*.golden`
  via the `update`-flag idiom; includes an unknown-top-level-key case (per chosen
  strict policy).

## Verification commands (from `.claude/tce/profile.md`)

- `go build ./...` (typecheck)
- `go test -race ./internal/config/...` and `go test -race ./...`
- `go test ./internal/config -run TestValidate` + `make golden` (review `git diff`)
- `make lint` (`go vet` + `golangci-lint`)

## Related documents

- `thoughts/shared/tickets/AC-0007-config-schema-loader.md` — this ticket.
- `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md` — WP-1.2
  (lines 142–148), `internal/config` responsibility (line 76).
- `thoughts/shared/tickets/AC-0008-config-include-merge.md` — include resolution &
  merge (the direct follow-on; owns filesystem walking + the global implicit include).
- `thoughts/shared/tickets/AC-0009-sysdep-seam-extensions.md` — ships the `FileSystem`
  seam (WP-1.4) AC-0008 needs.
- `thoughts/shared/research/2026-06-05-AC-0006-state-dir-project-identity.md` +
  `thoughts/shared/plans/2026-06-05-AC-0006-state-dir-project-identity.md` — the
  predecessor (WP-1.1); template for this ticket's own artifacts.
- `docs/design.md` — "The configuration" (76–153), "Allowlist generators" (155–210),
  "Per-host enforcement modes" (212–248).
</content>
</invoke>
