---
date: 2026-06-05
author: Tobias Schlitt
ticket: AC-0014
topic: "Seatbelt profile compiler → network.sb (WP-2.5)"
status: ready
branch: main
research: thoughts/shared/research/2026-06-05-AC-0014-seatbelt-profile-compiler.md
gates: [AC-0003 (S3, resolved), AC-0005 (S5, resolved)]
---

# Implementation Plan — AC-0014: Seatbelt profile compiler → `network.sb` (WP-2.5)

## Overview

Create `internal/profile`: a library package that generates the cage's network
Seatbelt profile. Two artifacts:

1. **`network.sb`** — an `--append-profile` *fragment*: a `(deny network*)` baseline
   plus one `(allow network-outbound (remote tcp "localhost:<port>"))` per
   `network.host_services` entry. Written out-of-tree to `state.Layout.NetworkSB()`,
   regenerated every launch (no input-hash cache — see decisions).
2. **launch-time proxy fragment** — a pure render function that emits a single
   `(allow network-outbound (remote tcp "localhost:<port>"))` line for the live,
   ephemeral proxy port. Not persisted by this ticket; consumed at launch (AC-0023).

The package mirrors the AC-0013 `internal/policy/compile` patterns (DI via sysdep
seams, atomic out-of-tree write, golden tests, C4 no-in-tree-write assertion) but is
simpler: it has no registry I/O and, per the checkpoint decision, no cache.

## Checkpoint decisions (authoritative for this plan)

1. **Self-test scope:** integration test only (`//go:build integration`, S3
   sandbox-exec probes). No `doctor`/`setup` runtime wiring in this ticket.
2. **Allow action token:** `network-outbound` (tighter than `network*`; inbound stays
   denied even on allowlisted ports). Compiles fine (S3 used this form).
3. **Proxy fragment shape:** bare allow line + a documented ordering contract for
   AC-0023 (append `network.sb` before the proxy fragment, both after Safehouse's base).
4. **Caching:** none. Always regenerate `network.sb`. No `inputHash`/`readCompiled`
   machinery (design.md:295 calls regenerating the `.sb` "free"). Still
   deterministic + golden-tested. AC criterion #2's "not part of the input-hash-cached
   body" is trivially satisfied (nothing is cached); the *separate-function*
   requirement for the proxy fragment is what we honour.
5. **CLI wiring:** none. Library-only package, like `internal/policy/compile` today.

## Critical correction carried from the spikes

The ticket/design/spec all assume the literal-IP rule `(remote tcp "127.0.0.1:<port>")`.
**This does not compile** on macOS 26.5 (`host must be * or localhost`). Per S3 + S5
the compiler emits **`localhost:<port>`** (family-agnostic, port-enforced) and must
**never** emit `*:<port>`. This inverts the ticket's negative test (verification step
4): instead of forbidding `localhost`/`::1`, the generated output must *contain*
`localhost:` and must *not* contain `127.0.0.1`, `::1`, or a `"*:` host token.

## Current state

- No `internal/profile` package exists.
- `state.Layout.NetworkSB()` (`internal/state/state.go:158`) already returns the
  out-of-tree path `<state-root>/network.sb`.
- `config.HostService{Label, Port}` (`internal/config/config.go:60-63`) and
  `config.Loader.Load(projectConfigPath) (*Config, error)` (`internal/config/load.go:48`)
  already exist and produce a merged config with `Network.HostServices`.
- Template to mirror: `internal/policy/compile/compile.go` (write idiom),
  `compile_test.go` (golden harness + C4), `live_integration_test.go` (integration shape).

## Desired end state

`internal/profile` exports:

