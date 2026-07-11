# AC-0068e Implementation Status

**Plan**: `thoughts/shared/plans/2026-07-10-AC-0068e-github-flagship.md`
**Base commit**: `6621078030ae2346fa7fee8e6acb4ddd38f6fe62`
**Started**: 2026-07-10

## Phases

- [x] Phase 1 — `docs/design.md` credential-injection section — `2bd931a`, revised risk-first in Phase 5
- [x] Phase 2 — Go real-GitHub integration test — `57b19ca`
- [x] Phase 3 — Python concurrent-proxy scoping test — `5370da1`
- [~] Phase 4 — dogfooding config: **dropped** (see the 2026-07-11 security finding)
- [x] Phase 5 — out-of-cage validation batch — done; results below

## 2026-07-11 — security finding redirected the ticket

Out-of-cage validation confirmed the injection path works against real GitHub
(`gh` matrix: `POST /graphql -> 200`, phantom overwritten; Go test 3/3; enforcer
16/16), but probing the token exposed that a repo-scoped PAT does **not** scope
*public* reads. Opening `/graphql` therefore lets the agent read any public repo —
the ingestion vector the cage exists to close. Agreed with the user: keep the
mechanism, make `/graphql` an opt-in risk, default to scoped REST, and document both
in `docs/design.md` + `README.md`.

Consequences:
- **Phase 4 dropped.** The dogfood `.agent-creance.yaml` was reverted to clean — this
  project uses tmt (not GitHub issues) so needs no injection, and a personal `op://`
  ref in a public repo would 472 other contributors.
- **Phase 1 docs revised.** The design section was rewritten risk-first (the hard
  limit on public reads, the safe scoped-REST posture, the `/graphql` opt-in), and a
  `README.md` "Using GitHub in the cage" section added.

## Deviations from the plan

**Phase 4 cannot run in-cage.** The plan sequenced it as the last in-cage step. In fact
the cage renders `(deny file-write* (literal "<project>/.agent-creance.yaml"))`
(`internal/profile/profile.go`, `RenderConfigReadOnlyFragment`), so a caged agent may not
rewrite its own egress policy — the AC-0059/AC-0060 integrity property. The edit fails
with `EPERM`. Phase 4 has been folded into the front of the Phase 5 breakout, which also
keeps the cage down for exactly one window.

**Phase 3 found and fixed a harness bug.** `_curl` / `_curl_stream` passed `-x` but let
curl fall back to the ambient `NO_PROXY` bypass list. In a cage that is
`NO_PROXY=localhost,127.0.0.1,::1`, so every request to the 127.0.0.1 echo origin skipped
the proxy under test: the three AC-0068c injection assertions saw an empty `Authorization`
header and the streaming test saw no audit entry. Passing `--noproxy ""` fixes it. As a
side effect, 11 of the 16 enforcer integration tests (all the local-origin ones,
including the new concurrency test) now run green **in-cage**; only the 5 needing real
external egress still require the cage down.

## Verification so far

- `make test` — green
- `make lint` — green
- `go vet -tags=integration ./...` — clean
- `make test-enforcer` — 148 passed
- `pytest -m integration -k "streams or inject or concurrent or hot_reload or deny or reason"`
  — 11 passed, in-cage
- `go test -tags=integration ./internal/proxy/ -run TestInjectGitHubGraphQLRealUpstream`
  — skips cleanly without `AC_TEST_GITHUB_TOKEN_REF`

## Remaining (out-of-cage batch)

1. Apply Phase 4's `.agent-creance.yaml` edit.
2. Confirm `op://Personal/fjv2nwlg4tdjpzfafo5ditavxa/token` holds a **fine-grained** PAT
   scoped to `tobyS/agent-creance` with Metadata: Read + Issues: Read and write +
   Contents: Read.
3. `AC_TEST_GITHUB_TOKEN_REF='op://Personal/fjv2nwlg4tdjpzfafo5ditavxa/token' make test-integration`
4. `make build`, then the real `gh` matrix and adversarial checks in a fresh cage.
5. Mark the ticket Done once the plan-compliance gate passes.
