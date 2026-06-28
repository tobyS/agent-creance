---
date: 2026-06-28
git_commit: 9ffb7cb
branch: main
repository: agent-creance
ticket: AC-0065
topic: "AC-0065 — fix stale README, add Quickstart and an install path"
status: ready
tags: [plan, docs, readme, onboarding, install, makefile]
---

# AC-0065 — user-facing onboarding: README, Quickstart, install path

## Overview

Fix first contact with the project. Three concrete changes, all in two files:

1. **`Makefile`** — add an `install` target that builds the binary *with version
   stamping* and places it on `PATH` (via `go install -ldflags …` into
   `$(go env GOPATH)/bin`).
2. **`README.md`** — replace the stale, self-contradicting status banner and
   "(not yet wired up)" requirement with accurate text; add a self-contained
   `## Quickstart` (obtain → `setup` → `init` → `run`) and a `## Install` section
   documenting both install methods.

No Go source changes. This matches the ticket's Medium estimate: primarily docs
plus one small Makefile target.

## Decisions (from the question checkpoint)

- **Install methods: document both.** `go install …@latest` (zero-clone, version
  reports `dev`) and a new `make install` target (stamped, from source). They
  land in the same `$(go env GOPATH)/bin`, so they complement rather than
  compete. Both require `$(go env GOPATH)/bin` on `PATH` — the README states this.
- **Quickstart location: self-contained in the README**, with a pointer to
  `docs/design.md` for depth. `docs/design.md` is not rewritten (out of scope).

## Current State

- `README.md:9-12` — banner claims "only `version` and `doctor`" are implemented.
  False: 16 commands are fully implemented (`internal/cli/cli.go:119-134`),
  including `run` (`run.go:68-287` really launches the cage).
- `README.md:18` — "(not yet wired up)" qualifier on the cage requirements. False.
- `README.md` has **no** Quickstart / Getting Started / Install heading
  (verified by full read); the only "obtain the binary" content is the
  contributor-oriented `## Development` block (`README.md:104-113`,
  `make build` → `./bin/agent-creance`).
- `Makefile` has `build` (`Makefile:37-41`, ldflags into `./bin/`) and `run`, but
  **no `install`** target.
- Version stamping is ldflags-only (`internal/buildinfo/buildinfo.go:21-28`;
  `Makefile:8-18`); `VERSION = git describe --tags --always --dirty` → short SHA
  when untagged. Only `version` (`internal/cli/version.go:18-20`) consumes it;
  no `run`/`doctor` behavior depends on it.

## Desired End State

A new user landing on the README can, in one place:

1. Trust the status text — it no longer enumerates per-command status falsely.
2. Follow a Quickstart from obtaining the tool to a running caged session.
3. Install `agent-creance` onto `PATH` by at least one documented, working method
   (two are documented: `go install …@latest` and `make install`).

And: no README statement is contradicted by the code or by another part of the
README.

## What We're NOT Doing

- No Homebrew tap/formula (v1.0 roadmap, `docs/design.md:575`).
- No `--help`/`Long:`/`Example:`/grouping changes (AC-0064).
- No rewrite of `docs/design.md`.
- No `buildinfo` change to read `runtime/debug.ReadBuildInfo()` so `go install`
  reports a real version — noted in research as a possible future improvement,
  explicitly not required here (the `dev` line is cosmetic).
- No signed releases / release automation.

---

## Phase 1 — Add a `make install` target

### Changes

**File: `Makefile`** — add an `install` target after `build` (`Makefile:37-41`),
following the existing `## <name>: <desc>` help-comment convention so it shows in
`make help`:

```make
## install: build and install agent-creance onto PATH (into `go env GOPATH`/bin)
.PHONY: install
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/agent-creance
```

Rationale: `go install` (not `go build` + manual copy) reuses the existing
`$(LDFLAGS)` so the installed binary keeps version stamping, and it lands in the
standard `$(go env GOBIN)` / `$(go env GOPATH)/bin` location — the same place a
bare `go install …@latest` goes, with no `sudo` and no hardcoded `/usr/local/bin`.

### Success Criteria

#### Automated verification
- [ ] `make build` succeeds (sanity: Makefile still parses): `make build`
- [ ] Unit tests still green: `make test`
- [ ] Lint clean: `make lint`
- [ ] `make help` lists the new `install` target: `make help`

#### Manual verification
- [ ] `make install` completes and produces a runnable binary on `PATH`:
      run `make install`, then `"$(go env GOPATH)/bin/agent-creance" version`
      prints a version line (stamped short-SHA, not `dev`).

---

## Phase 2 — README: fix stale claims, add Quickstart + Install

### Changes (all in `README.md`)

