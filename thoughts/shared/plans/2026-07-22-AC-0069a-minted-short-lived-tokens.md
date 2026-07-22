# AC-0069a: Minted short-lived tokens — GitHub App installation + OAuth2 refresh

## Overview

Add **host-side minting and automatic refresh** of short-lived credentials on top
of the AC-0069b broker. Two flows: GitHub App installation tokens (JWT-sign with
the app private key → ≤1h repo-scoped installation token) and an OAuth2
refresh-grant flow (Google Drive `drive.file`). The broker daemon — the only
long-lived host-side Go process, and the custodian of the served token — becomes
the refresh-loop owner: it resolves key material once at spawn (Touch ID intact),
mints the first token, and re-mints before expiry through `broker.Store.Set`, the
rotation entry point AC-0069b built and left unexercised. The app private key /
OAuth refresh token never enter the cage.

The Python enforcer is **not touched**: it fetches a token by credential name from
the broker and renders it through the existing header/template shape, indifferent
to whether the token was resolved (static) or minted (this ticket).

## Current State Analysis

The AC-0069b broker (shipped, `internal/broker/`) is the substrate this ticket
consumes. Everything the refresh path needs already exists but is unused:

- **`Store.Set(name, token, expiresAt)`** (`internal/broker/store.go:63`) is the
  rotation entry point: thread-safe under an `RWMutex`, it wipes and unlocks the
  buffer it replaces so a refresh loop can swap a token in place while requests are
  served. Today it is called once per credential at broker startup with a **zero**
  `expiresAt` (`internal/cli/broker.go:123`).
- **`Server.answer`** (`internal/broker/server.go:96-100`) already returns
  `ErrExpired` when a non-zero `expiresAt` is at/before `clock.Now()`. The addon
  maps that to the same 472 as `unknown_credential`. This **is** the ticket's
  decision-3 "stale-then-472-at-expiry" semantics, pre-built: keep `Set`-ing a
  fresh expiry and requests succeed; let expiry pass without a successful refresh
  and the caged agent gets a human-recoverable 472 instead of a dead token
  upstream.
- **The wire protocol carries `expires_at`** already
  (`internal/broker/protocol.go:23-42`); `ErrUnknownCredential` and `ErrExpired`
  are distinguished on the wire for the audit trail, both → 472 at the addon.

The broker daemon (`internal/cli/broker.go:57-126`, hidden `broker` command)
reads a flat `map[string]string` payload from fd 3 at spawn
(`loadSecrets`, `:107-126`), `Set`s each with zero expiry, `Listen`s on a `0600`
socket, and `Serve`s until SIGTERM, then `Wipe`s. The fd-3 payload is built by
`resolveInjectionSecrets` (`internal/cli/inject.go:26-70`) from the compiled
policy's `inject` names + the `SecretResolver`.

What does **not** exist:

- **No minting.** No JWT/RSA code anywhere; `crypto/*` use is SHA-256 only. No
  `golang-jwt`, no `golang.org/x/oauth2`, no GitHub client in `go.mod`.
- **The HTTP seam is GET-only** (`internal/sysdep/http.go:30-79`). Minting needs
  POST (mint, refresh grant, code exchange) and DELETE (revoke).
- **`config.Credential` is `{Source, Header, Template, Username}`**
  (`internal/config/config.go:137-142`) — a static reference only. Strict YAML
  decoding (`KnownFields(true)`, `config.go:189`) rejects any unknown key, so a
  minted credential's extra fields are an explicit, versioned schema change.
- **`policy.Credential`** (`internal/policy/policy.go:112-136`) mirrors the config
  shape into `policy.json`, reference-only. The Python reader
  (`internal/proxy/enforcer/policy.py:109-115`) reads keys via `d.get(...)` and
  **ignores unknown ones** — confirmed — so new credential keys need no Python
  change and an old addon reading a minted credential renders an empty token → a
  safe 472.
- **No OAuth2 consent bootstrap.** No loopback listener, no browser-open, no PKCE,
  and `sysdep.Keychain` reads but cannot **write** a keychain item.

## Desired End State

A user can declare a minted credential and bind it to a host:

```yaml
credentials:
  gh-app:
    template: "Bearer {token}"           # or Basic x-access-token for git-over-HTTPS
    github_app:
      key: keychain://agent-creance/ghapp-key   # PKCS#1 PEM, host-side only
      client_id: "Iv1.0123456789abcdef"          # non-secret
      repo: tobyS/agent-creance                   # single repo (this ticket)
      permissions: { contents: read, issues: write }
  drive:
    template: "Bearer {token}"
    oauth2:
      refresh_token: keychain://agent-creance/drive-refresh
      client_id: "1234.apps.googleusercontent.com"
      # token_endpoint + scopes default to Google Drive drive.file
```

`agent-creance credential authorize drive` runs a one-time host-side loopback
consent that stores the refresh token in the keychain. `agent-creance run` then
spawns the broker, which mints an installation token / an access token, injects it
into requests to the bound host, and re-mints before expiry with no proxy restart
and no in-flight races. If a refresh keeps failing past expiry, injected requests
get a 472; the doctor/status surface says so. On last-agent teardown the broker
best-effort-revokes minted GitHub tokens (`DELETE /installation/token`).

