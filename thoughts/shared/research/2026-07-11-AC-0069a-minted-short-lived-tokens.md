---
date: 2026-07-11T09:24:12Z
git_commit: c681aea6a66456ae7d4e88369116b0482454f230
branch: main
repository: agent-creance
topic: "AC-0069a: Minted short-lived tokens — GitHub App installation + OAuth2 refresh"
tags: [research, codebase, credential-injection, minting, github-app, oauth2, proxy, enforcer, refresh-loop]
status: complete
last_updated: 2026-07-11
---

# Research: AC-0069a — Minted short-lived tokens (GitHub App installation + OAuth2 refresh)

**Date**: 2026-07-11T09:24:12Z
**Git Commit**: c681aea6a66456ae7d4e88369116b0482454f230
**Branch**: main
**Repository**: agent-creance

## Research Question

AC-0069a (Phase 2 of credential injection, sub-ticket of epic AC-0069): mint
short-lived tokens **host-side** — GitHub App installation tokens (JWT-sign with
the app private key → ≤1h repo-scoped installation token → refresh loop) and at
least one OAuth2 refresh-grant flow (e.g. Google Drive) — injected via the
Phase-1 substrate (resolve → inject → overwrite → fail-closed), refreshed
without interrupting in-flight cage requests. The app private key / refresh
token never enters the cage. Depends on AC-0068 (Phase 1), which is Done.

## Summary

Phase 1 (AC-0068, complete as of c681aea) provides everything up to and
including one-shot injection: a reference-only `credentials:` config model, the
`sysdep.SecretResolver` seam (`op://`, `keychain://`, `env://`), spawn-time
resolution into a flat `{name: token}` JSON payload delivered over inherited
fd 3, Python-side header rendering with overwrite, per-request 472 fail-closed,
and `X-Cage-Injected` annotation of upstream 401/403.

The load-bearing facts for AC-0069a:

1. **Token *production* has a clean extension point; token *refresh* has no
   channel.** Secrets are resolved exactly once, on the proxy-spawn path only
   (`internal/proxy/lifecycle.go:176-191`), and the fd-3 channel is one-shot by
   design: the Go side closes the write end after a single write
   (`internal/sysdep/processmanager.go:110-113`) and the addon reads it once at
   startup, guarded by `_secrets_read` (`internal/proxy/enforcer/enforcer.py:138-143`).
   Policy rules and credential *shapes* hot-reload via a 1s mtime poll on
   `policy.json`; the secret *values* are fixed for the addon's lifetime. The
   Phase-1 plans record the AC-0069b unix-socket broker — not an fd extension —
   as the intended rotation-capable channel.

2. **The minting flows themselves are small, well-documented surfaces.**
   GitHub: RS256 JWT (`iat` backdated 60s, `exp` ≤10min, `iss` = client ID) →
   `GET /repos/{owner}/{repo}/installation` → `POST
   /app/installations/{id}/access_tokens` with `repositories` + `permissions`
   down-scoping → token that lives exactly 1h (non-configurable), revocable via
   `DELETE /installation/token`. OAuth2: one form-encoded POST
   (`grant_type=refresh_token`) per RFC 6749 §6; `golang.org/x/oauth2` wraps it
   in a thread-safe, lazily-refreshing `TokenSource`.

3. **Several recorded Phase-1 constraints bind the design**: no interactive
   prompts on any non-spawn path (the spawn-only resolution exists specifically
   so `op read` cannot re-fire Touch ID mid-session); per-request 472, never
   fail-to-spawn; secrets never on disk/argv/env/logs; per-proxy isolation (N
   projects = N proxies = N independent payloads); no personal secret
   references or App IDs in the committed public config; real GitHub App /
   OAuth flows are out-of-cage work when dogfooding.

4. **Missing infrastructure the ticket would be first to need**: no JWT/RSA
   code exists anywhere in the Go source, the `sysdep.HTTPGetter` seam is
   GET-only (minting needs POST), and neither `golang.org/x/oauth2` nor any JWT
   library is in `go.mod`. A `Clock` seam (Now/Since), a cancellable `Sleeper`
   seam, and a fully worked background-goroutine pattern (`internal/configwatch`)
   do exist.

## Detailed Findings

### 1. Phase-1 substrate, Go side — where minting would plug in

**Config model** (`internal/config/`):

- `config.Credential` — `Source`, `Header`, `Template`, `Username`
  (`internal/config/config.go:137-142`); `Config.Credentials map[string]Credential`
  (`internal/config/config.go:35`). Header defaults to `Authorization`
  (`internal/config/config.go:123`, applied at `config.go:262-269`).
- Rules carry the auth axis: `Inject string` / `InCage bool`
  (`internal/config/config.go:109-110`), mutually exclusive, `inject` forbidden
  on `mode: passthrough` (`internal/config/validate.go:61-68`).
- Value-template DSL: `{token}` / `{user}` + single `base64(…)` wrapper
  (`internal/config/template.go:24-28`); Go reference renderer
  `RenderCredentialValue` (`template.go:34-53`); the Python enforcer ports the
  same semantics (`internal/proxy/enforcer/inject.py:31-64`).
- Compiled artifact is reference-only: `policy.Credential`
  (`internal/policy/policy.go:112-117`) copies `source`/`header`/`template`/
  `username` — never values — into `policy.json`; a dangling `inject` fails the
  compile closed (`internal/policy/compile/compile.go:344-346`, `399-417`).

**Secret resolution** (`internal/sysdep/secretresolver.go`):

