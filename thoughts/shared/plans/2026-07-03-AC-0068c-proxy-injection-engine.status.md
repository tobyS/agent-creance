# AC-0068c — implementation status

Plan: `thoughts/shared/plans/2026-07-03-AC-0068c-proxy-injection-engine.md`

## Phases

- [x] Phase 1 — ProcessManager seam: inherited-fd secret delivery (in-cage) — done 2026-07-03
- [x] Phase 2 — Resolve-at-spawn + delivery wiring, Go (in-cage) — done 2026-07-03
- [x] Phase 3 — Python: read new policy fields + port value-template (in-cage) — done 2026-07-03
- [x] Phase 4 — Python: secret intake, injection/overwrite/in-cage/472, annotation (in-cage) — done 2026-07-03
- [x] Phase 5 — Docs + status-code surface (472 + X-Cage-Injected) (in-cage) — done 2026-07-03 (verify-battery 472 vector deferred to Phase 6, needs a live proxy)
- [x] Phase 6 — Integration tests (3 enforcer injection e2e) — RUN out-of-cage: passed 2026-07-03

## Notes

Started 2026-07-03. Phases 1-5 are the hermetic in-cage gate (`make test`,
`make test-enforcer`, `make lint`, `make golden`, `make build`) — all green. Phase 6
ran out-of-cage: `make test-enforcer-integration` = 15/15 (incl. the 3 new injection
e2e tests against a real mitmdump fed a secret over a real fd). `make test-integration`
showed only the pre-existing `kc-read`/`kc-write` battery failures (verified identical
at the pre-code commit 392d963 — unrelated to AC-0068c). Ticket → Done.