**Verify:** `make test` + `make test-enforcer` green; `make test-integration` +
`make test-enforcer-integration` green (out-of-cage); a real GitHub App mint and a
real Drive refresh authenticate end-to-end; a killed-refresh scenario 472s at
expiry.

### Key Discoveries:

- **The broker is the refresh-loop owner** — the only host-side Go process that
  outlives the spawning CLI (`internal/cli/broker.go:29-33`), and it holds the
  `Store`. The refresh loop is a per-credential goroutine in the broker, following
  the `internal/configwatch` background-goroutine pattern
  (`configwatch.go:55-188`) with the `sysdep.Sleeper`
  (`internal/sysdep/sleeper.go:18-38`) and `sysdep.Clock`
  (`internal/sysdep/clock.go:12-31`) seams.
- **Decision 3 is already implemented** by `server.go:96-100`. The refresh loop's
  only job on failure is to *stop calling `Set`*; expiry does the rest.
- **The Python enforcer needs no change** (`policy.py:109-115` ignores unknown
  keys; the token comes from the broker by name; the shape DSL is unchanged).
  Minting is entirely a Go-side concern.
- **The 472 taxonomy is untouched** — `expired` reuses the shipped
  `responses.injection_unavailable` 472. The AC-0047 pinned-literal surface stays
  out of scope (a deliberate constraint, mirroring AC-0069b).
- **Secret resolution stays spawn-only** (Touch-ID guard,
  `internal/proxy/lifecycle.go:108-116`, `176-191`). Key material is resolved once
  at spawn and delivered to the broker over the existing fd-3 pipe; it is never
  re-resolved (that would re-prompt), so the broker holds it for the session.
- **Down-scoping is a cap, not a grant** (research): requested `repositories` /
  `permissions` must be a subset of what the App registration + installation
  already hold.
- **GitHub clock skew** is the classic failure — mitigated by the documented
  `iat − 60s` backdate and `exp ≈ 9min`, computed off `sysdep.Clock`.

## What We're NOT Doing

- **No change to the 470/471/472 taxonomy or its pinned literals** (AC-0047
  surface) — `expired` reuses the existing 472 body verbatim; the wire-level
  `unknown_credential` vs `expired` distinction is for the audit trail only.
- **No change to the `/graphql` public-read posture** (AC-0068 review): token
  lifetime bounds the leak window, not public-read scope; scoped REST stays the
  default, `/graphql` a documented opt-in. Not reopened.
- **No multi-repo GitHub App tokens** — single `repo:` per credential (user
  decision); multiple repos = multiple credentials.
- **No second OAuth2 provider** beyond Google Drive `drive.file`, and no restricted
  Drive scopes (`drive.readonly`/`drive` trigger Google app-verification).
- **No service-account (JWT-bearer) flow** — personal Drive is not reachable that
  way without Workspace domain-wide delegation.
- **No `memguard`, no peer-uid check** — inherited from AC-0069b (mlock is
  hygiene; filesystem permissions are the control).
- **No automatic broker restart** on death — fail closed, surface in doctor/status
  (AC-0069b behavior, unchanged).
- **No refresh-token encryption at rest beyond the keychain** — the keychain is the
  store, as for every other secret reference.

## Implementation Approach

Build bottom-up behind `internal/sysdep` seams so every phase is hermetically
testable, then wire the broker refresh loop and the human-facing onboarding last.
The credential *model* grows first (Phase 1), the *minting engine* is written and
unit-tested in isolation (Phase 2), the broker learns to *drive* it (Phase 3),
and the CLI learns to *register and authorize* minted credentials (Phases 4–5),
before real external flows and docs (Phase 6). Static credentials keep the exact
Phase-1/AC-0069b behavior throughout — a minted credential is purely additive.

The fd-3 payload changes shape from a flat `{name: token}` map to a structured
per-credential spec (`static | github_app | oauth2`). This is a Go→Go contract
inside one binary (the broker is a re-exec of the same `agent-creance`), so it
needs no cross-language versioning — the CLI that writes it and the broker that
reads it are always the same build.

---

## Phase 1: Config & compiled-policy schema for minted credentials

### Overview

Grow the credential model — config parse/validate/serialize, the compiled
`policy.json`, and a Go↔Python parity test — so a minted credential can be
declared, validated, compiled, and round-tripped. No minting behavior yet.

### Changes Required:

#### 1. Config schema

**File**: `internal/config/config.go`
**Changes**: `Credential` gains two optional, mutually-exclusive sub-blocks; the
presence of one is the "minted" discriminator, and `Source` becomes required only
for the static form.