- `SecretResolver` is a single-method seam: `Resolve(ctx, ref) ([]byte, error)`
  (`secretresolver.go:30-38`), with sentinel errors `ErrUnknownSecretScheme`,
  `ErrSecretNotFound`, `ErrSecretToolMissing` (`secretresolver.go:45-56`) and
  `ErrKeychainLocked` reuse (`secretresolver.go:41-44`).
- Backends: `op://` shells out to `op read --no-newline`
  (`secretresolver.go:130-141`), `keychain://service[/account]` uses the
  Keychain seam (`secretresolver.go:146-161`), `env://NAME`
  (`secretresolver.go:166-177`). Fake:
  `internal/sysdep/sysdeptest/secretresolver.go:16-52`.
- **Resolution happens only at proxy spawn, never per request and never on
  attach-to-running-proxy.** `runRun` builds a lazy `Secrets` closure
  (`internal/cli/run.go:241-246`) over `resolveInjectionSecrets`
  (`internal/cli/inject.go:26-54`); `proxy.Manager.Attach` invokes it only on
  the spawn branch (`internal/proxy/lifecycle.go:176-185`), documented as the
  guard against `op read` re-firing Touch ID per attach
  (`lifecycle.go:108-116`).

**Delivery** (`internal/sysdep/processmanager.go`, `internal/proxy/lifecycle.go`):

- Payload: flat JSON `{credential-name: raw-token}` built by
  `resolveInjectionSecrets` (`internal/cli/inject.go:34-53`); credential names
  come from the compiled policy's rule `inject` fields
  (`injectedCredentialNames`, `inject.go:58-70`).
- `SpawnWithSecret`: `os.Pipe()`, read end as `cmd.ExtraFiles[0]` → child fd 3
  (`SecretFD = 3`, `processmanager.go:59-63`); a goroutine writes the payload
  and **closes the write end → EOF** (`processmanager.go:110-113`). Never argv,
  env, or disk (`processmanager.go:34-40`).
- The fd number rides `--set creance_secret_fd=3`, appended only when a payload
  exists (`internal/proxy/lifecycle.go:495-507`).

**Fail-closed semantics (Go side)**:

- Per-credential resolve failure at spawn is best-effort: warn and omit from
  the payload; the proxy still starts (`internal/cli/inject.go:20-25`, `44-47`).
- Whole-resolver failure: warn and spawn the proxy plain
  (`internal/proxy/lifecycle.go:178-185`). 472 is produced per-request in the
  addon, never at spawn.

**Proxy lifecycle & reload**:

- `proxy.Manager` refcounts via a flocked `proxy.lock`
  (`internal/proxy/lifecycle.go:47-76`, `512-538`); reuse vs spawn decided per
  `Attach` (`lifecycle.go:137-221`); last-out `Detach` SIGTERMs the proxy
  (`lifecycle.go:247-277`).
- Policy hot-reload today: every mutating CLI command recompiles `policy.json`
  under a config flock (`applyAndRecompile`, `internal/cli/mutate.go:103-139`,
  `227-234`), and during a run session `internal/configwatch` (fsnotify-backed,
  100ms debounce) recompiles on hand-edits (`internal/cli/run.go:296-315`;
  `internal/configwatch/configwatch.go:99-121`, `137-188`). The addon picks the
  file up by mtime polling within ~1s.
- **Nothing on the Go side re-delivers secrets to a running addon.** The only
  recurring timer in the whole system is the addon's 1s mtime poll.

**CLI surface** (`internal/cli/credential.go`, `domain.go`, `allow.go`):

- `credential add NAME --source … [--bearer|--token|--raw|--basic]
  [--header] [--template] [--global]` (`credential.go:53-149`), `credential
  list` (merged view, never resolves values, `credential.go:151-191`),
  `credential remove` (refuses while a rule injects the name,
  `credential.go:200-246`).
- Binding: `allow URL --inject NAME` / `--in-cage` (`allow.go:16-62` over
  `domain.go:135-154`); in-place rule update via `config.SetRuleAuth`
  (`internal/config/setauth.go:28-69`).

**Phantom priming** is a config convention, not code: a fake token in the
config `env:` block rides into the cage via `cage.buildEnv`
(`internal/cage/cage.go:311-336`) and `--env-pass`; the addon's overwrite
clobbers it at egress. Documented example in `docs/design.md:176-182`.

### 2. Phase-1 substrate, Python addon side — what the enforcer can and cannot do

- **Secret receipt is strictly once-per-process.** `configure()` reads fd 3
  when `creance_secret_fd > 0` and `not self._secrets_read`
  (`internal/proxy/enforcer/enforcer.py:138-143`); `_read_secrets` loops
  `os.read` to EOF, parses the flat JSON object, closes the fd
  (`enforcer.py:215-244`). A later option update is ignored by the guard. Read
  failure logs only the exception type and leaves `_secrets = {}` → per-request
  472. No zeroization exists; tokens are ordinary `str` in a dict
  (`enforcer.py:99-103`).
- **Injection at request time**: only on intercepted flows (passthrough hosts
  are tunneled at `tls_clienthello`, `enforcer.py:276-292`, and never reach the
  request hook). `_apply_injection` (`enforcer.py:339-373`): matched allow rule
  → `in_cage` untouched → no `inject` untouched → missing credential shape *or*
  missing token → synthesized 472 → else render via
  `render_credential_value` and **overwrite** the header
  (`enforcer.py:360-371`), stashing `flow.metadata["creance_injected"]` for the
  response-side annotation.
- **472 shape**: `responses.injection_unavailable`
  (`internal/proxy/enforcer/responses.py:146-170`): status 472, `X-Cage-Reason:
  injection-unavailable`, `X-Cage-Injected: <name>`, JSON body
  `error: "agent_cage_injection_unavailable"` + human-recoverable
  `how_to_proceed` (unlock secret store; do NOT `allow`). Golden:
  `internal/proxy/enforcer/testdata/injection_unavailable_body.json.golden`.
