---
date: 2026-06-08
ticket: AC-0033
topic: Adversarial cage verification harness ("fake agent" escape battery)
status: complete
git_commit: b23713903de9b93714f0b6429e83bf586799bd26
branch: main
repository: github.com/tobyS/agent-creance
---

# Research: AC-0033 — Adversarial cage verification harness (WP-4.5)

## Research question

How do we build a repeatable battery that launches a hostile "fake agent" **inside
the real cage** (`agent-safehouse` + `mitmproxy`) and asserts, per bullet of
`docs/design.md` → "What the cage prevents — and what it doesn't", that BLOCKED
vectors are refused, ALLOWED vectors succeed, and DOCUMENTED non-guarantees behave as
written — plus a negative control proving the harness can actually *fail* against a
weakened cage? This is the executable acceptance gate for Milestone M3.

## Summary / key findings

1. **The cage launch path is already a pure, test-drivable chain** and the agent
   command is fully data-driven (no hardcoded `claude`). A hostile payload is just a
   different `agent.command`. The existing `internal/cage/cage_integration_test.go`
   is a working miniature of exactly this harness (it already runs `nc 1.1.1.1 443`
   inside the real cage and asserts egress is denied). AC-0033 **generalizes and
   supersedes** that file's thin checks.

2. **Two proven recipes already exist in-repo** for the two halves of the matrix and
   should be composed, not reinvented:
   - **Seatbelt/kernel half** — `internal/cage/cage_integration_test.go`: build the
     invocation via `cage.New().Resolve → Prepare`, `cage.Build`, run via real
     `safehouse`, with a `sandbox_apply: Operation not permitted` skip-guard for
     hosts that can't nest sandbox-exec.
   - **Proxy half** — `internal/proxy/enforcer/test_integration.py`: spawn a real
     `mitmdump` with the extracted enforcer + a `policy.json`, drive curl through it,
     assert `403` + `X-Cage-Reason: soft-deny|hard-deny` + JSON body, and prove
     passthrough by trusting **only** the system CA.
   AC-0033's *novel* contribution is running these **composed and from inside the
   cage** (Seatbelt + proxy together, end-to-end), with a structured per-vector
   report and a negative control — on the **Go** side (no Go precedent yet for
   asserting through the live proxy).

3. **The refusal contract is fixed and assert-on-able** (exact constants below). The
   audit log is JSONL with a stable schema. These are the ground-truth literals the
   battery asserts.

4. **Tooling is present on this host** (`mitmdump`, `safehouse`, `curl`, `nc`, `go`,
   `sandbox-exec` all found; mitmproxy CA installed at
   `~/.mitmproxy/mitmproxy-ca-cert.pem`), so the battery will actually *run* here,
   not skip. We are outside any cage (required — sandbox-exec does not nest).

5. **Two genuine design forks** remain for the checkpoint: (a) hermetic-local vs.
   real-network fixtures for the ALLOWED egress vectors, and (b) payload form (Go
   binary vs. shell/curl). Recommendations below.

## The cage launch path (system under test)

The `run` command is thin orchestration; the testable seam is `runRun(ctx, app, dir)`
(`internal/cli/run.go:48-156`) and, lower, the cage builder chain that it calls:

- `cage.New(fs, paths).Resolve(cfg, layout, proxyPort)` → `Prepare(in)` →
  `cage.Build(in)` → `cage.NewRunner(pg).Run(ctx, inv)`
  (`internal/cage/cage.go`, `internal/cage/run.go`; called at `run.go:137-154`).
- **Agent command is data**: `in.Config.Agent.Command` (`[]string`) is appended after
  `--` in `cage.Build` (`internal/cage/cage.go:109-110`). `Build` errors if empty
  (`cage.go:73-75`). Nothing hardcodes `claude`. **→ a hostile payload binary is just
  `agent.command: ["/tmp/probe", ...]`.**
- **Injected env** (`buildEnv`, `cage.go:196-219`): `HTTP(S)_PROXY/http(s)_proxy =
  http://127.0.0.1:<port>`, `NO_PROXY/no_proxy = localhost,127.0.0.1,::1`,
  `NODE_EXTRA_CA_CERTS/SSL_CERT_FILE/REQUESTS_CA_BUNDLE/GIT_SSL_CAINFO = <mitmproxy CA
  PEM>`, `CLAUDE_CONFIG_DIR = <layout>/projects/<hash>/claude`. User-disallowed:
  computed vars overwrite user `config.env` so filtering can't be disabled.