```go
type Credential struct {
    Source   string `yaml:"source"`   // static form; empty for a minted credential
    Header   string `yaml:"header"`
    Template string `yaml:"template"`
    Username string `yaml:"username"`

    GitHubApp *GitHubAppMint `yaml:"github_app"` // mutually exclusive with Source/OAuth2
    OAuth2    *OAuth2Mint    `yaml:"oauth2"`
}

// GitHubAppMint mints a repo-scoped installation token host-side (≤1h).
type GitHubAppMint struct {
    Key         string            `yaml:"key"`          // PKCS#1 PEM secret ref (op://|keychain://|env://)
    ClientID    string            `yaml:"client_id"`    // non-secret
    Repo        string            `yaml:"repo"`         // owner/name (single repo)
    Permissions map[string]string `yaml:"permissions"`  // e.g. {contents: read, issues: write}
}

// OAuth2Mint mints a short-lived access token from a stored refresh token.
type OAuth2Mint struct {
    RefreshToken  string   `yaml:"refresh_token"` // secret ref
    ClientID      string   `yaml:"client_id"`
    TokenEndpoint string   `yaml:"token_endpoint"` // default: Google
    Scopes        []string `yaml:"scopes"`         // default: drive.file
}
```

Mirror the two structs into `rawConfig`'s `Credentials map[string]Credential`
(`config.go:153`) — it already uses the public `Credential` type, so the sub-blocks
decode strictly with `KnownFields`. Add Google defaults in `applyDefaults`
(`config.go:226-249`): a nil/empty `OAuth2.TokenEndpoint` →
`https://oauth2.googleapis.com/token`, empty `Scopes` →
`["https://www.googleapis.com/auth/drive.file"]`. Keep `defaultCredentialHeaders`
(`config.go:262-269`) — the shape default applies to minted credentials too.

#### 2. Config validation

**File**: `internal/config/validate.go`
**Changes**: In `validateCredentials` (`:74-101`), branch on the form:

- Exactly one of {`Source` set, `GitHubApp` non-nil, `OAuth2` non-nil} — zero or
  more than one is an error.
- Static: existing checks (source syntax, template).
- `GitHubApp`: `Key` present + valid secret ref (`sysdep.ValidateSecretRefSyntax`);
  `ClientID` non-empty; `Repo` matches `owner/name`; each permission value is a
  known access level (`read`/`write`/`admin`); template still required.
- `OAuth2`: `RefreshToken` valid secret ref; `ClientID` non-empty; `TokenEndpoint`
  a plausible `https://` URL; template required.

The `inject → defined credential` cross-layer check (`ValidateEffective`,
`:125-158`) is unchanged — it keys on the credential name, not its form.

#### 3. Config serialization (writers)

**File**: `internal/config` credential writer (`AppendCredential`, used at
`internal/cli/credential.go:148`; `RemoveCredential` at `:241`)
**Changes**: Extend the credential YAML emission to render the `github_app:` /
`oauth2:` sub-blocks when present. `RemoveCredential` needs no change (removes by
name). Add golden/round-trip coverage for a minted entry.

#### 4. Compiled policy

**File**: `internal/policy/policy.go`
**Changes**: `Credential` (`:112-117`) gains the same two optional blocks (as
`policy`-tagged JSON structs, reference-only — `Key`/`RefreshToken` are secret
*references*, the rest non-secret config). `CredentialsFromConfig` (`:122-136`)
copies them across. **No `CompiledVersion` bump** (`:105`): the change is additive
and old readers fail safe (empty token → 472). Add the fields to the Python
`Credential` dataclass optionally is **not** required (it ignores them); instead
pin that with a parity test (below).

#### 5. Go↔Python tolerance parity test

**File**: `internal/proxy/enforcer/test_policy.py` (or the nearest existing policy
test), plus a Go golden for a compiled minted credential
**Changes**: A test asserting `Credential.from_dict` (`policy.py:109-115`) parses a
credential object carrying `github_app`/`oauth2` keys, ignores them, and yields the
correct `source`/`header`/`template`/`username`. This locks the "Python ignores
minting keys" invariant against a future strict-decode regression.

### Success Criteria:

#### Automated Verification:

- [x] `make test` passes (`go test -race ./...`)
- [x] `make lint` passes; `go build ./...` succeeds
- [x] `make test-enforcer` passes (the new `policy.py` tolerance test)
- [x] Table tests: a minted `github_app` and a minted `oauth2` credential parse,
      default (Google endpoint/scopes), validate, and compile; a static credential
      is unchanged
- [x] Validation rejects: two forms set at once; a minted block with a missing/bad
      secret ref; a `repo` not in `owner/name` form; an unknown permission level
- [x] `make golden` reviewed: `policy.json` golden gains the minting blocks;
      config round-trip golden covers a minted entry
- [x] Go↔Python parity test: the addon ignores `github_app`/`oauth2` keys and reads
      the shape correctly

#### Manual Verification:

- [ ] None (no user-visible behavior yet)

### Implementation log

- **Status**: ✅ Complete
- **Base commit**: `ff43db0` (HEAD before any implementation commit)
- **Commit**: _pending_
- **Did**: `config.Credential` gained `GitHubApp`/`OAuth2` sub-blocks + `IsMinted()`,
  OAuth2 endpoint/scope defaults, form-branching validation (repo slug, perm levels,
  https endpoint). `policy.Credential` mirrors them (reference-only, `source` now
  omitempty). Writer renders/round-trips minted entries; `sameCredentials` → deep
  equal. New `credentials_minted_test.go`; compile golden extended; Python parity
  test `test_minted_credential_keys_ignored_but_shape_read`.
- **Issues**: `Credential` gained pointer/map fields → writer's `==` compare replaced
  with `reflect.DeepEqual`; append round-trip now also applies minting defaults.