```go
package profile

const projectConfigName = ".agent-creance.yaml"

// RenderNetworkSB renders the cached-free network.sb body: the deny-all baseline
// plus one outbound allow per host service, deduped by port, in input order.
func RenderNetworkSB(services []config.HostService) string

// RenderProxyFragment renders the launch-time proxy-port allow line. It deliberately
// omits its own (deny network*); it relies on network.sb being appended first
// (ordering contract for AC-0023). Returns an error for an out-of-range port.
func RenderProxyFragment(port int) (string, error)

type Compiler struct { /* fs, loader, state */ }
func New(fsys sysdep.FileSystem, paths sysdep.PathResolver) (*Compiler, error)

type Result struct {
    ProfilePath string // absolute out-of-tree path written (== layout.NetworkSB())
    AllowCount  int    // number of host-service allow rules emitted
}
func (c *Compiler) Compile(projectDir string) (Result, error)
```

Generated `network.sb` for `host_services: [mysql:3306, redis:6379]`:

```scheme
;; agent-creance network.sb — appended after safehouse's base via --append-profile (AC-0023).
;; Deny-all network baseline; re-open only allowlisted host-service ports. Generated; do not edit.
(deny network*)
(allow network-outbound (remote tcp "localhost:3306"))   ;; mysql
(allow network-outbound (remote tcp "localhost:6379"))   ;; redis
```

Proxy fragment for port 18081:

```scheme
;; agent-creance proxy fragment — live ephemeral proxy port; regenerated per launch.
;; Relies on network.sb's (deny network*) being appended BEFORE this fragment (AC-0023).
(allow network-outbound (remote tcp "localhost:18081"))
```

---

## Phase 1 — Pure renderers + unit/golden tests

Create `internal/profile/profile.go` with the two pure render functions and the
shared SBPL string constants.

- `RenderNetworkSB(services)`: emit the two header comment lines, `(deny network*)`,
  then one `(allow network-outbound (remote tcp "localhost:<port>"))` per host
  service. **Dedupe by port** (keep first label for the trailing `;; <label>` comment)
  so duplicate ports don't yield duplicate rules; preserve input order otherwise.
  Empty `services` → header + `(deny network*)` only, no allow lines. Trailing newline.
- `RenderProxyFragment(port)`: validate `1 <= port <= 65535` (error otherwise, message
  in the `host_services entry %q ...` house style / `fmt.Errorf("profile: ...")`); emit
  the two comment lines + the single allow line + trailing newline.
- Use `strconv.Itoa`/`fmt.Sprintf` for ports; build with a `strings.Builder`.

Create `internal/profile/profile_test.go`:

- `var update = flag.Bool("update", false, "regenerate golden files")` — **exact**
  description string (Makefile `golden` target greps for it).
- `TestRenderNetworkSB_golden`: fixture `[mysql:3306, redis:6379]` → compare against
  `testdata/network.golden` (read/write real `os` under `testdata/`, honouring `-update`).
- `TestRenderNetworkSB_empty`: no services → deny baseline only, zero allow lines.
- `TestRenderNetworkSB_dedupeByPort`: two entries same port → one allow line.
- `TestRenderNetworkSB_noForbiddenLiterals` (corrected negative test): output contains
  `"localhost:"`, contains no `"127.0.0.1"`, no `"::1"`, and no `"*:"` host token.
- `TestRenderProxyFragment`: table — valid port → exactly one `localhost:<P>` allow
  line; `0`, `-1`, `65536` → error.
- `TestRenderProxyFragment_noForbiddenLiterals`: same forbidden-literal assertions.
- Create `internal/profile/testdata/network.golden` via `make golden`; review the diff.