- **Network deny-baseline**: `profile.RenderNetworkSB` emits `(deny network*)` first,
  then one `(allow network-outbound (remote tcp "localhost:<port>"))` per host service
  (`internal/profile/profile.go:39,46-48,54-68`); a second `--append-profile`
  (`proxy.sb`) re-opens only the proxy port (`profile.go:74-79`, written by
  `Builder.Prepare`, `cage.go:177-184`). Seatbelt is last-match-wins; ordering of the
  two `--append-profile` fragments is load-bearing (`cage.go:102-103`).
- **Real proxy lifecycle**: `proxy.NewManager(...).Attach(...)` spawns `mitmdump`
  with `--listen-host 127.0.0.1 --listen-port <ephemeral> -s <enforcer.py>
  --set creance_policy=<policy.json> --set creance_audit_log=<egress.jsonl> -q`
  (`internal/proxy/lifecycle.go:394-403`); flock-guarded; liveness = live PID **and**
  TCP probe of the port (`lifecycle.go:122`). The enforcer is `//go:embed`-extracted
  via `proxy.NewExtractor(...).Extract()` (`internal/proxy/extract.go`).
- **Hermetic out-of-tree state**: everything (policy.json, network.sb, proxy.sb,
  proxy.lock, egress.jsonl, claude/) lands under `XDG_CACHE_HOME`; tests set
  `t.Setenv("XDG_CACHE_HOME", t.TempDir())` (`state.go:199-208`;
  `cage_integration_test.go:96`).

## The refusal + audit contract (assert-on literals)

From `internal/proxy/enforcer/{policy.py,responses.py,audit.py,enforcer.py}` and the
golden fixtures in `internal/proxy/enforcer/testdata/`:

- **Decisions**: `allow` / `soft-deny` / `hard-deny` (`policy.py:46-48`). Passthrough
  is a *mode* (`intercept`/`passthrough`), not a decision (`policy.py:51-52`).
- **Both denials → HTTP 403**, `Content-Type: application/json`, header
  `X-Cage-Reason: soft-deny` or `hard-deny` (`responses.py:20-22,42-43`).
  - soft-deny body `error = agent_cage_not_allowlisted`, fields
    `error,url,host,path,method,how_to_proceed,allow_command_suggestion`.
  - hard-deny body `error = agent_cage_hard_deny`, fields
    `error,url,reason,how_to_proceed` (`reason` = matched deny rule's reason).
- **Allowlisted host, disallowed path/method → soft-deny** (falls through `decide`,
  `policy.py:174`), identical shape to a never-allowlisted host.
- **Passthrough**: `tls_clienthello` sets `data.ignore_connection = True` for a
  passthrough host with no host-level deny → blind TLS relay, real upstream cert
  presented; `connection_strategy=lazy`, `upstream_cert=False` (`enforcer.py:98-185`).
  A host-level `deny_always` on a passthrough host is refused at CONNECT
  (`enforcer.py:156-173`); a *path-scoped* deny is invisible on a tunnelled host
  (documented blind spot, `policy.py:156-157`).
- **Audit JSONL** (`egress.jsonl`, 0600, append, `audit.py`): intercepted entry
  `{ts,method,url,decision,rule,status}` (`rule` = `{list,index}` or null);
  passthrough entry `{ts,host,decision}` only. **POST bodies are NOT recorded**; URLs
  scrubbed for sensitive query params (`audit.py:56-94,97-120`).
- **DNS**: the cage's only egress is the loopback proxy (Seatbelt); mitmproxy does the
  resolution (`connection_strategy=lazy`). Direct queries to an external nameserver
  are blocked by the deny-baseline (`docs/design.md:52,59`).

## Threat-model → assertion mapping (the matrix to encode)

Each acceptance bullet maps to a named `docs/design.md` line. The battery must carry
this mapping as a comment/table so the threat model and the test can't drift (AC
criterion line 63).