- **Verification**: ✅ make test, ✅ make lint, ✅ make test-enforcer (161 passed)

---

## Phase 2: Minting engine behind seams

### Overview

The pure minting logic and the one new OS seam it needs — GitHub App installation
tokens and OAuth2 refresh grants — all hermetically testable against a fake HTTP
transport and a fake clock. Nothing wired into the broker yet.

### Changes Required:

#### 1. HTTP seam (POST/GET/DELETE)

**File**: `internal/sysdep/http.go`, `internal/sysdep/sysdeptest/http.go`
**Changes**: Add a general request method to the HTTP seam alongside GET-only
`HTTPGetter`. Introduce `HTTPClient`:

```go
type HTTPClient interface {
    Do(ctx context.Context, method, url string, headers map[string]string, body []byte) (status int, respBody []byte, err error)
}
```

`OSHTTPClient` reuses the existing body cap / timeout discipline
(`http.go:16-20`). Keep `HTTPGetter`/`OSHTTPGetter` for the registry consumer
(`internal/generator/registry`), or express `Get` in terms of `Do`. Add the
`sysdeptest` fake: scripted `(status, body, err)` keyed by `(method, url)`, and a
recorder so tests can assert the request body/headers. Compile-time assertion +
seam-trio pattern (`clock.go` / `sysdeptest/clock.go`).

#### 2. Minter interface + factory

**File**: `internal/mint/mint.go` (new)
**Changes**:

```go
// Minter produces a short-lived token and, where supported, revokes it.
type Minter interface {
    Mint(ctx context.Context) (token string, expiresAt time.Time, err error)
    Revoke(ctx context.Context, token string) error // best-effort; nil-op where unsupported
}
```

A factory `New(spec broker.CredentialSpec, http sysdep.HTTPClient, clock sysdep.Clock) (Minter, error)`
returns the right implementation from the resolved spec (defined in Phase 3), or a
"static" sentinel for non-minted credentials (never called).

#### 3. GitHub App minter

**File**: `internal/mint/githubapp/githubapp.go` (new) + `_test.go`
**Changes**: `Mint` performs, using `golang-jwt/jwt/v5` for RS256 only:

1. Parse the PKCS#1 PEM key (`jwt.ParseRSAPrivateKeyFromPEM`).
2. Sign an RS256 JWT: `iat = clock.Now() − 60s`, `exp = clock.Now() + 9min`,
   `iss = ClientID`.
3. `GET https://api.github.com/repos/{repo}/installation` (JWT Bearer) → parse the
   installation `id` (cache it on the minter for the session).
4. `POST https://api.github.com/app/installations/{id}/access_tokens` (JWT Bearer)
   with body `{repositories: [name], permissions: {…}}` → parse `token`,
   `expires_at` (RFC3339).

`Revoke` → `DELETE https://api.github.com/installation/token` authenticated with
the token itself; best-effort. All HTTP through `sysdep.HTTPClient`. Non-2xx →
typed error carrying status + endpoint, never the token. Treat the token as opaque
(the 2026 `ghs_…` ~520-char format).

#### 4. OAuth2 minter

**File**: `internal/mint/oauth2mint/oauth2mint.go` (new) + `_test.go`
**Changes**: `Mint` performs one form-encoded `POST TokenEndpoint`
(`grant_type=refresh_token&refresh_token=…&client_id=…`) → parse `access_token`,
`expires_in`; `expiresAt = clock.Now() + expires_in`. If the response carries a
**new `refresh_token`**, surface it so Phase 3 can persist it (RFC 6749 §6
rotation; `error=invalid_grant`/HTTP 400 is terminal → typed error signalling
"re-authorize"). `Revoke` is a nil-op (no teardown revoke for OAuth2 here). HTTP
through `sysdep.HTTPClient`.

#### 5. Dependency

**File**: `go.mod` / `go.sum`
**Changes**: add `github.com/golang-jwt/jwt/v5`. No go-github, no x/oauth2.

### Success Criteria:

#### Automated Verification:

- [ ] `make test` passes; `make lint` passes; `go build ./...` succeeds
- [ ] `sysdeptest` HTTP fake records the request (method/url/headers/body) and
      returns scripted responses; a GET expressed via `Do` still satisfies the
      registry consumer
- [ ] GitHub minter tests (fake HTTP + fake clock): JWT claims are correct
      (`iat−60s`, `exp+9min`, `iss=client_id`); the mint request body carries the
      single repo + permissions; `token`/`expires_at` are parsed; a non-2xx yields
      a typed error that does **not** contain the token; `Revoke` issues the DELETE
- [ ] OAuth2 minter tests: refresh grant body is correct; `expires_at` computed
      from `expires_in` + clock; a rotated `refresh_token` is surfaced;
      `invalid_grant`/400 is a terminal typed error
- [ ] JWT signing verifies against the public key in a round-trip test

#### Manual Verification:

- [ ] None (no wiring yet)

---

## Phase 3: Broker refresh loop + enriched fd-3 delivery

### Overview

