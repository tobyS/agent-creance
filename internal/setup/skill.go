package setup

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/tobyS/agent-creance/internal/setupcheck"
)

// skillMD is the Claude Code skill that teaches the agent the three network-refusal
// response types (allowed / soft-deny / hard-deny) and how to react to each. It
// activates on the X-Cage-Reason header and the agent_cage_ error prefix the proxy
// emits (internal/proxy/enforcer/responses.py); the content must track that wire
// format. setup installs it once; we deliberately never touch the project CLAUDE.md
// (design.md "executable config" — ~/.claude contents fire on the user's next
// un-caged Claude run, so the skill is host-side, user-scoped config).
//
//go:embed SKILL.md
var skillMD string

const (
	skillDirPerm  fs.FileMode = 0o755
	skillFilePerm fs.FileMode = 0o644
)

// InstallSkill writes the embedded skill to ~/.claude/skills/agent-creance/SKILL.md
// (the path setupcheck.SkillFileRel also checks), creating parent directories. It is
// idempotent: when the file already holds the embedded content it is left untouched,
// so re-running setup is a no-op. It only ever touches that one file — never the
// project's CLAUDE.md.
func (i *Installer) InstallSkill() error {
	home, err := i.paths.UserHomeDir()
	if err != nil {
		return fmt.Errorf("setup: resolve home dir: %w", err)
	}
	dest := filepath.Join(home, setupcheck.SkillFileRel)
	if err := i.fs.MkdirAll(filepath.Dir(dest), skillDirPerm); err != nil {
		return fmt.Errorf("setup: create skill dir: %w", err)
	}
	return i.writeSkillIfChanged(dest, []byte(skillMD))
}

// writeSkillIfChanged writes want to dest only when the current content differs (or
// dest is absent), atomically via a temp file + rename so a crash mid-write never
// leaves a torn skill file — the same idiom as proxy.Extractor.writeIfChanged. A
// genuine read error on an existing file is surfaced; a stale/corrupt file simply
// mismatches and is rewritten, so the install self-heals.
func (i *Installer) writeSkillIfChanged(dest string, want []byte) error {
	switch got, err := i.fs.ReadFile(dest); {
	case err == nil:
		if bytes.Equal(got, want) {
			return nil // already up to date
		}
	case errors.Is(err, fs.ErrNotExist):
		// first install — fall through to write
	default:
		return fmt.Errorf("setup: read skill %q: %w", dest, err)
	}

	tmp := dest + ".tmp"
	if err := i.fs.WriteFile(tmp, want, skillFilePerm); err != nil {
		return fmt.Errorf("setup: write %q: %w", tmp, err)
	}
	if err := i.fs.Rename(tmp, dest); err != nil {
		_ = i.fs.Remove(tmp) // best-effort cleanup of the orphaned temp file
		return fmt.Errorf("setup: commit %q: %w", dest, err)
	}
	return nil
}
