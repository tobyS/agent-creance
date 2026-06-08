---
date: 2026-06-08
ticket: AC-0034
title: "In-cage CA-trust env-var files point at an unreadable path"
status: complete
branch: main
commit: c314332d544b92fcc012713ea09654cac00100a0
researcher: Claude (tce:work)
---

# AC-0034 — Caged tools can't load the mitmproxy CA from the injected env-var files

## Research question

The cage injects four CA-trust env vars (`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
`REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`) all pointing at
`~/.mitmproxy/mitmproxy-ca-cert.pem`, but agent-safehouse denies reading
`~/.mitmproxy` inside the cage. How do we (1) reproduce the in-cage TLS failure
for env-var-CA clients, (2) make the single CA PEM readable in-cage with the
narrowest possible filesystem change, and (3) guard it with an added AC-0033
vector — without breaking keychain-based clients or broadening cage FS access?

## Summary / answer

The bug is real and the root cause is precisely as the ticket states. The four CA
env vars are computed purely from `$HOME` (`caCertPath(home) =
<home>/.mitmproxy/mitmproxy-ca-cert.pem`) and that path is **never** added to any
safehouse mount or read-grant. agent-safehouse's base policy is `(deny default)`,
so `~/.mitmproxy` is unreadable in-cage — confirmed at runtime by AC-0033
(`cat ~/.mitmproxy/mitmproxy-ca-cert.pem` → `Operation not permitted`). The S4
spike that "validated" these env vars ran every probe as a **host** process, where
`~/.mitmproxy` *is* readable, so it never exercised the in-cage path. macOS
`curl`/CFNetwork and `go` trust the CA via the keychain (`trustd`) and are
unaffected; node/npm and python-requests trust it **only** via these files and
will fail TLS through the proxy in-cage.

agent-creance composes safehouse only through its documented extension points
(`--append-profile`, `--add-dirs`/`--add-dirs-ro`, `--enable=`). It emits **no
filesystem rules today** — the two `--append-profile` fragments it generates are
network-only. There are exactly **two viable fix shapes**, both consistent with
the AC's "narrowest change" / "don't expose the private key" constraints:

- **Option A — single-file SBPL read-grant via `--append-profile`.** Emit
  `(allow file-read* (literal "<home>/.mitmproxy/mitmproxy-ca-cert.pem"))` (plus,
  likely, a stat/`file-read-metadata` grant on the `~/.mitmproxy` directory for
  path traversal — to be confirmed by the repro). The four env vars stay
  unchanged. Narrowest possible: exactly one file, no private-key exposure, no
  source-tree pollution. Cost: a new filesystem responsibility for the
  profile/cage layer (today network-only), and Seatbelt path-traversal semantics
  must be validated in-cage (never exercised before).

- **Option B — copy the CA into an already-mounted path and repoint the four env
  vars at the copy.** Mirrors exactly what the AC-0033 battery already does with
  its in-mount `CREANCE_CA` copy. Cost: needs a destination that is genuinely
  mounted + readable in-cage. The project dir (`.`) is mounted RW but copying the
  CA there pollutes the user's source tree and contradicts the
  "keep-security-state-out-of-tree" rule in `CLAUDE.md`. The out-of-tree state dir
  (`~/.cache/agent-creance/projects/<hash>/`) is **not** currently mounted (this is
  the sibling AC-0035 gap), so Option B would need to *also* add a mount —
  coupling it to AC-0035.

**Rejected — Option C (`--add-dirs-ro ~/.mitmproxy`).** Simplest, but
`~/.mitmproxy` also contains the CA **private key** (`mitmproxy-ca.pem`), so this
violates the AC's explicit "no general `~/.mitmproxy` read-grant if a narrower
option exists."

This is a genuine design fork (A vs B) requiring human judgment — surfaced at the
Phase 2 checkpoint.

## The bug — exact code path

### The env vars are computed from `$HOME`, never from a mount
- `internal/cage/cage.go:146-148` — `caCertPath(home)` returns
  `filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem")`. Pure function of
  the home dir; no consultation of `state.Layout`, the cache dir, or any mount.
- `internal/cage/cage.go:140` — `Resolve` sets `CACertPath: caCertPath(home)`,
  where `home = b.paths.UserHomeDir()` (`cage.go:131`).
- `internal/cage/cage.go:211-213` — `buildEnv` sets all four vars to the same
  value in one loop:
  ```go
  for _, k := range []string{"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "GIT_SSL_CAINFO"} {
      env[k] = in.CACertPath
  }
  ```
  The comment at `cage.go:192-195` still asserts the S4 (host-validated) rationale.

### Nothing grants the cage read access to that path
- `internal/cage/cage.go:85-97` — the only FS-widening flags emitted:
  `--add-dirs` (from `safehouse.add_dirs_rw`), `--add-dirs-ro` (from
  `safehouse.add_dirs_ro`), `--enable=`, `--workdir`. All take **directory**
  paths; there is no single-file variant. Defaults are `add_dirs_rw: [.]`,
  `add_dirs_ro: [~/.config/git]` (`internal/config/testdata/example.yaml:8-9`).
- `internal/cage/cage.go:99-103` — exactly two `--append-profile` fragments, both
  network-only: `network.sb` (deny-all baseline) then `proxy.sb` (live proxy
  port).
- `internal/profile/profile.go:42,49-51` — the profile package emits only
  `(deny network*)` + per-port `(allow network-outbound (remote tcp
  "localhost:<port>"))`. **No `file-read*` rule renderer exists anywhere.**
- `internal/cage/testdata/invocation.golden.json:23,27,29,30` — golden pins all
  four vars at `/home/test/.mitmproxy/mitmproxy-ca-cert.pem`; the only RO mount in
  the golden is `~/.config/git`. `~/.mitmproxy` is absent from every flag.

### The base policy denies it
- agent-safehouse's base opens with `(version 1)` / `(deny default)`
  (`thoughts/shared/research/2026-06-05-s5-append-profile.md:102-104`). Any read
  not explicitly allowed is denied; `~/.mitmproxy` is not on the base allowlist.
- Confirmed at runtime by AC-0033:
  `cat ~/.mitmproxy/mitmproxy-ca-cert.pem` in-cage → `Operation not permitted`
  (`thoughts/shared/tickets/AC-0034-incage-ca-trust-env-files.md:19-22`).

## Why S4 missed it (root cause of the gap)

- `thoughts/shared/research/2026-06-04-s4-proxy-env.md:56-61` — the CA was supplied
  "via the agent-creance env set ONLY … confirmed absent from both the login
  keychain and the System keychain." Every probe ran as a **host** process; the
  doc never mentions agent-safehouse, the cage, or filesystem confinement (its
  Limitations, `:211-227`, confirm host-only, "lightweight commands only").
- S4 matrix verdicts (`:82-155`): claude/npm/pip/git-over-HTTPS PASS **via the
  env-var CA files**; `go` PASSes routing but trusts the CA **only via the system
  keychain** — control D (`:137-141`) proves `go` *ignores* `SSL_CERT_FILE`.
- S1 (`thoughts/shared/research/2026-06-04-s1-ca-trust.md:40-45`) — the CA PEM is
  generated by the first `mitmdump` run at `~/.mitmproxy/mitmproxy-ca-cert.pem`;
  S1 tested env-var trust only and explicitly defers keychain install to AC-0026.

**Keychain vs. env-var split (the matrix to re-confirm in-cage):**

| Client | Trusts CA via | In-cage status (hypothesis) |
|--------|---------------|------------------------------|
| macOS `curl` / CFNetwork | keychain (`trustd`) | works (keychain unaffected) |
| `go` (macOS) | keychain only (ignores `SSL_CERT_FILE`) | works (keychain unaffected) |
| node / npm | `NODE_EXTRA_CA_CERTS` file | **FAILS** (file unreadable) |
| python `requests` / pip | `REQUESTS_CA_BUNDLE` / `SSL_CERT_FILE` file | **FAILS** (file unreadable) |
| git over HTTPS | `GIT_SSL_CAINFO` file | **FAILS** unless it also hits keychain |

The AC requires re-confirming this matrix **from inside the cage**, not on the
host.

## How agent-safehouse composition works (constraints on the fix)

- `docs/design.md:34` — "agent-creance uses Safehouse's documented extension points
  (`--append-profile`, `--env-pass`, `--add-dirs`) and never modifies it."
- `docs/design.md:44,486` — filesystem and process boundaries are "entirely
  Safehouse's"; agent-creance's generated profile is network-only by design.
- `thoughts/shared/research/2026-06-05-s5-append-profile.md:61-64` — `--append-profile
  PATH` appends arbitrary SBPL after the generated base rules (repeatable, in
  argument order). It is a **general** SBPL-append hook, so it *could* carry a
  `(allow file-read* …)` line — but S5 only ever validated **network** rules
  through it (`:16,206`). Filesystem widening via append-profile is unproven and
  must be validated by the repro.
- Known-good in-cage writable/readable targets per safehouse base: `/tmp`,
  `$TMPDIR`, specific toolchain dirs — **not** a generic `~/.cache`. So an Option-B
  copy under `~/.cache/agent-creance` is not readable in-cage unless that dir is
  explicitly mounted.
- `SessionOverlay` (`internal/state/state.go:240`) is a **host-side** proxy
  allow-overlay file consumed by the proxy manager; it is not mounted into the
  cage and is not a CA-copy destination.
- `docs/cage-verification.md:116-123` — "Known limitations" item 1 documents this
  exact gap; the fix should update/remove this item.

## The AC-0033 battery — how to add the guarding vector

The battery lives in `internal/verify/`. Adding a vector touches three files
mechanically:

1. **Register the vector** — append a `Vector` literal to the `Vectors` slice in
   `internal/verify/matrix.go:51-136`. Struct (`matrix.go:34-42`): `ID` (emit
   token), `Label` (`BLOCKED`/`ALLOWED`/`DOCUMENTED`, `matrix.go:17-27`),
   `Expected` (green-run observation), `Keyword` (must appear in `design.md`'s
   threat-model section for non-ALLOWED vectors), `DesignRef`, `Egress`, `Desc`.
   The natural shape here is **`Label: LabelAllowed, Expected: "200",
   Egress: true`** — a false-negative guard proving an env-var-CA client gets a
   200 through the proxy. ALLOWED vectors are **exempt** from the design-drift
   guard (`coverage_test.go:43-44`), so no `design.md` edit is forced.
2. **Emit the probe from `internal/verify/testdata/fake-agent.sh`** — add a block
   producing `CREANCE::<id>::<observed>`. Crucially this probe must **rely on the
   injected env-var CA** (e.g. node `https.get`, or curl/python *without*
   `--cacert`), i.e. it must NOT be covered by the `unset` at `fake-agent.sh:23`
   and must NOT use the `--cacert "$CREANCE_CA"` workaround (`:89,99,110,130,157`).
   Gate egress probes on `[ "$CREANCE_EGRESS" = "1" ]` else `emit <id> skip`
   (mirror `:129-142`).
3. **Evaluator/harness pick it up automatically.** `Evaluate` iterates `Vectors`
   (`battery.go:61-94`); the live harness `runBattery`
   (`verification_integration_test.go:135-221`) launches the real cage, copies the
   CA into the RW mount as `CREANCE_CA`, starts a real `mitmdump`, and feeds output
   through `ParseProbeOutput`→`Evaluate`. Negative control
   (`verification_integration_test.go:107-130`) runs the same battery against a
   weakened cage. The only hard-coded count to bump is `docs/cage-verification.md:40`
   ("all 16 vectors PASS" → 17).

`TestVectorsWellFormed` (`battery_test.go:113-123`) requires a unique, non-empty
`ID`/`Expected`/`Keyword`. The fast `make test` suite covers parser/evaluator
/drift only; the live probe runs under `make test-integration`.

## Key files

| Concern | File:line |
|---------|-----------|
| CA env injection / `caCertPath` | `internal/cage/cage.go:146-148, 211-213` |
| safehouse argv (`--add-dirs*`, `--append-profile`) | `internal/cage/cage.go:85-103` |
| Builder.Prepare (writes proxy.sb into state dir) | `internal/cage/cage.go:163-186` |
| network-only profile renderer | `internal/profile/profile.go:42,49-51` |
| out-of-tree state layout (not mounted) | `internal/state/state.go:75-208, 236, 240` |
| setup: CA generate + keychain install (no FS copy) | `internal/setup/setup.go:84-167, 252-268` |
| run.go wiring/order | `internal/cli/run.go:48-156` |
| battery matrix / evaluator | `internal/verify/matrix.go`, `battery.go` |
| live harness + CREANCE_CA copy | `internal/verify/verification_integration_test.go:161-172` |
| fake-agent unset + --cacert workaround | `internal/verify/testdata/fake-agent.sh:14-23` |
| invocation golden | `internal/cage/testdata/invocation.golden.json` |
| known-limitation to update | `docs/cage-verification.md:116-123` |

## Open questions for the planning checkpoint

1. **Fix mechanism: Option A (single-file SBPL read-grant) vs Option B (copy CA to
   a mounted dir + repoint env vars)?** A is narrowest and keeps env vars
   unchanged but introduces a filesystem rule to a network-only profile layer and
   needs Seatbelt traversal validation. B mirrors the existing battery workaround
   but needs a mounted destination (couples to AC-0035 since the state dir isn't
   mounted today; the project dir is mounted RW but pollutes the source tree).
2. **Repro vehicle:** extend `fake-agent.sh` with a new env-var-CA probe (lands the
   guard and the repro together), or build a throwaway standalone repro first? The
   battery vector is required by the AC regardless; a dedicated red→green repro can
   ride on the same probe.
3. **Which clients to prove in-cage:** the AC names node/npm and python-requests as
   the failing set; `go`/curl are keychain-backed. Is proving one representative
   env-var-CA client (e.g. curl-without-`--cacert` honoring `SSL_CERT_FILE`, or a
   node `https.get`) sufficient, or must the vector cover node *and* python?
