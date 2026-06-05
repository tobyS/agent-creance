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
Status: in progress

## Phase 3 — Loader: include resolution, cycle detection, depth limit
Status: not started
</content>