### Success criteria
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/profile/...` passes.
- [ ] `make golden` produces `testdata/network.golden`; diff reviewed (matches the
      desired-end-state sample; `localhost`, never `127.0.0.1`/`::1`/`*:`).

---

## Phase 2 — Compiler (write `network.sb` out-of-tree)

Add the `Compiler`, `New`, `Result`, and `Compile` to `internal/profile`
(`profile.go` or a sibling `compile.go` in the same package).

- `New(fsys, paths)`: build `config.NewLoader(fsys, paths)` and `state.New(paths)`;
  store the `FileSystem`. Wrap errors `fmt.Errorf("profile: ...: %w", err)`.
- `Compile(projectDir)`:
  1. `layout, err := c.state.Resolve(projectDir)`.
  2. `cfg, err := c.loader.Load(filepath.Join(projectDir, projectConfigName))`.
  3. `body := RenderNetworkSB(cfg.Network.HostServices)`.
  4. Atomic out-of-tree write (mirror `compile.go:400-422`): `MkdirAll(layout.Root,
     0o755)` → `WriteFile(dest+".tmp", []byte(body), 0o644)` → `Rename(tmp, dest)` →
     on rename failure `Remove(tmp)`. `dest := layout.NetworkSB()`.
  5. Return `Result{ProfilePath: dest, AllowCount: <#rules>}`.
- Reuse the `dirPerm`/`filePerm`/`tmpSuffix` constants idiom from `compile.go:44-48`.

Add compiler tests to `internal/profile/profile_test.go` (or `compile_test.go`):

- Fixture builds the `Compiler` by struct literal, injecting
  `sysdeptest.NewFakeFileSystem()` (seeded with `projDir+"/.agent-creance.yaml"`
  containing `host_services`), `config.NewLoader(fsys, paths)`, `state.New(paths)`,
  with `paths := sysdeptest.NewFakePathResolver(); paths.HomeDir = testHome`.
- `TestCompile_writesNetworkSB`: assert `fsys.Files[res.ProfilePath]` equals the
  expected body (reuse the golden or render inline); assert `res.AllowCount`.
- `TestCompile_C4_noInTreeWrite`: `res.ProfilePath` has prefix `testHome`; no
  `fsys.Files`/`fsys.Dirs` entry sits under `projDir` (seed-allowlist the input yaml).
- `TestCompile_regeneratesEachTime`: compile twice; both write identical bytes; no
  `Skipped` concept exists (no cache) — assert the file is (re)written each call.

### Success criteria
- [ ] `go build ./...` compiles.
- [ ] `go test -race ./internal/profile/...` passes.
- [ ] C4: compile writes only under the out-of-tree state root (asserted in test).
- [ ] `make lint` clean.

---

## Phase 3 — Integration test (S3 localhost-refusal self-test)

Create `internal/profile/live_integration_test.go` (`//go:build integration`,
`package profile_test`), shipping the S3 self-test design against the **generated**
rules. This proves our rule strings actually enforce via `sandbox-exec`.

- Skip cleanly if `sandbox-exec` or `nc` is absent (`exec.LookPath`; `t.Skip`).
- Pick an allowlisted port `A` and a non-allowlisted port `O`. Bind throwaway TCP
  listeners (so a refusal is provably the sandbox, not a closed port):
  `nc -4 -k -l 127.0.0.1 A`, `nc -4 -k -l 127.0.0.1 O`, `nc -6 -k -l ::1 O`
  (start as backgrounded `exec.Cmd`; ensure cleanup). Mirror S3's captured commands.
- Build a **full standalone** probe profile in a temp dir: an S3-style header
  (`(version 1)`, `(deny default)`, broad non-network allows for fork/exec/file/
  mach-lookup/signal/system-socket) **followed by** `RenderNetworkSB([{label, A}])`
  (its `(deny network*)` + the `localhost:A` allow). `network.sb` alone is a fragment
  with no header, so the test composes it under a minimal header to make a runnable
  profile — Safehouse composition is S5's/AC-0023's domain, not re-tested here.
- Run probes `sandbox-exec -f <profile> nc -vz -G2 -w2 <-4|-6> <addr> <port>`:
  - `-4 127.0.0.1 A` → **success** (rc 0).
  - `-4 127.0.0.1 O` → **EPERM** ("Operation not permitted").
  - `-6 ::1 O` → **EPERM**.
- Assert the failure *kind* is EPERM, not ECONNREFUSED (only EPERM proves
  enforcement). Use `require`.

### Success criteria
- [ ] `make test-integration` passes on macOS with `sandbox-exec`+`nc` (the S3 probes
      go EPERM on the non-allowlisted port over v4 **and** v6; allowlisted port works).
- [ ] On a host without the tools the test skips, not fails.

---

## Phase 4 — Doc corrections + close ticket

The spikes explicitly assign the literal-IP wording corrections to the gated build
ticket (= AC-0014).

- `docs/design.md:53` (address-family caveat): reframe to **family-agnostic,
  port-based**. Rule is `(remote tcp "localhost:N")`, not literal `127.0.0.1`. Note
  `localhost` covers both v4+v6, the port is the discriminator, the intra-machine
  widening nuance (`localhost` = all local addresses), and "never emit `*:N`". Keep
  the shipped-self-test sentence (now the integration test).
- `docs/design.md:99-114` (`host_services` config comment): the "address ALWAYS forced
  to 127.0.0.1" / "address as 127.0.0.1, NOT localhost" guidance is backwards — the
  compiler emits `localhost:N`; caged tooling can reach services via `localhost` *or*
  `127.0.0.1` (the rule covers both families). Keep the `0.0.0.0`-binding honesty and
  the "label is cosmetic" point.
- `docs/design.md:295`: update to reflect the **two-fragment split** — `network.sb`
  (deny baseline + host-service allows) regenerated every launch and **not** input-hash
  cached, plus a separate launch-time proxy-port allow fragment; the proxy port is
  appended after `network.sb`.
- Spec `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`
  WP-2.5 (lines 189-194) and §14: annotate the `127.0.0.1:<port>` / "IPv4 literal
  enforced" wording with the `localhost:<port>` correction (light annotation — it is a
  dated discussion artifact; a one-line "**Correction (AC-0014):** …" is enough).
