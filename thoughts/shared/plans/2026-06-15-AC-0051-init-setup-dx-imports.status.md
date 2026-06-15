---
plan: thoughts/shared/plans/2026-06-15-AC-0051-init-setup-dx-imports.md
ticket: AC-0051
updated: 2026-06-15
---

# Status — AC-0051 first-run DX imports

- [x] Phase 1 — Claude Code config readers (`internal/claudeimport`) — b66258f
- [x] Phase 2 — Static dev-port detection (`internal/portscan`) — c6f8bfe
- [x] Phase 3 — Shared YAML renderers + `host_services` splice — 7c2988a
- [x] Phase 4 — `agent-creance import <file>` command — d12700f
- [x] Phase 5 — `init` integration (gates + review + agent prompt) — 8ec9a06
- [x] Phase 6 — `setup` global seeding + docs — 4994f89

All phases complete. `make test`, `make lint`, `make build` green. Ticket Done.
End-to-end smoke test passed (non-interactive init scaffold, import merge,
strict reject, idempotent re-import).

## Notes
- Phase 1: `internal/claudeimport` reads settings/.mcp.json/~/.claude.json →
  WebRules (GET intercept), MCPRules (passthrough), Ports (localhost MCP).
