---
plan: thoughts/shared/plans/2026-06-05-AC-0008-config-include-merge.md
ticket: AC-0008
started: 2026-06-05
---

# AC-0008 — Implementation status

## Phase 1 — sysdep.FileSystem read seam
Status: done (commit pending)

- internal/sysdep/filesystem.go: FileSystem{ReadFile} interface + OSFileSystem +
  compile-time `var _ FileSystem` assertion. Narrow (ReadFile only); AC-0009 grows it.
- internal/sysdep/sysdeptest/filesystem.go: FakeFileSystem (Files/Errs maps,
  NewFakeFileSystem); unknown path → fs.ErrNotExist.
- filesystem_test.go: OSFileSystem round-trip + missing→fs.ErrNotExist smoke tests.
- go build / go vet / go test -race ./internal/sysdep all green; grep guard passes.

## Phase 2 — Merge semantics (merge.go)
Status: done (commit pending)

- internal/config/merge.go: pure merge(base, over) per the agreed table — scalar/
  command override (firstNonEmpty*), list union+dedupe (dedupeStrings/HostServices/
  Rules), env key-wise merge. dedupeRules uses reflect.DeepEqual (pointer-aware for
  *[]string Paths/Methods). Empty results normalise to nil for DeepEqual stability.
- merge_test.go: scalar override, command replace (not concat), string-list & rule
  union+dedupe, pointer-aware dedupe (nil≠empty Paths), env over-wins, determinism.
- go test -race ./internal/config + go vet green.

## Phase 3 — Loader: include resolution, cycle detection, depth limit
Status: in progress
</content>
