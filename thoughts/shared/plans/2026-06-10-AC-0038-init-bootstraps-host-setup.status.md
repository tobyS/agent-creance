# AC-0038 implementation status

Plan: 2026-06-10-AC-0038-init-bootstraps-host-setup.md

- [x] **Phase 1 — Seams.** Added `sysdep.Terminal` (`terminal.go`) + `OSTerminal`
  (unix.IoctlGetTermios), `sysdeptest.FakeTerminal`, `App.Stdin`/`App.Terminal`
  wired in `Main`, and the `confirm` helper. `go build` + production lint clean.
- [x] **Phase 2 — init behavior.** `--no-setup` flag; `runInit` reordered
  (gate-first); `ensureHostSetup` reusing `runSetup`; `msgInitNeedsSetup` /
  `msgNoSetupHint`; conditional success line. `go build` clean.
- [x] **Phase 3 — Tests + verification.** Extended `init_test.go` fixture
  (already-set-up default + `setupMissing`/`withStdin` helpers), updated existing
  call sites to the new signature, added 7 bootstrap cases (already-set-up,
  confirm, decline, setup-failure, keychain-locked, non-interactive-abort,
  `--no-setup`); rewrote `init.txtar` to `--no-setup` + `--no-setup` help
  assertion. `make test` (race), `make lint`, `go build`, `make golden` (no diff)
  all green.

All acceptance criteria met. Phases 1–3 committed together (the gate makes
`make test` red until the tests are updated, so they land in one commit).