- **Upstream 401/403 annotation**: `responseheaders` hook sets
  `X-Cage-Injected` before streaming starts (`enforcer.py:399-404`).
- **Reload**: `_poll_loop` (1s, `enforcer.py:63`, `208-211`) re-parses the
  *entire* `policy.json` including the `credentials` map
  (`internal/proxy/enforcer/policy.py:479-488`) — so rules, `inject` bindings,
  and credential shapes hot-reload; **`self._secrets` never does**. A
  credential newly bound mid-run 472s until the proxy respawns.
- **Audit log carries no injection-specific fields**; a 472 is distinguishable
  only by `status: 472` on an `allow` decision (`internal/proxy/enforcer/audit.py:72-90`).
- **Test coverage**: template parity (`test_inject.py`), hook behavior incl.
  overwrite/472/annotation/fd intake (`test_inject_hook.py`), 472 golden
  (`test_responses.py`), live e2e incl. two concurrent proxies with distinct
  secrets (`test_integration.py:512-655`). CLI `.txtar` tests
  (`internal/cli/testdata/script/credential.txtar`, `allow_inject.txtar`) are
  offline and never spawn `mitmdump`.

### 3. The refresh gap — what exists between "new token minted" and "addon injects it"

Today there are exactly two update channels to a live proxy, and neither
carries secret values:

1. **`policy.json` mtime reload** (~1s): rules and credential shapes only. The
   design deliberately keeps secret values out of this file (reference-only
   artifact; secrets never on disk — a recorded Phase-1 decision).
2. **fd 3 at spawn**: one-shot, EOF-terminated.

The recorded consequence (AC-0068e plan, Key Discoveries): editing injection
config while a cage runs hot-reloads the rules but not the secret map — "a cage
restart is required". The Phase-1 plans name the AC-0069b unix-socket broker as
the rotation-capable channel ("No unix-socket broker … — Phase 2 (AC-0069b)";
"No minted/rotating tokens — Phase 2 (AC-0069a)",
`thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md`, "What we
are NOT doing"). The epic AC-0069 sequencing notes prefer b before (or
alongside) a for exactly this reason.

A proxy respawn *does* re-run resolution and re-deliver secrets
(`internal/proxy/lifecycle.go:176-191`), but a respawn is the opposite of the
ticket's "refresh without interrupting in-flight cage requests" criterion.

### 4. Existing seams and patterns a refresh loop / minter would build on

- **Clock**: `sysdep.Clock` — `Now()` / `Since()` only, no ticker
  (`internal/sysdep/clock.go:12-31`); fake with `Advance(d)`
  (`internal/sysdep/sysdeptest/clock.go:13-28`). Consumer example: registry
  cache freshness (`internal/generator/registry/registry.go:203`, `242`).
- **Sleeper**: `sysdep.Sleeper.Sleep(ctx, d)` — context-cancellable
  (`internal/sysdep/sleeper.go:18-38`); fake records durations and returns
  instantly (`sysdeptest/sleeper.go:14-26`). Consumer: bounded readiness poll
  `waitProxyReady` (`internal/proxy/lifecycle.go:229-242`), tested by asserting
  `len(h.sleep.Sleeps) == 100` without wall-clock time
  (`internal/proxy/lifecycle_test.go:378-394`).
- **Background-goroutine pattern**: `internal/configwatch` is the one
  long-running host-side loop tied to a run session — `stop`/`done` channels +
  `sync.Once` (`configwatch.go:55-72`), `Start` spawns `go w.loop(ctx)`
  (`configwatch.go:99-121`), `Stop` closes and joins (`configwatch.go:126-133`),
  select-loop with a re-armed debounce timer (`configwatch.go:137-188`). Wired
  in run with advisory-on-failure and deferred stop
  (`internal/cli/run.go:296-315`). Tests use a fake event source +
  `require.Eventually` (`internal/configwatch/configwatch_test.go:88-131`).
- **Outbound HTTP**: `sysdep.HTTPGetter` — **GET-only**
  (`internal/sysdep/http.go:30-79`; 16 MiB cap, 30s default timeout, status
  separated from transport error). Only consumer: registry client
  (`internal/generator/registry/registry.go:210-233`). No POST or
  generic-request seam exists. Fake: `sysdeptest/http.go:20-63`.
- **Crypto**: no JWT, RSA, or signing code exists in the Go source; production
  `crypto/*` use is SHA-256 hashing only (`internal/state/state.go:20`,
  `internal/policy/compile/compile.go:21`, `internal/generator/cache.go:4`).
  mitmproxy generates its own CA; Go never touches key material
  (`internal/setup/setup.go:33-45`).
- **Dependencies** (`go.mod:5-12`): fsnotify, go-internal, cobra, testify,
  golang.org/x/sys, yaml.v3. **Not present**: `golang.org/x/oauth2`, any JWT
  library, any GitHub API client.
- **State files**: `internal/state` names artifacts as constants + path-only
  accessors (`internal/state/state.go:31-58`, `283-298`); package does no I/O.
  Two write disciplines: flock-guarded in-place read-modify-write for
  coordination state (`proxy.lock`, `internal/proxy/lifecycle.go:509-538`) vs
  atomic temp+rename for artifacts (`internal/generator/registry/registry.go:238-255`).
- **Composition root**: every seam is an `App` field wired in `cli.Main()`
  (`internal/cli/cli.go:21-84`, `207-245`), following the trio
  `sysdep/<name>.go` (interface + `OS*` impl + compile-time assertion),
  `sysdeptest/<name>.go` (scripted fake), consumer takes the interface.

