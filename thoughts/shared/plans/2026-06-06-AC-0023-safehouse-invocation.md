---
date: 2026-06-06
ticket: AC-0023
title: "Safehouse invocation (WP-4.2) — internal/cage"
status: ready
research: thoughts/shared/research/2026-06-06-AC-0023-safehouse-invocation.md
tags: [plan, cage, safehouse, proxy, WP-4.2]
---

# AC-0023 — Safehouse invocation (WP-4.2): implementation plan

## Overview

Build `internal/cage`: a thin composition layer that turns a compiled config +
live proxy port + resolved paths into the exact `safehouse` 0.10.1 invocation —
argv (mount/capability flags, two ordered `--append-profile` fragments,
`--env-pass`, the wrapped agent command) plus the environment to set on the
`safehouse` child (proxy routing, CA trust, `CLAUDE_CONFIG_DIR`, and the user's
`config.env`). The argv+env construction is a **pure function of (config, port,
paths)** (golden-testable). Two side effects are split out: seeding the ephemeral
`CLAUDE_CONFIG_DIR` and writing the launch-time proxy-port `.sb` fragment.

This package only *constructs and prepares*; it does not exec, forward signals,
or orchestrate lifecycle (AC-0024 / AC-0025).

### Decisions locked at the question checkpoint
1. **Env set:** the full S4 set — `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` + lowercase variants + the 4 CA vars.
2. **Sanitized seed:** `settings.json` = `{}` (minimal; nothing executable by construction).
3. **Argv scope:** full invocation — inject `config.env` via `--env-pass`, map `agent.workdir` → `--workdir`, append `config.agent.command` after `--`.

## Current state

- `internal/cage` does **not** exist.
- Upstream producers are stable: `config.Config` (`internal/config/config.go:29-49`), `state.Layout` (`internal/state/state.go:69-77,176-199`), `proxy.Manager.Attach` → `Attachment.Port` (`internal/proxy/lifecycle.go:80,96`), `profile.RenderNetworkSB`/`RenderProxyFragment` (`internal/profile/profile.go:54,74`).
- **Nothing writes the launch-time proxy-port fragment** (`profile.go:70-78` comment names it "the ordering contract for AC-0023"); `state.Layout` has no accessor for it.
- No Go constant for the mitmproxy CA path; it is `~/.mitmproxy/mitmproxy-ca-cert.pem`.
- Safehouse 0.10.1 CLI contract (confirmed from `--help`): `--add-dirs PATHS` / `--add-dirs-ro PATHS` (colon-joined), `--enable=FEATURES` (comma-joined), `--env-pass NAMES` (comma-joined names, values read from host env), `--append-profile PATH` (repeatable, in order), `--workdir DIR`, wrapped command after `--`.

## Desired end state

A `internal/cage` package whose `cage.Build(Inputs) (Invocation, error)` is a pure
argv+env builder, plus a `*cage.Builder` with `Resolve(...)` (seam → pure Inputs)
and `Prepare(Inputs)` (seed config dir + write proxy fragment). Covered by
golden + table + negative unit tests and one gated integration test that launches
real `safehouse` 0.10.1 around `echo ok`, proving the env reaches the cage and
default egress is denied. `make test`, `go build ./...`, and `make lint` green.