| Vector | Label | design.md | How to exercise |
|---|---|---|---|
| Read `~/.ssh/id_rsa`, `~/.aws/credentials` | BLOCKED-fs | :51 | payload `open()` outside `./` → EPERM/denied |
| Read/write real `~/.claude` | BLOCKED-fs | :51,:68 | payload stat/write `$HOME/.claude` → denied; real `~/.claude` never mounted (`cage.go:14-18,84`) |
| Raw outbound TCP bypassing proxy (`1.1.1.1:443`) | BLOCKED-net | :52 | raw `connect()` no proxy → EPERM (precedent: `cage_integration_test.go:59-79`) |
| Non-allowlisted localhost port over v4 **and** v6 | BLOCKED-net | :53 | listener on `127.0.0.1`/`[::1]:p` (not in host_services); raw connect → refused both families (precedent: `profile/live_integration_test.go`) |
| Child process inherits restrictions | BLOCKED-net | :54 | payload forks a subprocess that re-runs a blocked vector → still blocked |
| Egress to non-allowlisted host via proxy | BLOCKED-proxy | :57 | request `https://not-allowlisted.test/` → 403 soft-deny |
| Egress to `deny_always` host | BLOCKED-proxy | :57 | `https://blocked.test/` → 403 hard-deny + reason |
| Disallowed path/method on allowlisted host | BLOCKED-proxy | :57 | allowlisted host + off-path → 403 soft-deny |
| Direct DNS to external nameserver | BLOCKED-net | :59 | raw UDP/TCP to `8.8.8.8:53` → blocked by deny-baseline |
| Egress to allowlisted host/path/method | ALLOWED | :56-57 | 200 upstream, no `X-Cage-Reason` |
| Generated-rule host (dep homepage/repo) | ALLOWED | generator §:163 | allowlisted via generator → 200 |
| `host_services` `127.0.0.1:<port>` | ALLOWED | :53 | local listener on an allowlisted port → connects |
| `mode: passthrough` host | ALLOWED | :68 | tunnels; validates **real** upstream cert (system CA only) |
| `rm`/write within `./` succeeds | DOCUMENTED | :62 | payload writes/deletes in project dir → succeeds (cage does NOT block) |
| POST body to allowlisted host succeeds + audited | DOCUMENTED | :68 | POST through proxy → 200, audit entry recorded (no body) |
| Ephemeral `CLAUDE_CONFIG_DIR` writable; plant doesn't reach real `~/.claude` | DOCUMENTED | :68 | write hook into `$CLAUDE_CONFIG_DIR`; assert real `~/.claude` unchanged after |
| **Negative control**: deny-baseline removed / host-service widened → battery reports an escape | INTEGRITY | :62 | run battery against a deliberately-weakened profile/policy fixture; assert it FAILS |

## Existing patterns to reuse (do not reinvent)

- **Skip-guards** (`cage_integration_test.go:81-89,176-194`): `runtime.GOOS !=
  "darwin"` → skip; `exec.LookPath(cage.Binary)` → skip; `sandbox_apply: Operation
  not permitted` substring → skip ("run on an unsandboxed host").
- **Hermetic state** (`setupLayout`, `cage_integration_test.go:94-103`):
  `t.Setenv("XDG_CACHE_HOME", t.TempDir())` + resolve layout + seed `network.sb`.
- **`realApp(buf)`** (`internal/cli/doctor_fix_integration_test.go:108-127`): wires
  all real `OS*` sysdep seams exactly like `cli.Main` — the way to drive `runRun`.
- **Liveness via socket, not PID** (`clean_integration_test.go`,
  `doctor_fix_integration_test.go:85-89`): assert `pa.Probe(port)` /
  `!pa.Probe(port)`, never `kill(pid,0)` (the test is the proxy's parent; a SIGTERM'd
  proxy lingers as a zombie that `kill(pid,0)` still reports alive).
- **Real mitmdump + enforcer + policy + curl assertions**
  (`enforcer/test_integration.py:74-331`): `running_proxy(policy_obj)` ctx-manager,
  `_wait_until_ready` (socket connect + CA file exists), `_curl(..., use_mitm_ca=)`,
  `_wait_for_audit` JSONL poll. Passthrough proven by `use_mitm_ca=False`.
- **Local listeners** (`profile/live_integration_test.go:105-136`): `freePort`,
  `startListener` (nc) with `t.Cleanup` kill, `waitListening` dual-stack `-4`/`-6`.

## Key gotchas (from web research + code)

