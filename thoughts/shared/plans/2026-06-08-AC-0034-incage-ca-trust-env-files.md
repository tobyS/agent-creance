---
date: 2026-06-08
ticket: AC-0034
title: "Make the mitmproxy CA PEM readable in-cage via a single-file SBPL read-grant"
status: planned
branch: main
research: thoughts/shared/research/2026-06-08-AC-0034-incage-ca-trust-env-files.md
---

# AC-0034 — In-cage CA-trust for env-var-file clients

## Overview

The cage injects four CA-trust env vars (`NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`,
`REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`) pointing at the single global mitmproxy CA
`~/.mitmproxy/mitmproxy-ca-cert.pem`, but agent-safehouse's `(deny default)` base
denies reading `~/.mitmproxy` in-cage. Clients that trust the CA only via these
files (node/npm, python) therefore fail TLS through the proxy. macOS
`curl`/CFNetwork and `go` are keychain-backed and unaffected.

**Chosen fix (decided at the planning checkpoint): Option A — single-file SBPL
read-grant.** Because the CA is one global host-wide cert at a stable path (not
per-project), we emit a third `--append-profile` fragment granting in-cage
`file-read*` to exactly that one PEM (plus a metadata grant on its parent dir for
path traversal). The four env vars stay pointing at the real path, unchanged. This
is the narrowest possible change: one file, no private-key exposure (the sibling
`mitmproxy-ca.pem` private key remains unreadable), no source-tree pollution.

**Guard (decided at the checkpoint): node + python both.** The AC-0033 battery is
extended with two ALLOWED false-negative vectors that drive node and python TLS
through the proxy using the *injected env-var CA* (not the existing `--cacert`
workaround), so a regression of the read-grant turns the battery red.

**Order is repro-first** (per the AC): Phase 1 adds the two probes + vectors and
proves they FAIL in-cage (the reproduction); Phase 2 adds the read-grant and
proves they PASS while keychain clients still pass; Phase 3 updates docs/goldens.

## Current state

- `internal/cage/cage.go:146-148` — `caCertPath(home)` = `<home>/.mitmproxy/mitmproxy-ca-cert.pem`.
- `internal/cage/cage.go:211-213` — `buildEnv` sets the four CA vars to `in.CACertPath`.
- `internal/cage/cage.go:99-103` — two `--append-profile` fragments today: `network.sb`, `proxy.sb`.
- `internal/cage/cage.go:163-186` — `Prepare` does the launch-time I/O (has `in.CACertPath`).
- `internal/profile/profile.go` — network-only SBPL renderers (`RenderNetworkSB`, `RenderProxyFragment`, `allowRule`, `DenyBaseline`); last-match-wins ordering doc.
- `internal/state/state.go:39-40, 214-220` — `networkSBName`/`proxyProfileSBName` consts + `NetworkSB()`/`ProxyProfileSB()` accessors.
- `internal/verify/matrix.go:51-136` — 16 vectors. `internal/verify/testdata/fake-agent.sh:23` unsets the four CA vars; proxied probes use `--cacert "$CREANCE_CA"` (an in-mount copy).
- `internal/verify/verification_integration_test.go` — live harness; `requireUncagedHost` gates on macOS + safehouse/mitmdump/curl/nc; egress vectors gated on `CREANCE_EGRESS`.
- `internal/cage/testdata/invocation.golden.json` — pins the argv incl. the two `--append-profile`s.
- `docs/cage-verification.md:40,116-123` — "all 16 vectors PASS"; Known-limitation #1 documents this exact gap.

## Desired end state

- A third `--append-profile <…/ca.sb>` is emitted; in-cage, the one CA PEM is
  readable, its sibling private key is not.
- node and python clients using only the injected env-var CA complete TLS through
  the proxy to an allowlisted host (200) from inside the cage.
- `curl`/keychain clients continue to work unchanged (the negative control and the
  existing 16 vectors stay green).