## What we're NOT doing
- Exec, process groups, signal forwarding (AC-0024).
- `run`/`doctor` orchestration, credential pre-flight refusal, wiring `cage` into `App` (AC-0025 / AC-0031).
- Writing `network.sb` (that is `internal/profile`'s compile output, guaranteed present by the compile step before cage runs).
- The full isolation matrix (AC-0033, M3 gate).

---

## Phase 1 — `state.Layout.ProxyProfileSB()`

Add the on-disk path for the launch-time proxy-port fragment, keeping path naming
centralised in `internal/state` (consistent with `NetworkSB()`/`PolicyJSON()`).

**Changes — `internal/state/state.go`:**
- Add name constant alongside the others (`state.go:32-55`): `proxyProfileSBName = "proxy.sb"`.
- Add accessor next to `NetworkSB()` (~`state.go:179`):
  ```go
  // ProxyProfileSB is the launch-time Seatbelt fragment for the live proxy port,
  // passed to Safehouse via a second --append-profile after NetworkSB (the
  // ordering contract enforced by profile.RenderProxyFragment). Rewritten every
  // launch because the port is ephemeral.
  func (l Layout) ProxyProfileSB() string { return filepath.Join(l.Root, proxyProfileSBName) }
  ```

**Tests — `internal/state/state_test.go`:** extend the existing layout-paths test to assert `ProxyProfileSB()` == `<root>/proxy.sb` (mirror the `NetworkSB()` assertion).

### Success criteria
#### Automated
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/state/...` passes.

---

## Phase 2 — `internal/cage`: pure `Build` (argv + env)

Create `internal/cage/cage.go` with the pure builder and helpers.

**Types:**
```go
const Binary = "safehouse"

// Inputs is the complete, pre-resolved input to Build — no I/O happens here.
type Inputs struct {
    Config     *config.Config
    Layout     state.Layout
    ProxyPort  int
    HomeDir    string // resolved value of ~ (from PathResolver.UserHomeDir)
    CACertPath string // ~/.mitmproxy/mitmproxy-ca-cert.pem
}

// Invocation is the constructed safehouse command: a pure function of Inputs.
type Invocation struct {
    Path string   // == Binary
    Args []string // safehouse flags, then "--", then the agent command
    Env  []string // extra KEY=VALUE pairs (sorted) to set on the safehouse process
}
```

**`func Build(in Inputs) (Invocation, error)`** — pure. Steps:
1. Validate: `len(Config.Agent.Command) > 0` else error; `1 <= ProxyPort <= 65535` else error (defence in depth — the port comes from the proxy lock, not config).
2. Build env map via `buildEnv(in)` (below); derive sorted `--env-pass` names from its keys.
3. Assemble `Args` in this fixed order (skip empty groups):
   - `--add-dirs <colon-join(expand each AddDirsRW)>`
   - `--add-dirs-ro <colon-join(expand each AddDirsRO)>`
   - `--enable=<comma-join(Enable)>`  *(`=` form, matching design.md:83)*
   - `--workdir <expand(Agent.Workdir)>`  *(only if non-empty)*
   - `--append-profile <Layout.NetworkSB()>`
   - `--append-profile <Layout.ProxyProfileSB()>`
   - `--env-pass <comma-join(sorted env keys)>`
   - `--`
   - `Agent.Command...`
4. Return `Invocation{Path: Binary, Args, Env}`.

**`func buildEnv(in Inputs) map[string]string`** — precedence: user `config.env`
first, then computed vars overwrite (so a user cannot disable egress by setting
`HTTPS_PROXY`):
```
for k,v := range Config.Env { env[k]=v }
proxyURL := "http://127.0.0.1:" + strconv.Itoa(ProxyPort)
HTTP_PROXY/http_proxy/HTTPS_PROXY/https_proxy = proxyURL
NO_PROXY/no_proxy = "localhost,127.0.0.1,::1"
NODE_EXTRA_CA_CERTS/SSL_CERT_FILE/REQUESTS_CA_BUNDLE/GIT_SSL_CAINFO = CACertPath
CLAUDE_CONFIG_DIR = Layout.ClaudeConfigDir()
```
Then flatten to sorted `[]string` of `KEY=VALUE` for `Invocation.Env`.

**`func expandPath(p, home, projectDir string) string`** — `~`/`~/x` → `home`;
absolute → `filepath.Clean(p)`; relative/`.` → `filepath.Join(projectDir, p)`.
`projectDir` is `Layout.Canonical`.

Helpers: reuse the `sortedUnique`/`sort.Strings` idiom (`internal/generator/manifest.go:74-100`) for env keys; a small `colonJoin`/`commaJoin` over expanded slices.

**Tests — `internal/cage/cage_test.go` (`package cage_test`):**
- `TestBuild_Golden`: fixture `Inputs` (RW `[.]`, RO `[~/.config/git]`, enable `[shell-init]`, workdir `.`, command `[claude, --dangerously-skip-permissions]`, `env {MY_VAR: hello}`, port `18081`, home `/home/test`, canonical `/proj`, CA `/home/test/.mitmproxy/mitmproxy-ca-cert.pem`). `json.MarshalIndent(inv, "", "  ")` + trailing `\n` → compare to `testdata/invocation.golden.json` behind the package `-update` flag (pattern from `internal/generator/generator_test.go:21-78`).
- `TestExpandPath` (table): `~`, `~/.config/git`, `.`, `sub/dir`, `/abs/path`.
- `TestBuild_NeverMountsRealClaude` (AC step 3 negative): assert no arg equals/contains `<home>/.claude` in an `--add-dirs*` value, and that `CLAUDE_CONFIG_DIR` points under `Layout.Root` (`/claude`), not `~/.claude`.
- `TestBuild_EnvPrecedence`: `config.env{HTTPS_PROXY: evil}` is overridden by the computed proxy URL.
- `TestBuild_EnvPassMatchesEnvKeys`: the `--env-pass` names equal the sorted keys of `Invocation.Env`.
- `TestBuild_Validation`: empty `Agent.Command` → error; port `0` and `70000` → error.

### Success criteria
#### Automated
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/cage/...` passes; `make golden` diff reviewed.
- [ ] `make lint` clean.
#### Manual
- [ ] Golden `invocation.golden.json` shows: colon-joined `--add-dirs`, `--enable=shell-init`, both `--append-profile` paths in order (network.sb then proxy.sb), `--env-pass` listing all 11 injected names sorted, and the wrapped command after `--`.

---

## Phase 3 — `internal/cage`: `Builder`, `Resolve`, `Prepare` (side effects)

Add the seam-backed constructor and the two side-effecting steps to `cage.go`,
following the `proxy/extract.go:51-59` constructor pattern.

```go
type Builder struct {
    fs    sysdep.FileSystem
    paths sysdep.PathResolver
}
func New(fs sysdep.FileSystem, paths sysdep.PathResolver) *Builder { return &Builder{fs, paths} }

// Resolve turns seams into a pure Inputs (the only I/O is the home-dir lookup).
func (b *Builder) Resolve(cfg *config.Config, layout state.Layout, port int) (Inputs, error) {
    home, err := b.paths.UserHomeDir() // err -> wrap
    return Inputs{Config: cfg, Layout: layout, ProxyPort: port,
        HomeDir: home, CACertPath: caCertPath(home)}, nil
}

func caCertPath(home string) string { return filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem") }

// Prepare performs cage's side effects: seed the ephemeral CLAUDE_CONFIG_DIR and
// (re)write the launch-time proxy-port fragment. network.sb is NOT written here —
// it is the compile step's output, present before cage runs.
func (b *Builder) Prepare(in Inputs) error {
    dir := in.Layout.ClaudeConfigDir()
    b.fs.MkdirAll(dir, 0o700)            // idempotent
    settings := filepath.Join(dir, "settings.json")
    if _, err := b.fs.Stat(settings); errors.Is(err, fs.ErrNotExist) {
        b.fs.WriteFile(settings, []byte("{}\n"), 0o600) // seed only if absent
    }
    frag, err := profile.RenderProxyFragment(in.ProxyPort) // err -> wrap
    return b.fs.WriteFile(in.Layout.ProxyProfileSB(), []byte(frag), 0o600) // always overwrite
}
```
Rationale for **seed-only-if-absent** settings.json: the redirected dir persists
across launches under `projects/<hash>/claude`; preserving the in-cage agent's own
session state is good UX and safe — the security property comes from *never
copying the real `~/.claude`*, not from wiping. The proxy fragment is always
rewritten because the port is ephemeral. (Add inline comments to this effect.)

**Tests — `internal/cage/cage_test.go`** (using `sysdeptest.FakeFileSystem` /
`FakePathResolver`):
- `TestPrepare_SeedsAndWritesFragment`: empty FS → `settings.json` == `"{}\n"`, `proxy.sb` == `RenderProxyFragment(port)`, claude dir created (assert via `Dirs`/`Perms`).
- `TestPrepare_PreservesExistingSettings`: pre-load `settings.json` with non-empty content → unchanged after `Prepare`; `proxy.sb` still rewritten.
- `TestPrepare_RewritesFragmentOnPortChange`: two `Prepare` calls with different ports → `proxy.sb` reflects the latest.
- `TestResolve`: `FakePathResolver{HomeDir:"/home/test"}` → `Inputs.CACertPath == "/home/test/.mitmproxy/mitmproxy-ca-cert.pem"`, `HomeDir` set; home-dir error path wrapped.

### Success criteria
#### Automated
- [ ] `go test -race ./internal/cage/...` passes.
- [ ] `make lint` clean.
- [ ] Grep guard: no direct `os/exec`, `os.Getenv`, or keychain calls in `internal/cage` (all I/O via seams).

---

## Phase 4 — Integration test + final verification

**`internal/cage/cage_integration_test.go`** (`//go:build integration`), skipping
via `t.Skip` if `safehouse` is not on `PATH` (use `exec.LookPath`):
1. Make a temp project dir; `state.New(OSPathResolver).Resolve(tmp)` → layout; `MkdirAll(layout.Root)`.
2. Write the deny-all baseline to `layout.NetworkSB()` via `profile.RenderNetworkSB(nil)` (in the real flow the compile step produces this; the test stands it in).
3. `b := cage.New(OSFileSystem{}, OSPathResolver{})`; `in,_ := b.Resolve(cfg, layout, port)`; `b.Prepare(in)`.
   - `cfg`: `agent.command = ["/bin/sh","-c","echo ok; printenv HTTPS_PROXY"]`, `safehouse.add_dirs_rw = [tmp]`, no `enable`.
   - `port`: an unused localhost port (no real proxy is started — we are testing default-deny + env wiring, not the allow path).
4. `inv,_ := cage.Build(in)`; `cmd := exec.CommandContext(ctx, inv.Path, inv.Args...)`; `cmd.Env = append(os.Environ(), inv.Env...)`; capture output.
5. Assert: exit 0; stdout contains `ok`; stdout contains `http://127.0.0.1:<port>` (proves `--env-pass` forwarded `HTTPS_PROXY` into the cage).
6. Egress-deny smoke: a second caged command `curl -s --max-time 3 https://example.com` (proxy env unset for this probe, or pointed at the dead port) **fails** — proving the deny-all `network.sb` blocks direct egress. Document that the "allowed *via* proxy" path is covered by `internal/profile`'s live integration test and the full matrix by AC-0033 (ticket step 6).

**Buildinfo skew check (no code change unless it blocks):** the integration test
runs against the installed `safehouse` (0.10.1). `internal/buildinfo` records
`agent-safehouse: 1.4.2` (buildinfo.go:35). Note the discrepancy in the commit
body; do **not** silently change the constant — flag it for the maintainer to
reconcile (re-test + bump per the buildinfo convention, or correct the installed
tool). It does not gate this package's behavior.

**Final verification:**
- `go build ./...`
- `make test` (race, hermetic) — all green.
- `make test-integration` — the new cage integration test passes (or skips cleanly if `safehouse` absent); note any pre-existing unrelated failures (e.g. the proxy real-proxy test) without attributing them here.
- `make lint` — clean.
- `make golden` diff reviewed and intentional.
- Tick AC-0023 acceptance boxes; mark the ticket **Done**.

### Success criteria
#### Automated
- [ ] `go build ./...`, `make test`, `make lint` green.
- [ ] `make test-integration` cage test passes or skips cleanly.
#### Manual
- [ ] Real `safehouse` 0.10.1 accepts the built argv and runs `echo ok` → exit 0.
- [ ] `HTTPS_PROXY` is visible inside the cage; direct egress is denied by the baseline `.sb`.
- [ ] Ticket marked Done; acceptance criteria checked.

---

## Testing strategy summary
- **Pure logic** (`Build`, `expandPath`, `buildEnv`) → table + golden tests, no I/O.
- **Generated artifact** (the `Invocation`) → golden JSON with `-update`.
- **Side effects** (`Prepare`, `Resolve`) → `sysdeptest` fakes.
- **Real tool** (`safehouse`) → `//go:build integration` only.
- **Negative/security** → never-mount-real-`~/.claude`, env precedence, validation.

## References
- Research: `thoughts/shared/research/2026-06-06-AC-0023-safehouse-invocation.md`
- Ticket: `thoughts/shared/tickets/AC-0023-safehouse-invocation.md`
- Patterns: `internal/proxy/lifecycle.go:230-239`, `internal/generator/generator_test.go:21-78`, `internal/proxy/extract.go:51-59`
