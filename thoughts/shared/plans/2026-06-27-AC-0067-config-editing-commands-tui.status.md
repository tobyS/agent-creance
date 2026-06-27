---
plan: thoughts/shared/plans/2026-06-27-AC-0067-config-editing-commands-tui.md
ticket: AC-0067
status: in-progress
---

# Status: AC-0067 — config-editing commands with TUI fallback

## Phase 1: Config-layer add for safehouse.add_dirs_* — DONE
- [x] AppendDir + helpers + validate gate (internal/config/edit_safehouse.go)
- [x] Inline-string tests (flow/block/synthesize/dup/comment-preservation)
- [x] Verified (make test / build / lint)
- Note: flow lists rewritten in place (reconstruct from typed values); inline
  LineComment carried over; block lists get an appended item. Slashed paths are
  quoted via scalar(), consistent with `include`.

## Phase 2: Removal infrastructure
- [ ] endOfItem primitive + RemoveRule/RemoveHostService/RemoveDir + ErrNotFound
- [ ] Table + golden tests, fixtures
- [ ] Verified

## Phase 3: domain group + allow/deny aliases (flags)
- [ ] domain.go (add/remove), rule builder, guards
- [ ] allow/deny delegate to shared body
- [ ] register in cli.go
- [ ] unit + testscript tests
- [ ] Verified

## Phase 4: interactive prompt helper + domain add fallback
- [ ] prompt.go helper (select/text/require-interactive)
- [ ] domain add prompts for omitted choices
- [ ] interactive unit tests + non-TTY testscript
- [ ] Verified

## Phase 5: service + mount with write-and-warn
- [ ] applyAndWarn helper (live-cage probe)
- [ ] service.go, mount.go
- [ ] register in cli.go
- [ ] unit + testscript tests (incl. seedlock live-cage warning)
- [ ] Verified

## Notes
- Checkpoint decisions: last-path removal drops whole rule; remove-missing errors
  (non-zero); mount remove detaches both rw+ro; allow/deny delegate to domain add.