1. **Status banner (`README.md:9-12`).** Replace the per-command enumeration with
   accurate text. New banner (keep the design.md pointer, drop the false
   "skeleton plus version and doctor" claim):

   > **Status:** early development (pre-v0.1), under active development. The v0.1
   > core is implemented — `setup`, `init`, and `run` work end to end, along with
   > the egress-policy commands (`allow`/`deny`/`policy`/`import`), `doctor`,
   > `status`, and `logs`. The full design lives in
   > [`docs/design.md`](docs/design.md).

   (Phrasing may be tightened during implementation; the constraint is: no
   false per-command "not implemented" claim, and no contradiction with the
   command sections below.)

2. **Requirements (`README.md:14-19`).** Remove the "(not yet wired up)"
   qualifier from the cage line:

   > - For running a cage: `agent-safehouse` and `mitmproxy` on `PATH`.

3. **Insert `## Quickstart` and `## Install`** immediately after `## Requirements`
   (before `## Egress baseline`). Distilled from `docs/design.md:406-475`:

   - **`## Quickstart`** — the ordered happy path, annotated with the host-once /
     project-once distinction (incidentally addresses audit S5):

     ```
     # 0. Prerequisites: Go 1.26+, plus agent-safehouse and mitmproxy on PATH
     #    (macOS only). Install agent-creance — see Install below.

     agent-creance setup          # once per machine: trust the mitmproxy CA,
                                  #   install the skill, scaffold the global config
     cd your-project
     agent-creance init           # once per project: write .agent-creance.yaml
     agent-creance run            # start the cage and your agent
     ```

     Plus 1–2 sentences: `setup` is per-machine, `init` per-project; `run`
     refuses with a pointer to `setup`/`init` if they haven't run. Pointer line:
     "For the full command reference, see [`docs/design.md`](docs/design.md)."

   - **`## Install`** — both methods, recommended-first, with the PATH note:

     ```sh
     # Quickest (no clone; requires `go env GOPATH`/bin on your PATH):
     go install github.com/tobyS/agent-creance/cmd/agent-creance@latest

     # From source (stamps the build version):
     git clone https://github.com/tobyS/agent-creance.git
     cd agent-creance
     make install
     ```

     One line noting both land in `$(go env GOPATH)/bin` — add it to `PATH` if it
     isn't already (`export PATH="$PATH:$(go env GOPATH)/bin"`). One line noting
     the `go install` binary reports its version as `dev` (cosmetic; the from-
     source `make install` stamps a real version).

4. **Light accuracy pass.** Research confirmed no other stale claims; while
   editing, re-scan the surrounding sections to ensure the new Quickstart doesn't
   contradict `## Egress baseline` / `## First-run config` / `## Shell completion`
   (it shouldn't — they describe individual features the Quickstart references).

### Notes for implementation

- Keep the existing `## Development` section (`README.md:104-131`) — it is the
  contributor build path (`make build` → `./bin/agent-creance`) and stays
  accurate; the new `## Install` is the *user* path. Optionally add a one-line
  cross-reference, but do not duplicate.
- Heading order after the edit: `# agent-creance` → banner → `## Requirements`
  → `## Quickstart` → `## Install` → `## Egress baseline` → `## First-run config`
  → `## Shell completion` → `## Development` → `## License`.

### Success Criteria

#### Automated verification
- [ ] Build still green (no code touched, but cheap sanity): `make build`
- [ ] Tests green: `make test`

#### Manual verification
- [ ] Banner no longer claims only `version`/`doctor` are implemented
      (AC-0065 criterion 1).
- [ ] "(not yet wired up)" is gone from `## Requirements` (criterion 2).
- [ ] README has a Quickstart listing obtain/install → `setup` → `init` → `run`
      and naming the `agent-safehouse` + `mitmproxy` prerequisites (criterion 3).
- [ ] At least one documented install method puts the binary on `PATH` and works
      — verified in Phase 1 for `make install` (criterion 4).
- [ ] Read the full README end to end: no statement contradicts the code or
      another section (criterion 5).

---

## Testing Strategy

This is a docs + Makefile-target change; there is no Go code path to unit-test.
Automated coverage is limited to "the repo still builds, tests pass, lint is
clean" (`make build` / `make test` / `make lint`) plus `make help` showing the
new target. The substantive verification is manual: run `make install` and invoke
the installed binary; read the README against the five acceptance criteria.

`go install …@latest` cannot be fully verified locally until the commit is pushed
(the public Go proxy resolves `@latest` from the pushed default branch). It is a
standard, well-understood invocation against a confirmed-public module path; the
README documents it, and `make install` (the from-source equivalent, same
`go install` mechanics minus the network fetch) is verified locally as the proxy
for its correctness.

## References

- Ticket: `thoughts/shared/tickets/AC-0065-onboarding-readme-quickstart-install.md`
- Research: `thoughts/shared/research/2026-06-28-AC-0065-onboarding-readme-quickstart-install.md`
- UX audit (S2/S3/S5): `thoughts/shared/research/2026-06-25-ux-audit.md`
- `docs/design.md:406-475` — command reference the Quickstart distills.
- `Makefile:8-18,37-41` — ldflags + existing `build` target.
- `internal/buildinfo/buildinfo.go:21-28` — version vars (ldflags-only).