Teach the broker to receive minting specs, mint the first token, refresh before
expiry through `Store.Set`, and best-effort-revoke on shutdown. Change the Go→Go
fd-3 payload to a structured per-credential spec. Static credentials keep the exact
current behavior.

### Changes Required:

#### 1. Structured fd-3 payload

**File**: `internal/broker/payload.go` (new)
**Changes**: The broker owns its input contract:

```go
type CredentialSpec struct {
    Kind      string          `json:"kind"` // "static" | "github_app" | "oauth2"
    Token     string          `json:"token,omitempty"`      // static
    GitHubApp *GitHubAppSpec  `json:"github_app,omitempty"` // resolved key + non-secret params
    OAuth2    *OAuth2Spec     `json:"oauth2,omitempty"`     // resolved refresh token + params
}
type Payload map[string]CredentialSpec
```

`GitHubAppSpec`/`OAuth2Spec` carry the **already-resolved** key material (PEM /
refresh token strings) plus the non-secret params from the compiled policy.

#### 2. Payload builder (host side)

**File**: `internal/cli/inject.go`
**Changes**: `resolveInjectionSecrets` (`:26-70`) builds a `broker.Payload` instead
of `map[string]string`. Per credential in the compiled policy:

- Static (`Source` set): resolve → `CredentialSpec{Kind:"static", Token: …}`
  (existing warn-and-omit on resolve failure).
- `GitHubApp`: resolve `Key` → `CredentialSpec{Kind:"github_app", GitHubApp:{PEM, ClientID, Repo, Permissions}}`.
- `OAuth2`: resolve `RefreshToken` → `CredentialSpec{Kind:"oauth2", OAuth2:{RefreshToken, ClientID, TokenEndpoint, Scopes}}`.

Resolve failure stays best-effort warn-and-omit → the credential 472s. For an
OAuth2 refresh token that resolves to *not found* (`ErrSecretNotFound`), the warn
is the **authorization hint** (Phase 4): `run 'agent-creance credential authorize NAME'`.

#### 3. Broker daemon: load, mint, refresh, revoke

**File**: `internal/cli/broker.go`
**Changes**: `loadSecrets` (`:107-126`) decodes `broker.Payload`. For each spec:

- `static` → `store.Set(name, []byte(token), zero)` (unchanged behavior).
- minted → construct a `mint.Minter` (via `App.HTTPClient` + `App.Clock`) and hand
  it to the refresher (below); do **not** block startup on the first mint.

`runBroker` (`:57-93`): after `Listen`, start the refresher, then `Serve`. On
`Serve` return (SIGTERM), **before** `store.Wipe`, best-effort `Revoke` each
minted GitHub token (grab the current token from the store first), bounded by a
short timeout. `App` gains `HTTPClient sysdep.HTTPClient` for the broker branch.

#### 4. Refresher

**File**: `internal/broker/refresher.go` (new) + `_test.go`
**Changes**: One goroutine per minted credential, following the `configwatch`
pattern (`configwatch.go:55-188`):

```go
type Refresher struct {
    store   *Store
    clock   sysdep.Clock
    sleeper sysdep.Sleeper
    warn    func(string) // stderr; never a token
}
func (r *Refresher) Run(ctx context.Context, name string, m mint.Minter, margins Margins)
```

- Initial `Mint` → `Set(name, token, expiresAt)`; on failure, warn and retry on a
  bounded backoff (the credential 472s meanwhile — `unknown_credential`).
- Loop: `Sleep` until `expiresAt − margin` (+ small jitter), re-`Mint`, `Set`. On
  failure: **do not** `Set` — the previous token stays (stale-but-valid) until
  `expiresAt`, after which `server.answer` returns `expired` → 472 (decision 3,
  free); keep retrying on backoff so a transient failure self-heals.
- Margins: GitHub ≈ 5min (on the 1h token), OAuth2 ≈ 2min; jitter ≈ ±30s. Keep-hot
  (proactive) regardless of cage idleness.
- `ctx` cancellation stops the loop; `Sleeper.Sleep` is context-cancellable.

If the minter surfaces a **rotated OAuth2 refresh token**, persist it to the
keychain via the write path added in Phase 5 (`App.Keychain.Set`); until Phase 5
lands, log that rotation occurred (Google does not rotate today, so this is
defensive).

#### 5. Composition root

**File**: `internal/cli/cli.go`
**Changes**: `App` gains `HTTPClient sysdep.HTTPClient`, wired to `OSHTTPClient{}`
in `Main` (`:216-249`). The broker command already receives `App`.

### Success Criteria:

#### Automated Verification:

- [ ] `make test` passes; `make lint` passes
- [ ] Refresher tests (fake minter + `FakeClock`/`FakeSleeper`): initial mint sets
      the token; a scheduled re-mint before expiry swaps it in `Store` with no gap;
      a failing re-mint leaves the old token until expiry, after which `Server`
      answers `expired`; the loop retries and self-heals; `ctx` cancel stops it
      without wall-clock delay (assert `Sleeper` calls, per
      `lifecycle_test.go:378-394`)
- [ ] Payload round-trip: a static, a `github_app`, and an `oauth2` spec marshal
      through fd-3 JSON and load into the right minter/store entry