- Two new ALLOWED battery vectors (`env-ca-node`, `env-ca-python`) guard the fix;
  the live count is 18.
- Known-limitation #1 is rewritten to reflect that the env-var CA files now work
  in-cage; goldens regenerated.

## Key decisions & rationale

- **Single-file grant over directory mount.** `--add-dirs-ro ~/.mitmproxy` is
  rejected: that dir holds the CA *private key* (`mitmproxy-ca.pem`). A literal
  `file-read*` on just `mitmproxy-ca-cert.pem` honors the AC's "no broader access
  than needed."
- **Third dedicated fragment, written in `Prepare`.** The grant is host-static
  (depends only on the home-dir-derived CA path, available as `in.CACertPath`).
  `Prepare` already performs launch-time fragment I/O. Folding it into `network.sb`
  would couple policy compilation (`run.go`) to home-dir resolution; folding into
  `proxy.sb` conflates it with the per-launch port. A separate `ca.sb` keeps each
  fragment single-purpose and mirrors the existing pattern.
- **Resolve symlinks before emitting the literal.** macOS firmlinks mean the kernel
  may see `/System/Volumes/Data/Users/...` while `UserHomeDir()` returns
  `/Users/...`. Seatbelt literal matching is on the resolved path, so the renderer
  must `EvalSymlinks` the CA path (falling back to the raw path if the file is
  absent, e.g. setup not yet run). **This must be validated by the Phase 2 repro**;
  if a bare file-literal still won't open, add `(allow file-read-metadata (literal
  <dir>))` for the parent, and only if that fails escalate.
- **Probes use the injected env var, not `CREANCE_CA`.** The whole point is to prove
  the injected `~/.mitmproxy/...` path is now readable; the probes capture the
  injected value *before* the script's `unset` and re-apply it per-probe.
- **Per-probe skip when node/python absent.** Don't add them to
  `requireUncagedHost` (that would skip the entire battery); instead emit `skip`
  when the tool isn't found, matching the existing egress-skip convention. node is
  expected to run in-cage (the cage exists to run a node agent); `/usr/bin/python3`
  is a system binary.

## What we're NOT doing

- Not changing how `setup` installs/keychain-trusts the CA (AC-0026 territory).
- Not removing the env-var CA injection.
- Not touching the `CLAUDE_CONFIG_DIR` writability gap (AC-0035).
- Not mounting any new directory.

---

## Phase 1 — Reproduce the failure: add probes + vectors (RED)

### 1a. Add two ALLOWED vectors — `internal/verify/matrix.go`

Append to the `Vectors` slice (in the ALLOWED group, `matrix.go:104-119`):

```go
{
    ID:        "env-ca-node",
    Label:     LabelAllowed,
    Expected:  "200",
    Keyword:   "NODE_EXTRA_CA_CERTS", // ALLOWED vectors are exempt from the design-drift guard
    DesignRef: "AC-0034",
    Egress:    true,
    Desc:      "node trusts the injected env-var CA file and gets 200 through the proxy in-cage",
},
{
    ID:        "env-ca-python",
    Label:     LabelAllowed,
    Expected:  "200",
    Keyword:   "SSL_CERT_FILE",
    DesignRef: "AC-0034",
    Egress:    true,
    Desc:      "python trusts the injected env-var CA file and gets 200 through the proxy in-cage",
},
```

(ALLOWED ⇒ a block is a `Failed` vector, not an `Escape` — correct for a
false-negative regression guard. `TestVectorsWellFormed` only needs unique,
non-empty ID/Expected/Keyword; the drift guard skips ALLOWED vectors.)

### 1b. Add the two probes — `internal/verify/testdata/fake-agent.sh`

- **Before** the `unset` at line 23, capture the injected CA path:
  ```sh
  CREANCE_INJECTED_CA="${NODE_EXTRA_CA_CERTS:-$SSL_CERT_FILE}"
  ```
  (all four injected vars hold the same value).
- In the egress block (`if [ "$CREANCE_EGRESS" = "1" ]`), add node + python probes
  that rely on the injected CA and hit the intercept host `$CREANCE_ALLOW_HOST`
  through `$HTTPS_PROXY` with **no `--cacert`**. Skip per-tool when absent. Sketch:
  ```sh
  # node: trust the proxy CA ONLY via NODE_EXTRA_CA_CERTS (the injected env file).
  if command -v node >/dev/null 2>&1; then
      code=$(NODE_EXTRA_CA_CERTS="$CREANCE_INJECTED_CA" node -e '
          const https=require("https");
          https.get(process.env.U,{timeout:25000},r=>{console.log(r.statusCode);process.exit(0)})
               .on("error",e=>{console.log("ERR:"+e.code);process.exit(0)});
      ' 2>/dev/null U="https://$CREANCE_ALLOW_HOST/")
      [ "$code" = "200" ] && emit env-ca-node 200 || emit env-ca-node "blocked:${code:-000}"
  else
      emit env-ca-node skip
  fi

  # python: trust the proxy CA ONLY via SSL_CERT_FILE (OpenSSL CA-file path, same
  # mechanism requests' REQUESTS_CA_BUNDLE uses). Stdlib urllib — no pip dep.
  if command -v python3 >/dev/null 2>&1; then
      code=$(SSL_CERT_FILE="$CREANCE_INJECTED_CA" REQUESTS_CA_BUNDLE="$CREANCE_INJECTED_CA" \
          python3 - "https://$CREANCE_ALLOW_HOST/" <<'PY' 2>/dev/null
  import os,sys,urllib.request
  try:
      r=urllib.request.urlopen(sys.argv[1],timeout=25); print(r.status)
  except Exception as e: print("ERR")
  PY
      )
      [ "$code" = "200" ] && emit env-ca-python 200 || emit env-ca-python "blocked:${code:-000}"
  else
      emit env-ca-python skip
  fi
  ```
  In the `else` (offline) branch, add `emit env-ca-node skip` / `emit env-ca-python skip`.
  - node honors `HTTPS_PROXY` automatically only from v18+? Node's `https.get` does
    NOT auto-read proxy env; if needed, pass an explicit agent using
    `process.env.HTTPS_PROXY`. **Validate in 1c**; if node ignores the proxy env,
    set the proxy explicitly in the snippet (e.g. via `HttpsProxyAgent`-free manual
    `CONNECT` is overkill — simpler: confirm node respects `--use-env-proxy` / global
    fetch which does honor proxy env in recent node). Keep the snippet minimal and
    adjust to whatever the in-cage node version honors.
  - The heredoc must survive being inside the larger script — keep POSIX-sh safe.

> NOTE for the implementer: the exact node/python invocation is subordinate to the
> behavior — "trust the CA *only* via the injected env file, reach the allowlisted
> host through the proxy, print the HTTP status." Adjust the snippets to the
> in-cage runtimes; the contract is the emitted `200` / `blocked:*` token.

### 1c. Prove the reproduction (RED)

Run the live battery (fix NOT yet applied — `ca.sb` does not exist):

```
make test-integration
```

Expected: `TestCageVerificationBattery` FAILS with `env-ca-node` and
`env-ca-python` reported as failing vectors (TLS CA error → not 200), because the
injected CA path is unreadable in-cage. This is the required caged reproduction.
Capture the failing summary in the commit message. Confirm node/python actually
*execute* in-cage (a `blocked:*` from a CA error, not a missing-binary skip); if
either cannot exec in-cage at all, STOP and surface to the user (the Node+Python
guard choice would need revisiting).

Also confirm `make test` (fast) stays green — the new vectors flow through the
evaluator unit tests without edits.

### Phase 1 success criteria

#### Automated
- [ ] `make test` green (new vectors well-formed; evaluator unit tests pass).
- [ ] `make test-integration` shows `env-ca-node` + `env-ca-python` FAILING in-cage with a CA/TLS error (the reproduction), all other 16 vectors still PASS, negative control still detects the escape.

#### Manual
- [ ] The two failing probes fail due to CA-unreadability (not a missing runtime); node and python both actually executed in-cage.

---

## Phase 2 — Fix: single-file SBPL read-grant (GREEN)

### 2a. Render the CA fragment — `internal/profile/`

Add `RenderCAReadFragment(caCertPath string) (string, error)` (new file
`profile_ca.go` or appended to `profile.go`; update the package doc to note it now
also emits a CA file-read fragment). It must:
- Return an error for an empty path.
- Emit a header comment + a `file-read*` allow for the resolved literal path, and a
  `file-read-metadata` allow for the parent dir (traversal), e.g.:
  ```
  ;; agent-creance ca.sb — allow in-cage read of the single mitmproxy CA PEM (AC-0034).
  ;; Appended after safehouse's base; last-match-wins reopens exactly this one file.
  (allow file-read-metadata (literal "<dir>"))
  (allow file-read* (literal "<caCertPath>"))
  ```
- The caller passes an already-symlink-resolved absolute path (see 2b). Quote paths
  with `%q`.

Add a golden test `internal/profile/ca_test.go` (or extend the existing profile
test) writing to `internal/profile/testdata/ca.golden` with a `-update` flag,
following the existing network-golden pattern. Use a fixed fake path in the test so
the golden is host-independent.

### 2b. Wire the fragment — `internal/state/state.go` + `internal/cage/cage.go`

- `state.go`: add `caProfileSBName = "ca.sb"` to the const block (`:39-40`) and an
  accessor `func (l Layout) CAProfileSB() string { return filepath.Join(l.Root, caProfileSBName) }` near `ProxyProfileSB` (`:220`).
- `cage.go` `Prepare` (`:163-186`): after writing the proxy fragment, resolve the CA
  path for firmlinks and write `ca.sb`:
  ```go
  caResolved := in.CACertPath
  if r, err := filepath.EvalSymlinks(in.CACertPath); err == nil {
      caResolved = r
  } // else: file absent (setup not run); fall back to the raw path
  caFrag, err := profile.RenderCAReadFragment(caResolved)
  if err != nil {
      return fmt.Errorf("cage: render CA fragment: %w", err)
  }
  if err := b.fs.WriteFile(in.Layout.CAProfileSB(), []byte(caFrag), 0o600); err != nil {
      return fmt.Errorf("cage: write CA fragment %q: %w", in.Layout.CAProfileSB(), err)
  }
  ```
  (`EvalSymlinks` is a real-FS call; it is acceptable here in `Prepare` alongside
  the other `b.fs` writes since `Prepare` is the side-effecting method. If the
  `sysdep.FileSystem` seam lacks `EvalSymlinks`, add it to the interface + fake per
  the project's "no direct OS in logic" rule, OR resolve in `Resolve` via the
  `PathResolver` seam — prefer extending the seam so unit tests stay hermetic.)
- `cage.go` `Build` (`:99-103`): append the third fragment after the proxy one:
  ```go
  args = append(args, "--append-profile", in.Layout.CAProfileSB())
  ```
- Update the `buildEnv` doc comment (`:188-195`) to note the CA path is now granted
  in-cage read via the `ca.sb` fragment (AC-0034); the env vars are unchanged.

### 2c. Prove the fix (GREEN)

```
make test-integration
```

Expected: `env-ca-node` + `env-ca-python` now PASS (200) from inside the cage; all
18 vectors PASS; the negative control still reports an escape. Run `-count=2` for
stability (the acceptance-gate convention from `docs/cage-verification.md`).

Manually confirm the private key stays unreadable in-cage (defence-in-depth check):
e.g. a throwaway probe `cat ~/.mitmproxy/mitmproxy-ca.pem` must still be denied
while `mitmproxy-ca-cert.pem` is readable. (Can be a temporary manual check; not a
committed vector unless trivial to add as a BLOCKED vector — optional.)

### 2d. Update the invocation golden

`make golden` regenerates `internal/cage/testdata/invocation.golden.json` with the
third `--append-profile …/ca.sb`. Review the diff: exactly one new append-profile
arg + the `ca.sb` path; nothing else changed.

### Phase 2 success criteria

#### Automated
- [ ] `make test` green (new `RenderCAReadFragment` golden + existing unit tests).
- [ ] `make build` (typecheck) green.
- [ ] `make lint` green.
- [ ] `make test-integration` green: all 18 vectors PASS, negative control detects the escape, stable across `-count=2`.

#### Manual
- [ ] `env-ca-node`/`env-ca-python` succeed via the injected env-var CA (no `--cacert`).
- [ ] The CA private key (`mitmproxy-ca.pem`) remains unreadable in-cage.
- [ ] `curl`/keychain probes (the existing proxy + allow-200 + passthrough vectors) still PASS unchanged.
- [ ] `invocation.golden.json` diff is limited to the new `ca.sb` append-profile.

---

## Phase 3 — Docs & cleanup

### 3a. `docs/cage-verification.md`
- Bump the vector count at `:40` from 16 to 18.
- Rewrite Known-limitation #1 (`:116-123`): the env-var CA files now work in-cage —
  agent-creance grants in-cage read of the single CA PEM via an `--append-profile`
  fragment; both the keychain track and the env-var-file track are functional. Note
  the new `env-ca-node`/`env-ca-python` guards. (If the list would become empty,
  keep the section with the remaining items; AC-0035's CLAUDE_CONFIG_DIR item stays.)

### 3b. `docs/design.md` (accuracy, not test-gated)
- Add a one-line note where the CA/filesystem isolation is described: the cage
  grants in-cage `file-read*` to the single mitmproxy CA PEM (the one documented
  exception alongside the keychain ACL), so env-var-CA clients (node/python) trust
  the proxy. Keep it minimal; ALLOWED vectors don't require a threat-model bullet,
  so no drift-guard keyword change is needed.

### 3c. Ticket file
- Tick the Acceptance Criteria in `thoughts/shared/tickets/AC-0034-incage-ca-trust-env-files.md`, set Status to Done, add a dated Notes entry summarizing the fix + the red→green evidence.

### Phase 3 success criteria

#### Automated
- [ ] `make test` green (the `designMatrixSection` drift guards still pass — no BLOCKED/DOCUMENTED keyword removed).

#### Manual
- [ ] Known-limitation #1 reflects reality; vector count says 18.
- [ ] design.md note is accurate and minimal.

---

## Testing strategy

- **Fast (`make test`)**: vector well-formedness + evaluator (auto-covers new vectors); new `RenderCAReadFragment` golden; cage `invocation.golden.json`. Hermetic — no cage, no node/python.
- **Integration (`make test-integration`)**: the live battery is the red→green proof and the regression guard. Repro-first ordering means Phase 1 must show red before Phase 2 shows green.
- **Stability**: `-count=2` on the integration battery (acceptance-gate convention).
- **Lint/typecheck**: `make lint`, `make build`.

## Risks & mitigations

- **Seatbelt firmlink path mismatch** → resolve via `EvalSymlinks` before emitting the literal; validated by the Phase 2 repro. Fallback: parent-dir metadata grant; escalate if a file-literal still won't open.
- **append-profile file rules unproven (S5 only validated network rules)** → last-match-wins should let an `allow file-read*` override `(deny default)`; the Phase 2 integration green is the proof. If it doesn't take effect, STOP and reconsider (Option B fallback).
- **node/python not runnable in-cage** → per-probe skip avoids a false battery failure on hosts without them; Phase 1c confirms they actually exec in-cage (node must, by design). If blocked, surface to the user.
- **node proxy-env handling** → validate the node snippet honors `HTTPS_PROXY` in-cage; adjust the invocation if not.
- **Golden churn** → only `invocation.golden.json` (one new arg) + the new `ca.golden`; review both diffs.
