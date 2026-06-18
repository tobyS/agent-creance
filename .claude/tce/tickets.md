<!-- tce-config-version: 1.0.0 -->
# Ticket System

> Read by the tce workflow commands at runtime to work with this project's
> ticket system. `/tce:init` seeds this file and fills in the backend sections;
> keep it accurate. If the ticket system or how you access it changes, update
> this file (or re-run `/tce:init` / `/tce:refresh`).

## System

**tmt** (Toby Markdown Tickets) — file-backed tickets living in the repo under
`thoughts/shared/tickets/`, one markdown file per ticket. Configured by
`.claude/tmt/config` (`TICKET_PREFIX=AC`). Managed via the `tmt` plugin commands
(`/tmt:create`, `/tmt:list`, `/tmt:update`).

## Canonical ticket ID

`AC-NNNN` — the prefix `AC` plus a zero-padded 4-digit number (e.g. `AC-0056`).
Sub-tickets append a lowercase letter (`AC-0056a`). This form is used verbatim in
`thoughts/` filenames and in commit scopes (`feat(AC-0056): …`). Normalize other
references into it: a bare number `56` or `#56` → `AC-0056`; a filename or path
→ extract the leading `AC-NNNN`.

## Reading a ticket

The ticket file is `thoughts/shared/tickets/AC-NNNN-*.md` (one match per ID) —
read it directly. To find **all** thoughts docs for a ticket (ticket + research +
plan + review), run the tce helper:

```
"${CLAUDE_PLUGIN_ROOT}/scripts/ticket.sh" AC-NNNN
```

(globs `thoughts/` for the ID; ships in the `tce` plugin). Research lives in
`thoughts/shared/research/`, plans in `thoughts/shared/plans/`, reviews in
`thoughts/shared/reviews/`, each named `<date>-AC-NNNN-*.md`.

## Parent / epic tickets

tmt has no formal parent/epic field. Relationships are expressed informally in the
ticket body (e.g. a `## References` section listing "Related: AC-0051, AC-0023").
When present, follow those IDs and read them for context; otherwise treat the
ticket as standalone.

## Creating a ticket

Allowed. Determine the next ID with the tmt helper, then write the file:

```
"${CLAUDE_PLUGIN_ROOT}/scripts/next-ticket.sh"
```

(scans `thoughts/shared/tickets/` for the highest `AC-NNNN` and returns the next;
ships in the `tmt` plugin). Write `thoughts/shared/tickets/AC-NNNN-brief-slug.md`
with the layout below; a new ticket starts at status **Open**. `/tce:ticket`
(interactive) and `/tce:quickfix` (autonomous) both create tickets this way; the
`/tmt:create` command wraps the same envelope logic.

## Ticket title & body layout

Markdown file. First line is the heading `# AC-NNNN: <Title>`, followed by the
meta block, then the body:

```
# AC-NNNN: <Title>

**Status:** Open
**Estimated Complexity:** <Low|Medium|High>
**Created:** <YYYY-MM-DD>
**Updated:** <YYYY-MM-DD>

<body — Problem Statement / Desired Outcome / Acceptance Criteria / Out of Scope /
Open Questions / References / Notes & Updates, etc.>
```

The author-facing body *structure* is owned by `/tce:ticket`; this section only
fixes where the title and meta lines go.

## Status / completion

tce performs the transition itself by editing the `**Status:**` line in the
ticket file (and bumping `**Updated:**` to the current date). Valid statuses:
**Open**, **In Progress**, **Done**, **Rejected**. Lifecycle moments:

- **start** — implementation begins → set `**Status:** In Progress`;
- **complete** — all phases done and verified → set `**Status:** Done`;
- **reject** — work abandoned / won't-fix → set `**Status:** Rejected`.

`/tmt:update` wraps the same edit (and validates the enum). Append a dated note
under `## Notes & Updates` describing the transition where the ticket uses that
section.

## What tce needs from a ticket

<!-- Backend-independent — applies to every ticket system. Keep as-is; this is
     also what /tce:ticket and human ticket authors should aim for. -->

tce works from any ticket that provides, at minimum:

- **Clear scope** — what should change or be built, and roughly where the
  boundary is (what is explicitly not part of it, if anything).
- **Observable outcome** — you can tell from the ticket what "done" would look
  like, even informally.
- **An anchor** — at least one concrete pointer into the system (a feature,
  screen, command, error message, or code area) so research has somewhere to
  start.

Not required: business justification, formal acceptance criteria, technical
detail, or any particular section structure.

If a ticket additionally contains these, the tce commands exploit them
directly:

- **Open Questions** — resolved with the user before planning.
- **Questions for Research/Planning** — guide the research phase.
- **Acceptance Criteria** — used as the review checklist by `/tce:review`.

Tickets missing the minimum trigger an upfront clarification round in
`/tce:research` (and `/tce:work`) before any research starts.