- [ ] `inject.go` builder tests: static path unchanged; minted paths resolve key
      material and carry non-secret params; a resolve failure warns-and-omits; an
      OAuth2 `ErrSecretNotFound` warn contains the authorize hint
- [ ] Broker teardown test (fake minter): SIGTERM revokes minted GitHub tokens
      best-effort, then wipes; a static-only broker revokes nothing
- [ ] The token never appears in any warn/error string (existing hygiene assertion,
      extended to minted specs)

#### Manual Verification:

- [ ] `agent-creance run` with a real minted GitHub credential: the broker mints on
      startup and an injected request authenticates (deferred to Phase 6 for the
      real-key run; hermetic paths covered here)

---

## Phase 4: Registration CLI + cage-start authorization check

### Overview

Let users declare minted credentials from the CLI, and verify at cage start that
each minted credential is authorized/resolvable — warning with an actionable
`credential authorize` hint rather than surfacing a mid-session 472.

### Changes Required:

#### 1. `credential add-github-app`

**File**: `internal/cli/credential.go`
**Changes**: A subcommand under `newCredentialCmd` (`:36`):
`credential add-github-app NAME --key <ref> --client-id … --repo owner/name
--perm contents=read --perm issues=write [--basic --username x-access-token |
--bearer]`. Reuses `resolveTemplate` (`:93-115`) for the shape,
`ValidateSecretRefSyntax` for `--key`, and `applyAndRecompile` (`:147-148`) so a
running cage hot-reloads the *shape* (the token itself needs a respawn to mint —
documented). Repeated `--perm k=v` builds the permissions map.

#### 2. `credential add-oauth2`

**File**: `internal/cli/credential.go`
**Changes**: `credential add-oauth2 NAME --refresh-token <ref> --client-id …
[--token-endpoint …] [--scope …] [--bearer]`. Defaults to the Google Drive
endpoint/scope. The `--refresh-token` reference typically points at the keychain
item `credential authorize` will populate (Phase 5); registering before authorizing
is allowed (the cage-start check flags it).

#### 3. `credential list` + shape display

**File**: `internal/cli/credential.go`
**Changes**: `runCredentialList` (`:168-191`) shows the credential *kind* (static /
github-app / oauth2) and the relevant reference (source / key / refresh_token),
never a value.

#### 4. Cage-start authorization check

**File**: `internal/cli/run.go` (spawn path, the `Secrets` closure at `:241-246`
per research) via `internal/cli/inject.go`
**Changes**: The check piggybacks on the existing spawn-time resolution (so it does
not double-prompt Touch ID). When a minted credential's key material fails to
resolve, `resolveInjectionSecrets` already warns; for an OAuth2 `ErrSecretNotFound`
the warn is specifically:
`credential "NAME" is not authorized yet — run 'agent-creance credential authorize NAME'; requests needing it will be refused (472)`.
The proxy still spawns (never fail-to-spawn); the credential 472s until authorized.

#### 5. Doctor / status surface

**File**: `internal/doctor/`, `internal/status/`
**Changes**: For a project with minted credentials, report a best-effort,
**non-prompting** authorization check: for `keychain://` refs, whether the item
exists; for `op://`, report "configured (unlockable at run)" without prompting. An
unauthorized OAuth2 credential is a **warning** with the `credential authorize`
action, consistent with the AC-0069b `broker-down` warning style.

### Success Criteria:

#### Automated Verification:

- [ ] `make test` passes; `make lint` passes
- [ ] testscript (`internal/cli/testdata/script/credential_minted.txtar`):
      `add-github-app` and `add-oauth2` write the expected YAML sub-blocks;
      validation errors are surfaced; `list` shows the kind; hidden/help behavior
      correct
- [ ] Inject-builder test: an unauthorized OAuth2 credential produces the exact
      authorize-hint warning and is omitted (proxy still spawns)
- [ ] Doctor/status tests: unauthorized minted credential ⇒ warning with the
      authorize action; a fully-authorized project ⇒ no warning; no minted
      credentials ⇒ no report
- [ ] `make golden` reviewed for any doctor/status golden changes

#### Manual Verification:

- [ ] `credential add-github-app` / `add-oauth2` then `credential list` reads back
      correctly and `agent-creance run` warns actionably when unauthorized

---

## Phase 5: OAuth2 consent bootstrap — `credential authorize`

### Overview

A one-time, host-side, out-of-cage interactive flow that obtains an OAuth2 refresh
token via RFC 8252 loopback + PKCE and stores it in the keychain, so the broker can
mint access tokens from it.

### Changes Required:

#### 1. Keychain write

**File**: `internal/sysdep/keychain.go` (+ `sysdeptest`)
**Changes**: Add `Set(service, account string, secret []byte) error` to the
`Keychain` seam (`security add-generic-password -U -s service -a account -w …`,
passed via stdin, never argv). The fake records stored items. This is what
`credential authorize` writes and what a rotated refresh token (Phase 3) persists.

#### 2. Browser-open seam

**File**: `internal/sysdep/browser.go` (new) + `sysdeptest`
**Changes**: `Browser.Open(url string) error` → `open <url>` on macOS via the
`Commander`. Fake records the opened URL.

#### 3. `credential authorize` command

