# AC-0035 implementation status

Plan: `2026-06-09-AC-0035-incage-claude-config-dir-writable.md`

## Phase 1 — Reproduce the gap in the battery (RED, WP)
- [x] Relocated `runBattery` cache to a non-granted `$HOME` temp dir (AC-0035 guard)
- [x] Reused the already-present `home` fetch (no duplicate declaration)
- [x] `go vet -tags=integration ./internal/verify/` passes (compiles)
- [x] `make test` (fast) green
- [x] `make test-integration` reproduces RED: `doc-config-dir want=planted got=blocked`,
      all 17 other vectors PASS (empirical proof of the gap at a non-granted cache loc)
- [ ] Commit RED

## Phase 2 — Mount CLAUDE_CONFIG_DIR read-write (GREEN)
- [ ] `cage.Build` always emits `--add-dirs` incl. `ClaudeConfigDir()`
- [ ] Update `TestExpandPathViaArgs`; add `TestBuildAlwaysMountsConfigDir`
- [ ] `make golden` (review single-segment diff)
- [ ] `make test` + `make lint` green; integration battery GREEN (or skip noted)
- [ ] Commit GREEN

## Phase 3 — Docs & close
- [ ] cage-verification.md limitation #2 rewritten
- [ ] design.md light note
- [ ] Ticket ACs ticked, Status: Done
- [ ] Commit docs/close