### 5. GitHub App installation tokens (external research)

**Minting flow** (all docs.github.com, links in External Sources):

1. Sign an RS256 JWT with the app private key. Claims: `iat` backdated 60s
   (GitHub's own clock-drift recommendation), `exp` ≤10min ahead, `iss` = the
   app's client ID (recommended over the numeric app ID since May 2024; client
   IDs are strings, app IDs ints; neither is secret).
2. Discover the installation: `GET /repos/{owner}/{repo}/installation` with the
   JWT as Bearer → installation object incl. `id` (stable, cacheable per
   owner).
3. `POST /app/installations/{installation_id}/access_tokens` (JWT Bearer) with
   body `repositories` (names) / `repository_ids` and `permissions`
   (e.g. `contents`, `issues`, `pull_requests`; `metadata:read` underpins most
   reads). Down-scoping is a **cap, not a grant** — requested permissions must
   be a subset of what the registration and the installation already hold.
   Response 201: `token`, `expires_at`, `permissions`, `repositories`.
4. Lifetime is **exactly 1 hour, non-configurable**. Early revocation:
   `DELETE /installation/token`, authenticated with the token itself (204).

**Registration/onboarding**: a GitHub App can be registered under a personal
account; webhooks can be deselected entirely (right shape for a pure
token-minting app); install on selected repositories. The private key is
generated in the app settings and downloads as **PKCS#1 PEM**
(`-----BEGIN RSA PRIVATE KEY-----`). Up to 25 keys per app; rotation is manual
(generate new before deleting old); no automatic expiry. GitHub's storage
guidance explicitly warns against env vars and hard-coding.

**Operational realities**:

