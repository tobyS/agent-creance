# Status: AC-0010 — Rule model & matcher (WP-2.1)

Plan: `thoughts/shared/plans/2026-06-05-AC-0010-rule-model-matcher.md`

- [x] Phase 1 — Core types, matcher, unit tests
- [ ] Phase 2 — Decision-vector corpus + corpus-driven test

## Log

- Phase 1 done: `internal/policy` (policy.go, glob.go, match.go) + match_test.go.
  `go build ./...`, `go test -race ./internal/policy/...`, `make lint` all green.