**File**: `internal/cli/authorize.go` (new)
**Changes**: `credential authorize NAME` for an OAuth2 credential:

1. Load the config; find `NAME`'s `oauth2` block (error if not OAuth2 /
   not defined).
2. Generate a PKCE verifier + S256 challenge and a `state` nonce (stdlib
   `crypto/rand`, `crypto/sha256`, base64url — pure, testable).
3. Start a loopback listener on `127.0.0.1:0` (a random ephemeral port; **not**
   `$TMPDIR`-style paths — a real TCP loopback), build the Google auth URL
   (`https://accounts.google.com/o/oauth2/v2/auth`) with `client_id`,
   `redirect_uri=http://127.0.0.1:<port>`, `scope`, `code_challenge`,
   `access_type=offline`, `prompt=consent`, `state`.
4. `Browser.Open` the URL; print a copy-paste fallback.
5. Receive the redirect, validate `state`, exchange `code` + `verifier` at the
   token endpoint (`sysdep.HTTPClient` POST) → `refresh_token`.
6. `Keychain.Set` the refresh token at the credential's `refresh_token`
   keychain reference; print success and next steps.

Refuse cleanly on `op://`/`env://` refresh-token refs (authorize can only write a
`keychain://` ref). Bounded overall timeout; the listener is closed on return.

#### 4. Command registration

**File**: `internal/cli/credential.go`
**Changes**: register `authorize` under `newCredentialCmd` (`:36`). `App` gains
`Browser sysdep.Browser`, wired in `cli.go` `Main`.

### Success Criteria:

#### Automated Verification:

- [ ] `make test` passes; `make lint` passes
- [ ] PKCE generation test: verifier/challenge satisfy S256 (`base64url(sha256(v))`),
      correct lengths, unpadded
- [ ] Code-exchange test (fake HTTP): the exchange POST body carries `code`,
      `code_verifier`, `client_id`, `grant_type=authorization_code`,
      `redirect_uri`; the `refresh_token` is parsed and handed to `Keychain.Set`
- [ ] Callback-handler test: a mismatched `state` is rejected; a well-formed
      callback yields the code
- [ ] testscript / unit: `authorize` on a non-OAuth2 or undefined credential errors
      clearly; an `op://` refresh-token ref is refused with guidance
- [ ] `Keychain.Set` fake records the stored item; the secret is passed via stdin,
      never argv (hygiene assertion)

#### Manual Verification:

- [ ] `agent-creance credential authorize drive` opens the browser, completes Google
      consent, stores the refresh token, and a subsequent `run` mints an access
      token that authenticates against Drive (out-of-cage; Phase 6 batch)

---

## Phase 6: Integration, docs, and dogfood

### Overview

Real GitHub App mint + real OAuth2 refresh, end to end; then correct the design doc
and rebuild. **This phase runs out-of-cage** (real external services, real
`mitmdump`/`agent-safehouse`) — batch it as one breakout per the dogfooding rule.

### Changes Required:

#### 1. GitHub App integration test

**File**: `internal/mint/githubapp/githubapp_integration_test.go` (new,
`//go:build integration`)
**Changes**: env-gated (e.g. `AC_TEST_GH_APP_KEY_REF`, `AC_TEST_GH_APP_CLIENT_ID`,
`AC_TEST_GH_APP_REPO`): mint a real installation token, assert presence + a future
`expires_at` ≤ ~1h, assert an authenticated `GET /repos/{repo}` works, then
`Revoke` and assert the token no longer authenticates. Assert-presence-only on the
token (never log it).

#### 2. OAuth2 refresh integration test

**File**: `internal/mint/oauth2mint/oauth2mint_integration_test.go` (new,
`//go:build integration`)
**Changes**: env-gated (`AC_TEST_DRIVE_REFRESH_REF`, `AC_TEST_DRIVE_CLIENT_ID`):
refresh a real access token from a stored refresh token, assert presence + a future
`expires_at`, and (optionally) a `drive.file` API call.

#### 3. End-to-end broker mint test

**File**: `internal/proxy/broker_integration_test.go` (extend the AC-0069b test)
**Changes**: with a real minted GitHub credential, assert an injected request
carries a real (minted) token and the concurrent-proxy isolation invariant still
holds with minted credentials.

#### 4. Docs

