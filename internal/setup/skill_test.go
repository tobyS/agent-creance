package setup

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// skillPath is where InstallSkill writes, given testHome.
var skillPath = filepath.Join(testHome, ".claude", "skills", "agent-creance", "SKILL.md")

func TestInstallSkillWritesToExpectedPath(t *testing.T) {
	f := newFakes()

	if err := f.installer().InstallSkill(); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}

	got, ok := f.fs.Files[skillPath]
	if !ok {
		t.Fatalf("skill not written to %q; files: %v", skillPath, keys(f.fs.Files))
	}
	if string(got) != skillMD {
		t.Errorf("written content does not match embedded SKILL.md")
	}
	if perm := f.fs.Perms[skillPath]; perm != skillFilePerm {
		t.Errorf("file perm = %o, want %o", perm, skillFilePerm)
	}
	dir := filepath.Dir(skillPath)
	if !f.fs.Dirs[dir] {
		t.Errorf("skill dir %q not created", dir)
	}
	if perm := f.fs.Perms[dir]; perm != skillDirPerm {
		t.Errorf("dir perm = %o, want %o", perm, skillDirPerm)
	}
}

func TestInstallSkillIdempotentWhenPresent(t *testing.T) {
	f := newFakes()
	// Pre-seed the file with the exact embedded content; a re-install must not write.
	f.fs.Files[skillPath] = []byte(skillMD)
	f.fs.Perms[skillPath] = skillFilePerm
	sentinel := errors.New("boom: should not write")
	f.fs.WriteErrs[skillPath+".tmp"] = sentinel
	f.fs.RenameErrs[skillPath+".tmp"] = sentinel

	if err := f.installer().InstallSkill(); err != nil {
		t.Fatalf("InstallSkill on up-to-date file should be a no-op, got: %v", err)
	}
}

func TestInstallSkillRewritesOnDrift(t *testing.T) {
	f := newFakes()
	f.fs.Files[skillPath] = []byte("# stale content from an older version")

	if err := f.installer().InstallSkill(); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}

	if got := string(f.fs.Files[skillPath]); got != skillMD {
		t.Errorf("stale skill was not rewritten to the embedded content")
	}
}

// TestInstallSkillNeverTouchesClaudeMD enforces the ticket's hard guard: the
// installer must never read or write any CLAUDE.md path.
func TestInstallSkillNeverTouchesClaudeMD(t *testing.T) {
	f := newFakes()

	if err := f.installer().InstallSkill(); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}

	for path := range f.fs.Files {
		assertNotClaudeMD(t, path)
	}
	for path := range f.fs.Dirs {
		assertNotClaudeMD(t, path)
	}
	for path := range f.fs.Perms {
		assertNotClaudeMD(t, path)
	}
}

func assertNotClaudeMD(t *testing.T, path string) {
	t.Helper()
	if strings.Contains(path, "CLAUDE.md") {
		t.Errorf("installer touched a CLAUDE.md path: %q", path)
	}
}

// TestSkillContentMentionsTriggers asserts the embedded skill carries the markers
// the agent activates on: the four egress response types, the in-cage
// authentication-failure case (AC-0045), and the body-blind fetch case (AC-0049).
func TestSkillContentMentionsTriggers(t *testing.T) {
	for _, marker := range []string{
		"X-Cage-Reason",
		"soft-deny",
		"hard-deny",
		"injection-unavailable",
		"agent_cage_not_allowlisted",
		"agent_cage_hard_deny",
		"agent_cage_injection_unavailable",
		"X-Cage-Injected",
		"Failed to start OAuth callback server",
		"log in",
		"on the host",
		"restart the caged session",
		"WebFetch",
		"response body was not retrieved",
		"Do NOT try mirrors",
		"470",
		"471",
		"472",
	} {
		if !strings.Contains(skillMD, marker) {
			t.Errorf("embedded SKILL.md is missing required marker %q", marker)
		}
	}
}

// TestSkillAuthTriggersInFrontmatter asserts the auth-failure activation language
// lives in the frontmatter description (what the agent matches on), not only in
// the body (AC-0045).
func TestSkillAuthTriggersInFrontmatter(t *testing.T) {
	end := strings.Index(skillMD[3:], "---")
	if !strings.HasPrefix(skillMD, "---") || end < 0 {
		t.Fatal("embedded SKILL.md has no frontmatter block")
	}
	frontmatter := skillMD[:end+3]
	for _, marker := range []string{
		"Failed to start OAuth callback server",
		"login/onboarding prompt",
	} {
		if !strings.Contains(frontmatter, marker) {
			t.Errorf("frontmatter description is missing auth trigger %q", marker)
		}
	}
}

// TestSkillWebFetchTriggerInFrontmatter asserts the body-blind fetch activation
// language lives in the frontmatter description (what the agent matches on), not
// only in the body (AC-0049). WebFetch discards body and headers of non-2xx
// responses, so the description must trigger on what the model actually sees.
func TestSkillWebFetchTriggerInFrontmatter(t *testing.T) {
	end := strings.Index(skillMD[3:], "---")
	if !strings.HasPrefix(skillMD, "---") || end < 0 {
		t.Fatal("embedded SKILL.md has no frontmatter block")
	}
	frontmatter := skillMD[:end+3]
	for _, marker := range []string{
		"WebFetch",
		"response body was not retrieved",
	} {
		if !strings.Contains(frontmatter, marker) {
			t.Errorf("frontmatter description is missing body-blind fetch trigger %q", marker)
		}
	}
}

func TestInstallSkillSurfacesErrors(t *testing.T) {
	t.Run("mkdir fails", func(t *testing.T) {
		f := newFakes()
		boom := errors.New("mkdir boom")
		f.fs.MkdirErrs[filepath.Dir(skillPath)] = boom
		if err := f.installer().InstallSkill(); !errors.Is(err, boom) {
			t.Fatalf("InstallSkill error = %v, want wrapping %v", err, boom)
		}
	})
	t.Run("write fails", func(t *testing.T) {
		f := newFakes()
		boom := errors.New("write boom")
		f.fs.WriteErrs[skillPath+".tmp"] = boom
		if err := f.installer().InstallSkill(); !errors.Is(err, boom) {
			t.Fatalf("InstallSkill error = %v, want wrapping %v", err, boom)
		}
	})
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