- Mark the ticket **Done** and fill its Implementation Plan / Notes sections.

### Success criteria
- [ ] design.md no longer claims the literal-`127.0.0.1` rule form or "force IPv4".
- [ ] Ticket status = Done; acceptance criteria checked with their spike-driven
      corrections noted.

---

## Acceptance-criteria mapping (with spike corrections)

| Ticket AC | How this plan satisfies it |
|---|---|
| `deny network*` baseline + per-host-service allows | Phase 1 `RenderNetworkSB`. **Correction:** `localhost:<port>`, not `127.0.0.1:<port>` (S3/S5). |
| Proxy-port allow = separate function, not in cached body | Phase 1 `RenderProxyFragment`. Body isn't cached at all (decision 4), so "not in cached body" holds; separate-function requirement met. |
| Deterministic + golden-tested for a representative set | Phase 1 golden (`mysql:3306, redis:6379`). |
| (Per S3) ships the localhost-refusal self-test | Phase 3 integration test (decision 1: integration only). |
| Composition matches S5 | `--append-profile` fragment, `localhost:N`, deny-before-allows; no wholesale profile. Ordering contract for AC-0023 documented. |

## Verification (automated — from `.claude/tce/profile.md`)

- `go build ./...` — compiles.
- `make test` — `go test -race ./...` green.
- `make lint` — `go vet` + `golangci-lint` clean.
- `make golden` — `network.golden` regenerated; diff reviewed (no `127.0.0.1`/`::1`/`*:`).
- `make test-integration` — S3 probes pass on macOS (skip without `sandbox-exec`/`nc`).

## Manual verification
- Review the golden `network.sb` body reads as a valid SBPL append fragment.
- Confirm the integration probes report EPERM (not ECONNREFUSED) on the non-allowlisted
  port over both families.

## Notes / out of scope
- Passing the profile to Safehouse via `--append-profile` (AC-0023), incl. the
  **ordering contract** (network.sb before proxy fragment).
- Lock-file/port allocation (AC-0020) — AC-0014 only consumes a port value.
- Runtime self-test wiring into `doctor`/`setup` (deferred; AC-0033 is the end-to-end
  isolation gate the probe folds into).
- No `internal/buildinfo` version bump (sandbox-exec is a system tool).
</content>
