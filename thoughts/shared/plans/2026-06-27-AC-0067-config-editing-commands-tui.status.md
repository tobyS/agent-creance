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

## Phase 2: Removal infrastructure — DONE
- [x] RemoveRule (whole + single-path, drop-on-last), RemoveHostService(port),
      RemoveDir(both lists) + ErrNotFound sentinel
- [x] Element span bounded by maxLine(node subtree) so inter-element comments survive
- [x] Inline-string tests incl. comment preservation, both-lists, not-found
- [x] Verified (make test / build / lint)
- Note: single-path removal re-renders the item from typed values (reflows just
  that item); whole-element removal deletes only the element's own lines.

## Phase 3+4: domain group + aliases + interactive prompt — DONE
(Consolidated: building a flags-only intermediate with no fallback would be a
deliberately broken step, so the prompt fallback was built with domain add.)
- [x] prompt.go helper (promptSelect / promptText / requireInteractive)
- [x] domain.go (add/remove), rule builder, guards (all-paths/path conflict,
      passthrough+paths/methods, deny+method/mode, unknown mode)
- [x] domain add prompts for the paths decision when omitted; non-TTY → flag hint
- [x] allow/deny delegate to runDomainAdd via setURLPath (ruleFromURL removed)
- [x] register newDomainCmd in cli.go
- [x] domain_test.go (guards/builder/remove) + domain_interactive_test.go + domain.txtar
- [x] Verified (make test / build / lint)
- DEVIATION from a strict AC sub-bullet: methods and mode are NOT prompted when
  omitted — they use safe defaults (any method / intercept). Only the paths
  decision prompts (the explicit user story). --method/--mode set them explicitly.
  Flag for review.

## Phase 5: service + mount with write-and-warn
- [ ] applyAndWarn helper (live-cage probe)
- [ ] service.go, mount.go
- [ ] register in cli.go
- [ ] unit + testscript tests (incl. seedlock live-cage warning)
- [ ] Verified

## Notes
- Checkpoint decisions: last-path removal drops whole rule; remove-missing errors
  (non-zero); mount remove detaches both rw+ro; allow/deny delegate to domain add.