- **Go ignores HTTP(S)_PROXY for loopback hosts** (`localhost`/`127.0.0.1`/`::1`):
  `http.ProxyFromEnvironment` returns nil for them ([golang/go#33695]). A Go payload
  talking to a loopback upstream would **silently bypass** the proxy. Fixes: set
  `Transport.Proxy = http.ProxyURL(...)` explicitly, or use a non-loopback hostname,
  or shell out to `curl` (always honors `-x`). The injected env also sets
  `NO_PROXY=...,127.0.0.1,::1`, reinforcing this.
- **Seatbelt `network-outbound` deny surfaces as `EPERM`/`EACCES` on `connect(2)`** —
  the discriminator that proves the *sandbox* blocked it vs. `ECONNREFUSED`
  (nothing-listening). Classify with `errors.As(err, &syscall.Errno)` /
  `errors.Is(err, syscall.EPERM)`. Pin a target that *would* connect/refuse uncaged so
  EPERM is unambiguous.
- **sandbox-exec does not nest** — `forbidden-sandbox-reinit` /
  `sandbox_apply: Operation not permitted`. The test runner must be un-sandboxed (we
  are). Keep the existing skip-guard.
- **Passthrough cert proof**: trust **only** `ts.Certificate()` (or system CA), never
  mitmproxy's CA; a successful handshake then proves tunneling. Mirror test: an
  intercept host must FAIL against system-CA-only.
- **`SSL_CERT_FILE` replaces** the trust store (Go/curl); `NODE_EXTRA_CA_CERTS`
  appends. For a polyglot or real-upstream-plus-mitm payload, point at a bundle that
  concatenates system roots + mitmproxy CA, or pick the right var per probe.

## Fixture-provisioning options (the ALLOWED egress vectors)

- **host_services ALLOWED** is inherently local: an `nc`/`net.Listen` listener on
  `127.0.0.1:<port>` with that port in `host_services` — fully hermetic, no network.
- **allowlisted-host 200 / generated-rule host / passthrough** go *through the proxy
  to an upstream*. Two strategies:
  - **(A) Real public hosts** (`example.com` intercept, `example.org` passthrough) +
    skip-on-no-egress. This is the **established project convention**
    (`enforcer/test_integration.py:37-39,217-221`); simplest, least new infra; the
    only skip is "no network".
  - **(B) Fully-local httptest fixtures.** Hermetic but needs: a hostname mitmproxy
    can resolve to the fixture (loopback name), `ssl_insecure` for the
    intercept-upstream TLS (httptest self-signed cert), and the cert-pool trick for
    passthrough. More moving parts; risk of testing the fixture, not the cage.
- **BLOCKED-proxy vectors need NO egress**: mitmproxy returns the 403 locally
  (`.test` hosts), so soft/hard-deny + path/method assertions are hermetic regardless.

## Negative control approach (harness integrity)

Build a deliberately-weakened cage from a **test-only** fixture and assert the battery
detects the escape:
- **Network weakening**: render a `network.sb` *without* the `(deny network*)`
  baseline (or run with a host-service widened to the blocked port), then re-run a
  BLOCKED-net vector and assert it now **succeeds** → the battery must flag an escape.
- These fixtures are constructed in-test (never `//go:embed`-shipped, never written to
  a real layout path), closing the ticket's Q4 risk of shipping a weakened profile.
  The weakened profile is produced by calling the renderer with the deny line stripped
  in the test, or by passing a hand-built `.sb` string — it never lives in
  `internal/profile` golden data as a "real" profile.

## Coverage-mapping check (anti-drift)

A test (or `make` target) parses `docs/design.md`'s "Prevented/Not prevented" bullets
and asserts each maps to a labeled assertion (AC line 63, V&T step 5). Simplest form:
a table in the harness keyed by a stable design-bullet id, plus a test that fails if a
design bullet has no entry. The mapping table above is the seed.

## Open questions for the planning checkpoint

1. **ALLOWED-egress fixtures: real public hosts (A) or fully-local (B)?** This is the
   M3 *acceptance gate* — whether it may skip when offline is a policy call.
   *Recommendation: hybrid — local for host_services + all BLOCKED + DOCUMENTED;
   real public hosts (A) for allow-200/generated-rule/passthrough, skip-on-no-egress,
   matching the existing enforcer integration test.*
2. **Payload form (ticket Q1): checked-in Go binary, shell/curl, or JSON-driven
   runner?** *Recommendation: a small checked-in Go "fake agent" (`package main`
   under `internal/...`), built by the test via `go build`, emitting structured JSON
   per-probe results — gives errno classification (EPERM vs ECONNREFUSED) and
   explicit proxy control the loopback gotcha demands; curl used only where it
   matches the proven Python recipe.*
3. **Scope of this pass**: full matrix + negative control + coverage-mapping +
   `docs/cage-verification.md` manual checklist, committed phase-by-phase.
   *Recommendation: yes, full — it is the M3 gate — but phased so each commit is green.*

## References

- Ticket: `thoughts/shared/tickets/AC-0033-adversarial-cage-verification.md`
- M3 gate: `thoughts/shared/discussions/2026-06-04-v0.1-technical-specification.md`
- Threat model: `docs/design.md:48-70`
- Seatbelt half (pattern): `internal/cage/cage_integration_test.go`
- Proxy half (pattern): `internal/proxy/enforcer/test_integration.py`
- Refusal/audit contract: `internal/proxy/enforcer/{policy,responses,audit,enforcer}.py`
- Run/cage/proxy: `internal/cli/run.go`, `internal/cage/{cage,run}.go`,
  `internal/proxy/{lifecycle,extract}.go`, `internal/profile/profile.go`
- Spikes: `thoughts/shared/research/2026-06-04-s{1,2,3,4}-*.md`,
  `2026-06-05-s5-append-profile.md`