- Clock skew is the classic failure ("'Expiration time' claim ('exp') is too
  far in the future"); mitigation is the documented `iat`-60s backdate plus
  `exp` ≈9min.
- Installation tokens are accepted on `/graphql` — which per this repo's own
  gotcha does **not** change the public-read-scoping posture: token lifetime
  bounds the leak window, not public-read scope; scoped REST stays the default,
  `/graphql` stays a documented opt-in risk.
- Git-over-HTTPS works as Basic auth with username literal `x-access-token`
  and the token as password (requires the Contents permission) — the same
  Basic shape the Phase-1 template DSL already models.
- **Token format change (April 2026)**: GitHub is rolling out a stateless
  `ghs_APPID_JWT` format, ~520 chars and variable length. Integrators must
  treat tokens as opaque strings. (The Phase-1 addon already does — tokens are
  opaque dict values.)
- Rate limits: installation tokens get ≥5,000 req/h; minting once per hour per
  cage session is negligible against the app's own limit.

**Refresh practice**: `ghinstallation/v2` re-mints lazily when a request
arrives within **60s of `expires_at`** (`Token(ctx)` checks and renews);
GitHub Actions' own model is one repo-scoped installation token per job,
runner-side refresh on long jobs, dead at job end, and
`actions/create-github-app-token` revokes the token in its `post` step —
i.e. mint scoped → use → revoke on teardown.

**Go libraries**:

- `github.com/golang-jwt/jwt/v5` (v5.3.1, MIT, ~15k importers):
  `SigningMethodRS256` + `RegisteredClaims` covers the GitHub JWT exactly;
  parses PKCS#1 PEM via `jwt.ParseRSAPrivateKeyFromPEM`.
- `github.com/bradleyfalzon/ghinstallation/v2` (v2.19.0, Apache-2.0,
  maintained): `http.RoundTripper`-based, auto-refresh 60s before expiry,
  down-scoping via go-github's `InstallationTokenOptions` — but depends on
  `google/go-github`, a heavy tree for one REST call.
- Minimal hand-rolled surface: PKCS#1 PEM parse → RS256 JWT → two HTTPS calls
  (`GET …/installation`, `POST …/access_tokens`) → parse `token`/`expires_at`;
  optional `DELETE /installation/token` on teardown. Everything except the JWT
  signing is stdlib.

### 6. OAuth2 refresh-grant minting (external research)

**The grant** (RFC 6749 §6): form-encoded POST to the token endpoint with
`grant_type=refresh_token&refresh_token=…` (+ `client_id` for public/native
clients); response carries `access_token`, `expires_in` (recommended), and
**optionally a new `refresh_token`** — the server MAY rotate, and the client
MUST then discard the old one. A correct implementation always persists a
returned `refresh_token`, even against providers that don't rotate today.
`error=invalid_grant` (HTTP 400) means invalid/expired/revoked — terminal for
that refresh token; re-auth is the only recovery. RFC 9700 (OAuth Security
BCP): refresh tokens for public clients MUST be sender-constrained or rotated.

**`golang.org/x/oauth2`** (v0.36.0, Feb 2026, actively maintained):

- `TokenSource` is `Token() (*Token, error)`, safe for concurrent use.
  **Refresh is lazy** — `ReuseTokenSource` returns the cached token while
  valid and refreshes inside `Token()` once invalid; there is no background
  loop in the package. A proactive host-side refresher drives its own schedule
  and calls `Token()`.
- Default early-expiry margin is 10s (`defaultExpiryDelta`);
  `ReuseTokenSourceWithExpiry(t, src, earlyExpiry)` makes the margin
  configurable (1–5min is the idiomatic knob for never-expire-mid-request).
- Bootstrap from refresh token only:
  `Config.TokenSource(ctx, &oauth2.Token{RefreshToken: rt})` — first `Token()`
  call performs the refresh grant. The caller must check `tok.RefreshToken`
  after each call and persist changes.
- Expiry is computed from the `expires_in` delta relative to local receipt
  time — the standard clock-skew defense.

**Google specifics** (candidate first target per the ticket):

- Token endpoint `https://oauth2.googleapis.com/token`; access tokens live
  ~1h. Installed/native app clients **always** get refresh tokens (no
  `access_type=offline` needed). Google does not rotate refresh tokens on use
  (implied by docs; code for rotation anyway).
- **Operational traps**: consent screen in "Testing" publishing status →
  refresh token expires in **7 days**; **100 refresh tokens per account per
  client** (LRU eviction — store one refresh token per host, not per
  cage/session); 6 months of disuse expires it.
- **Drive scopes**: `drive.file` is non-sensitive and frictionless;
  `drive.readonly` and `drive` are **restricted** scopes triggering Google's
  app-verification (possible CASA assessment) for published apps.
- **Initial consent for a CLI**: OOB is dead (blocked since Jan 2023). The
  current native-app path is RFC 8252 loopback redirect —
  system browser + listener on `http://127.0.0.1:<random port>`, PKCE S256
  recommended; "Desktop app" client type (its client secret is not
  confidential). Device flow (RFC 8628) exists but among Drive scopes allows
  only `drive.file`/`drive.appdata` — not `drive.readonly`.
- **Refresh-token-free alternative**: Google service accounts (RFC 7523 JWT
  bearer — sign a JWT, exchange for a ~1h access token, no refresh token at
  all), but personal-account Drive data is reachable only via domain-wide
  delegation (Workspace), so it does not fit a personal first target.

## Code References

- `internal/cli/inject.go:26-70` — `resolveInjectionSecrets` +
  `injectedCredentialNames`: builds the `{name: token}` payload from compiled
  policy + `SecretResolver`; warn-and-omit per credential.
- `internal/cli/run.go:227-246` — reads compiled policy, wires the lazy
  `Secrets` closure into proxy attach.
- `internal/proxy/lifecycle.go:108-116`, `176-191` — spawn-only resolution
  (Touch-ID guard), `SpawnWithSecret` vs plain `Spawn` decision.
- `internal/sysdep/processmanager.go:34-63`, `87-117` — `SecretFD = 3`,
  one-shot pipe write + close.
- `internal/proxy/lifecycle.go:495-507` — `mitmArgs`, `--set creance_secret_fd=3`.
- `internal/sysdep/secretresolver.go:30-56`, `104-177` — resolver seam,
  sentinel errors, three backends.
- `internal/config/config.go:35`, `109-110`, `123`, `137-142` — credentials
  map, rule auth axis, `Credential` struct.
- `internal/config/template.go:24-89` — value-template DSL + validation.
- `internal/policy/policy.go:112-149` — reference-only compiled credentials.
- `internal/policy/compile/compile.go:336-346`, `386-417` — credential merge +
  dangling-inject fail-closed.
- `internal/proxy/enforcer/enforcer.py:99-103`, `138-143`, `215-244` — secret
  dict, once-per-process read guard, fd read.
- `internal/proxy/enforcer/enforcer.py:294-373` — request hook +
  `_apply_injection` (overwrite, 472).
- `internal/proxy/enforcer/enforcer.py:63`, `158-161`, `197-211` — 1s mtime
  poll / hot reload (rules + shapes only).
- `internal/proxy/enforcer/responses.py:146-170` — 472 response shape.
- `internal/proxy/enforcer/inject.py:31-100` — Python template renderer.
- `internal/cli/credential.go:26-261` — `credential add/list/remove`.
- `internal/cli/domain.go:135-154`, `internal/config/setauth.go:28-69` —
  `--inject`/`--in-cage` binding.
- `internal/cli/mutate.go:103-139`, `208-234` — `applyAndRecompile`, config
  flock, recompile→mtime-reload path.
- `internal/configwatch/configwatch.go:55-188` — the background-goroutine
  pattern (start/stop/loop/debounce).
- `internal/sysdep/clock.go:12-31`, `internal/sysdep/sleeper.go:18-38` —
  Clock and Sleeper seams; fakes in `internal/sysdep/sysdeptest/`.
- `internal/sysdep/http.go:30-79` — `HTTPGetter` (GET-only).
- `internal/state/state.go:31-58`, `283-298` — state-file naming pattern.
- `go.mod:5-12` — current direct dependencies.
- `internal/proxy/inject_github_integration_test.go` — live GitHub injection
  integration test (env-gated `AC_TEST_GITHUB_TOKEN_REF`).

## Architecture Documentation

- **Reference-only artifacts, values only in memory.** Config and
  `policy.json` carry secret *references* and shapes; resolved values exist
  only in proxy-process memory (Go transiently, Python for the lifetime).
  Minted-credential config (App/client IDs, key references, token endpoints,
  scopes) would follow the same reference-only model; `validateInjectRefs`
  fails closed on dangling names.
- **Refusal taxonomy is pinned**: 470 agent-recoverable / 471 permanent / 472
  human-recoverable ("unlock the secret store; do NOT `allow`"), asserted
  across SKILL.md, `internal/cage/briefing.md`, and `docs/design.md` marker
  tests; the AC-0068c research enumerates every surface a new/changed status
  code touches (AC-0047 pinned-literal checklist). Auto-refresh failure states
  must land inside this taxonomy or extend it via that checklist.
- **Testing conventions**: new side effects (JWT signing, POST to token
  endpoints, refresh-loop clock) belong behind `internal/sysdep` interfaces
  with `sysdeptest` fakes; real GitHub App / OAuth flows go behind
  `//go:build integration`, gated by env-var opt-in, assert-presence-only on
  secrets. External tools are never invoked in unit tests.
- **Per-proxy isolation is a proven invariant** (two concurrent proxies hold
  distinct secrets under the same credential name,
  `internal/proxy/enforcer/test_integration.py:614-655`). A minting/refresh
  design must not introduce shared cross-project token state.
- **Dogfooding split**: real GitHub App registration / OAuth token minting is
  out-of-cage work (project CLAUDE.md); hermetic (fakes) and live (batched
  breakout) phases must be planned separately.
- **Public-repo config discipline**: no personal `op://`/`keychain://`
  references — and by extension no personal App IDs / installation IDs — in
  the committed `.agent-creance.yaml` (the dogfood-secret gotcha; the
  AC-0068e plan's dogfood-config decision was reversed for exactly this
  reason).

## Historical Context (from thoughts/)

- `thoughts/shared/discussions/2026-06-28-credential-injection.md` — the
  founding discussion: prior art (nono, Envoy, Cloudflare, GitHub Actions),
  the two-axis model, the IMDS lesson (never an in-cage token endpoint), the
  phasing (minting = Phase 2 where "rotation and the hot-reload delivery
  channel pay off"), and the one recorded Open Decision (delivery-channel
  evolution → unix-socket broker, held in AC-0069b).
- `thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md` —
  delivery-channel decisions as shipped: fd 3 over stdin, one-shot EOF
  contract, "no minted/rotating tokens — Phase 2 (AC-0069a)", "no unix-socket
  broker … — Phase 2 (AC-0069b)"; "472 is per-request, not fail-to-spawn" is a
  locked decision.
- `thoughts/shared/research/2026-07-10-AC-0068e-github-flagship.md` — the
  consumer-assumption list for the substrate (secret map populated at spawn
  and never refreshed); the spawn-only-resolution dogfooding hazard; the
  most-specific-rule injection trap; phantom-token mechanics (`gh` does no
  format check; `GH_TOKEN` outranks keyring).
- `thoughts/shared/plans/2026-07-10-AC-0068e-github-flagship.md` — fine-grained
  PAT scoping for the flagship (Metadata:R + Issues:RW + Contents:R because
  `gh issue create` selects `defaultBranchRef`, cli/cli#12798); "editing the
  config while a cage runs hot-reloads the rules but not the secret map".
- `thoughts/shared/reviews/2026-07-11-AC-0068-review.md` — the read-scoping
  finding (minting does **not** fix public reads through `/graphql`; lifetime
  bounds the leak window, not scope); dogfood-secret gotcha; secret-hygiene
  and concurrent-proxy invariants to preserve; Phase-2 items confirmed out of
  Phase-1 scope.
- `thoughts/shared/tickets/AC-0069-credential-injection-phase2-epic.md` —
  epic-level sequencing: a and b independent in principle, but "prefer b
  before (or alongside) a so minting writes into a channel built for it".
- `thoughts/shared/tickets/AC-0069b-secret-broker.md` — the sibling ticket
  holding the delivery-channel Open Decision; its Desired Outcome explicitly
  includes "AC-0069a's refresh loop can update the served credential without
  restarting the proxy and without racing in-flight requests".

## Related Research

- `thoughts/shared/research/2026-06-29-AC-0068a-secretresolver-seam.md`
- `thoughts/shared/research/2026-07-02-AC-0068b-config-injection-model.md`
- `thoughts/shared/research/2026-07-02-AC-0068c-proxy-injection-engine.md`
- `thoughts/shared/research/2026-07-05-AC-0068d-cli-credential-management.md`
- `thoughts/shared/research/2026-07-10-AC-0068e-github-flagship.md`

## Impact Analysis

AC-0069a extends the Phase-1 substrate: minting is a new way to *produce* a
token before delivery, and refresh needs a way to *re-deliver* one after
spawn. The pieces it touches, their contracts, and what binds them:

### Existing Usages Found

- `internal/cli/run.go:241-246` — sole producer of the `Secrets` closure;
  assumes resolution is cheap enough to run once at spawn and may prompt
  (Touch ID) at most once per spawn.
- `internal/proxy/lifecycle.go:176-191` — sole consumer of `cfg.Secrets`;
  calls it only on the spawn branch; treats resolver failure as
  warn-and-spawn-plain.
- `internal/sysdep/processmanager.go:87-117` + fake
  (`sysdeptest/processmanager_test.go:9-53`) — `SpawnWithSecret` contract:
  exactly one payload, write end closed immediately (EOF).
- `internal/proxy/enforcer/enforcer.py:138-143`, `215-244` — addon reads fd 3
  once to EOF; `_secrets_read` guard makes any second delivery over the same
  channel unreachable; `test_inject_hook.py:155-181` pins the intake behavior;
  `test_integration.py:512-557` delivers over a real fd with `pass_fds`.
- `internal/cli/inject.go:26-54` + `inject_test.go` — payload building;
  warn-and-omit semantics pinned by tests.
- `internal/config/config.go:137-142` / `internal/policy/policy.go:112-117` —
  `Credential` is `{source, header, template, username}`; strict-decode config
  parsing rejects unknown fields; consumers: compiler
  (`compile.go:336-346`), CLI `credential` commands, addon shape lookup
  (`policy.py:92-116`).
- `internal/cli/credential.go:117-149` — `credential add` prompts for/accepts
  a `--source` secret *reference* and syntax-checks it
  (`ValidateSecretRefSyntax`) — today's onboarding assumes a static reference.

### Current Contract

- **Input**: compiled policy with reference-only `credentials` +
  rule `inject` names; a `SecretResolver` that maps `scheme://ref` → bytes.
- **Output**: flat JSON `{name: raw-token}` written once to fd 3; addon
  renders headers from token + shape at request time.
- **Assumptions consumers make**: (a) secret values are fixed for the proxy's
  lifetime; (b) resolution happens only at spawn (interactive prompts allowed
  there, nowhere else); (c) values never touch disk/argv/env/logs; (d) a
  missing token is a per-request 472, never a spawn failure; (e) tokens are
  opaque strings (no format/length assumptions — relevant to GitHub's 2026
  ~520-char format change); (f) each proxy holds only its own project's
  payload.

### Adaptation Requirements

- `internal/config/config.go:137-142` (+ validation, merge, compile, addon
  `policy.py:92-116`) — a minted credential needs fields a static reference
  does not carry (kind/type discriminator, app client ID, private-key
  reference, target repo/permissions, or token-endpoint/client-ID/scopes for
  OAuth2). The strict decoder means any schema growth is an explicit,
  versioned change across Go parse → compile → Python parse (the Go/Python
  template parity tests are the model: `internal/config/template_test.go` ↔
  `internal/proxy/enforcer/test_inject.py`).
- `internal/cli/inject.go:26-54` — `resolveInjectionSecrets` maps
  `source → Resolve`; a minted credential replaces "resolve stored value"
  with "resolve key material, then exchange it" (JWT + HTTP POST). New
  side-effecting deps (signer, POST-capable HTTP, clock) need sysdep seams —
  `HTTPGetter` is GET-only today (`internal/sysdep/http.go:30-79`) and no
  JWT/RSA code or library exists (`go.mod:5-12`).
- Refresh delivery — the binding constraint. `SpawnWithSecret`
  (`processmanager.go:110-113`) and `_read_secrets`/`_secrets_read`
  (`enforcer.py:138-143`) together make the current channel structurally
  one-shot; the addon additionally ignores later `creance_secret_fd` option
  updates. Any refresh mechanism is new surface on both sides (see options).
- Refresh scheduling — a host-side loop needs an owner whose lifetime matches
  the proxy's. Today no Go process outlives a CLI command except the run
  session itself; `internal/configwatch` (one goroutine per run session,
  `configwatch.go:99-133`) is the only existing pattern, and multiple
  concurrent run sessions sharing one proxy would each run their own loop
  (coordination state would follow the flocked `proxy.lock` pattern,
  `lifecycle.go:509-538`).
- 472 semantics — mint-at-spawn failure maps naturally onto warn-and-omit →
  per-request 472; a *refresh* failure after a successful initial mint is a
  new state (stale-but-valid token until expiry, then upstream 401 annotated
  `X-Cage-Injected`, or a proactive 472). Whatever is chosen must stay inside
  the pinned 470/471/472 taxonomy or extend it via the AC-0047 checklist.

### Backward Compatibility Options

- **Option A: consume the AC-0069b broker as the delivery/rotation channel**
  (the recorded evolution target; the epic prefers b before/alongside a).
  Pros: purpose-built for rotation ("update the served credential without
  restarting the proxy", AC-0069b Desired Outcome); moves custody out of
  Python; one channel for static + minted. Cons: sequences AC-0069a behind
  AC-0069b; two-ticket dependency for one feature.
- **Option B: extend the fd channel to a long-lived stream** (keep the write
  end open; addon reads framed updates instead of read-to-EOF). Pros: no new
  transport; stays off disk. Cons: rewrites the recorded one-shot contract on
  both sides (`SpawnWithSecret`, `_read_secrets`, their tests); the writer
  must then outlive the CLI command that spawned the proxy, which the current
  detached-daemon model (`Setsid`, CLI exits) does not provide; still
  Python-side custody.
- **Option C: respawn the proxy per refresh** (re-runs resolution by existing
  design, `lifecycle.go:176-191`). Pros: zero new mechanism. Cons: directly
  conflicts with the ticket's "refresh without interrupting in-flight cage
  requests" criterion; refcount/lock churn every ≤1h.
- **Option D: static-path preservation regardless of channel** — minted kinds
  are additive config (`credentials:` entries of a new kind); existing static
  credentials keep the one-shot fd path untouched. AC-0069b's Out of Scope
  already sanctions this split ("static tokens may continue to use the
  simpler path if justified at planning"). This is orthogonal to A–C and is
  what keeps Phase-1 behavior backward-compatible during the transition.

## External Sources

**GitHub App installation tokens**

- https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app — JWT claims (iat −60s, exp ≤10min, iss = client ID), RS256.
- https://docs.github.com/en/rest/apps/apps?apiVersion=2022-11-28#create-an-installation-access-token-for-an-app — mint endpoint, `repositories`/`permissions` body, 1h lifetime.
- https://docs.github.com/en/rest/apps/apps?apiVersion=2022-11-28#get-a-repository-installation-for-the-authenticated-app — installation discovery per repo.
- https://docs.github.com/en/rest/apps/installations?apiVersion=2022-11-28#revoke-an-installation-access-token — `DELETE /installation/token`.
- https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation — git-over-HTTPS `x-access-token` Basic shape.
- https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/managing-private-keys-for-github-apps — PKCS#1 PEM, ≤25 keys, manual rotation, storage guidance.
- https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app — personal-account registration, webhooks optional.
- https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app — Contents permission required for HTTP git.
- https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api?apiVersion=2022-11-28 — installation-token rate limits.
- https://docs.github.com/en/actions/concepts/security/github_token — the GITHUB_TOKEN model (per-job installation token).
- https://github.blog/changelog/2024-05-01-github-apps-can-now-use-the-client-id-to-fetch-installation-tokens/ — client-ID-as-iss.
- https://github.blog/changelog/2026-04-24-notice-about-upcoming-new-format-for-github-app-installation-tokens/ — new ~520-char `ghs_` token format; treat tokens as opaque.
- https://pkg.go.dev/github.com/bradleyfalzon/ghinstallation/v2 and https://github.com/bradleyfalzon/ghinstallation/blob/master/transport.go — transport-based minting, refresh at expiry −60s.
- https://pkg.go.dev/github.com/golang-jwt/jwt/v5 — RS256 signing, PKCS#1 PEM parsing.
- https://github.com/actions/create-github-app-token — mint-scoped/use/revoke-on-teardown reference implementation.
- https://github.com/probot/probot/issues/1426 , https://github.com/actions/create-github-app-token/issues/178 — clock-skew failure reports.
- https://github.com/cli/cli/issues/12798 — `gh issue create` needs Contents:Read.

**OAuth2 refresh minting**

- https://datatracker.ietf.org/doc/html/rfc6749#section-6 — refresh grant; rotation MAY; invalid_grant.
- https://datatracker.ietf.org/doc/html/rfc9700 — OAuth Security BCP (public-client refresh tokens: sender-constrained or rotated).
- https://datatracker.ietf.org/doc/html/rfc8252 — native apps / loopback redirect.
- https://datatracker.ietf.org/doc/html/rfc8628 — device authorization grant.
- https://datatracker.ietf.org/doc/html/rfc7523 — JWT bearer grant (service accounts).
- https://pkg.go.dev/golang.org/x/oauth2 and https://github.com/golang/oauth2/blob/master/token.go — TokenSource, ReuseTokenSourceWithExpiry, 10s default expiry delta.
- https://developers.google.com/identity/protocols/oauth2 — refresh-token expiration rules (7-day Testing status, 100-token cap, 6-month disuse).
- https://developers.google.com/identity/protocols/oauth2/native-app — installed-app flow (refresh tokens always issued; loopback; PKCE).
- https://developers.google.com/identity/protocols/oauth2/limited-input-device — device-flow scope restrictions (Drive: only drive.file/drive.appdata).
- https://developers.google.com/workspace/drive/api/guides/api-specific-auth — Drive scope classification (drive.file non-sensitive; drive.readonly restricted).
- https://developers.google.com/identity/protocols/oauth2/resources/oob-migration — OOB deprecation.
- https://developers.google.com/identity/protocols/oauth2/service-account — JWT-bearer service-account flow.

## Open Questions

1. **Sequencing with AC-0069b.** The epic prefers the broker before or
   alongside minting; the fd channel is structurally one-shot. Does AC-0069a
   proceed against the broker (Option A), or does planning deliberately extend
   the fd contract (Option B)? This is the Phase-1 Open Decision surfacing
   here; it is formally held in AC-0069b.
2. **Who owns the refresh loop.** The proxy is a detached daemon; the CLI
   exits; only a run session has a persistent Go process today (configwatch
   pattern). With multiple concurrent run sessions sharing one proxy, which
   process refreshes, and how do they coordinate (flocked `proxy.lock` is the
   existing pattern)? A broker process (AC-0069b) would answer this
   structurally.
3. **App private-key custody.** `keychain://` and `op://` both already resolve
   via the SecretResolver, but the no-prompt-after-spawn constraint means the
   key (or the minting capability) must be available to the refresh loop
   without re-prompting — resolve-once-and-hold vs re-resolve per refresh is
   undecided. GitHub's guidance (no env vars, prefer vault/sign-only) and the
   public-repo config discipline (no personal references committed) both
   apply.
4. **Refresh cadence and idle-past-expiry.** Reference points: ghinstallation
   refreshes at expiry −60s (lazy); x/oauth2 defaults to −10s (lazy,
   configurable). A proactive loop must pick a margin, decide jitter, and
   define what happens when the cage sits idle past expiry (mint on next
   request? keep hot?). The ticket names clock-skew margin explicitly.
5. **Refresh-failure semantics.** Between "refresh failed" and "old token
   expired" the injected token is stale-but-valid; after expiry, requests get
   upstream 401 + `X-Cage-Injected` (existing behavior) unless the design
   proactively 472s. Which behavior is wanted, and does it need a briefing/
   SKILL.md update (AC-0047 checklist)?
6. **First OAuth2 target and its bootstrap.** Google Drive was the sketch:
   scope choice is consequential (`drive.file` frictionless vs `drive.readonly`
   restricted/verification), the initial consent needs a loopback listener
   host-side (out-of-cage), and the refresh token needs durable storage
   (keychain fits the existing backends). The Testing-status 7-day expiry and
   100-token cap shape onboarding docs.
7. **Revocation on cage close.** GitHub Actions revokes the job token on
   teardown (`DELETE /installation/token`); should last-agent `Detach` do the
   same for minted tokens? Nothing in the ticket requires it, but the
   reference model includes it.
8. **GraphQL posture is unchanged by minting.** The AC-0068 review pinned:
   token lifetime bounds the leak window, not public-read scope; scoped REST
   stays the default and `/graphql` a documented opt-in. Planning should not
   re-open this.

## tce Config Drift

Two concrete mismatches between `.claude/tce/profile.md`'s code map and the
codebase (both from packages/files added during AC-0068 and the config-watch
work):

- `internal/configwatch/` (fsnotify-backed config hot-reload watcher, wired
  into `run`) exists but has no code-map row.
- The `internal/cli` row's command list ("one file per command: run, init,
  setup, doctor, status, policy, allow, deny, logs, import, clean, version")
  predates `credential.go`, `domain.go`, `mutate.go`, `edit.go`, `inject.go`.

Consider running `/tce:refresh` to reconcile the profile.
