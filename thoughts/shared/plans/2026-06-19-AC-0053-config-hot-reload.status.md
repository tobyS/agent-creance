# AC-0053 Implementation Status

Plan: `thoughts/shared/plans/2026-06-19-AC-0053-config-hot-reload.md`

- [x] Phase 1: Loader include-graph enumeration (`ResolveFiles`)
- [ ] Phase 2: `FileWatcher` sysdep seam + fake
- [ ] Phase 3: `internal/configwatch` package
- [ ] Phase 4: wire into run session
- [ ] Phase 5: integration test + docs + build

## Notes

### Phase 1 (done)

- Added `Loader.ResolveFiles` + `collectFiles` in `internal/config/load.go`:
  returns the canonical, deduped file set (global + project + transitive
  includes), mirroring `resolve`'s walk (cycle detection, depth limit, include
  path rules). 10 table-driven tests in `load_test.go`. `make test`/`make lint`
  green.
</content>