**File**: `docs/design.md`
**Changes**: extend the "Credential injection" section: minted short-lived tokens
(GitHub App + OAuth2), the broker as refresh-loop owner, the fd-3 spec delivery,
`credential authorize`, best-effort revocation, and the honest bounds (app key /
refresh token never enter the cage; TTL + scope are the leak bound; `/graphql`
posture unchanged; 472 taxonomy unchanged — `expired` reuses the shipped 472).
Note the GitHub App registration + install onboarding (webhooks off, single-repo
install, PKCS#1 PEM key stored in the keychain) and the Google "Desktop app" OAuth
client + Testing-status 7-day / 100-token caveats. **No** `internal/cage/briefing.md`
or `internal/setup/skill.go` change (472 semantics unchanged, deliberately).

#### 5. Rebuild

**File**: `bin/agent-creance`
**Changes**: `make build` at the final commit (the user tests with this binary).

### Success Criteria:

#### Automated Verification:

- [ ] `make test` and `make lint` green
- [ ] `make test-integration` passes out-of-cage (GitHub App mint + broker e2e)
- [ ] `make test-enforcer` / `make test-enforcer-integration` green
- [ ] `make build` produces `bin/agent-creance` at the final commit
- [ ] `grep` confirms the AC-0047 pinned-literal 472 surface is unchanged

#### Manual Verification:

- [ ] A real GitHub App credential authenticates a caged `gh`/REST call end-to-end;
      killing the refresh (or waiting past expiry with refresh disabled) yields a
      472 with the human-recoverable body while non-injected hosts still work
- [ ] `credential authorize drive` → a caged Drive `drive.file` call authenticates
      with a minted access token; the token visibly rotates across the expiry
      boundary without a proxy restart
- [ ] `docs/design.md` describes the shipped minting/refresh/authorize/revocation
      accurately

---

## Testing Strategy

### Unit Tests:

- **Config/policy**: table tests for parse/validate/default/compile of both minted
  forms and the unchanged static form; Go↔Python tolerance parity; config
  round-trip golden.
- **HTTP seam**: fake records requests and returns scripted responses; GET-via-Do
  parity for the registry consumer.
- **Minters**: fake-HTTP + fake-clock coverage of JWT claims, mint/refresh request
  bodies, expiry computation, error taxonomy (non-2xx, `invalid_grant`), revoke;
  JWT sign/verify round-trip.
- **Refresher**: fake minter + `FakeClock`/`FakeSleeper` — initial mint, pre-expiry
  swap, stale-then-`expired` on failure, self-heal, cancel — no wall-clock time.
- **CLI**: testscript for `add-github-app`/`add-oauth2`/`authorize`; PKCE +
  code-exchange + callback-handler units; the authorize-hint warning; doctor/status
  warning states.

### Integration Tests (out-of-cage, `integration` tag):

- Real GitHub App mint + revoke; real OAuth2 Drive refresh.
- Real broker + real `mitmdump` + real socket injecting a **minted** token; the
  concurrent-proxy isolation invariant with minted credentials.

### Manual Testing Steps:

1. Register a GitHub App credential; `run`; confirm an injected REST call
   authenticates with a minted token (`x-cage-injected`).
2. Wait past the refresh boundary; confirm the token rotated with no proxy restart.
3. Disable/kill refresh; confirm a 472 at expiry while non-injected hosts work.
4. `credential authorize drive`; complete consent; confirm a minted Drive access
   token authenticates.
5. Exit the last agent; confirm the broker best-effort-revoked the GitHub token and
   left no socket/process behind.

## Performance Considerations

Minting is one JWT sign + two HTTPS calls per GitHub credential per hour (one POST
per OAuth2 refresh); the refresh goroutine sleeps the rest of the time. Negligible
against the cage's egress traffic. The per-request broker fetch is unchanged
(sub-millisecond AF_UNIX round-trip; AC-0069b). A brief startup window exists
between `Serve` starting and the first mint completing, during which a minted
credential answers 472 (`unknown_credential`) — bounded by mint latency (~1s) and
fail-safe.

## Migration Notes

Purely additive. Existing static credentials parse and behave unchanged (config,
`policy.json`, and the fd-3 path all carry `kind:"static"` implicitly / explicitly).
`policy.json` gains optional credential sub-blocks with no `CompiledVersion` bump —
an old Python addon ignores them and an old binary reading a minted credential
renders an empty token → safe 472. The fd-3 payload format change (flat map →
structured spec) is a Go→Go contract inside one binary build (the broker is a
re-exec of the same `agent-creance`), so no cross-version delivery ever mixes
formats; the AC-0069b `proxy.lock` old-binary tolerance is unaffected.

## References

- Ticket: `thoughts/shared/tickets/AC-0069a-minted-short-lived-tokens.md`
  (binding decisions in Notes & Updates 2026-07-12: broker channel; Drive
  `drive.file`; stale-then-472-at-expiry; best-effort revocation)
- Research: `thoughts/shared/research/2026-07-11-AC-0069a-minted-short-lived-tokens.md`
- Epic: `thoughts/shared/tickets/AC-0069-credential-injection-phase2-epic.md`
- The channel this consumes (shipped): `thoughts/shared/tickets/AC-0069b-secret-broker.md`,
  `thoughts/shared/plans/2026-07-13-AC-0069b-secret-broker.md`
- Broker substrate: `internal/broker/store.go:63` (`Set`),
  `internal/broker/server.go:96-100` (`expired`→472),
  `internal/broker/protocol.go:23-42`, `internal/cli/broker.go:57-126`
- Injection substrate: `internal/cli/inject.go:26-70`,
  `internal/config/config.go:137-142`, `internal/policy/policy.go:112-136`,
  `internal/proxy/enforcer/policy.py:109-115` (tolerant reader)
- Patterns to follow: `internal/configwatch/configwatch.go:55-188`
  (background goroutine), `internal/sysdep/clock.go` + `sleeper.go` + their fakes,
  `internal/proxy/lifecycle.go:229-242` (readiness poll shape),
  `internal/proxy/lifecycle_test.go:378-394` (asserting Sleeps without wall-clock)
