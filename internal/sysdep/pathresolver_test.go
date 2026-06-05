package sysdep

import (
	"os"
	"path/filepath"
	"testing"
)

// These smoke tests cover the real OSPathResolver against the actual filesystem
// and environment. They live here (not in internal/state) because internal/state
// is barred from importing "os" by its grep guard; the real impl's correctness is
// verified once, here, while internal/state tests use the in-memory fake.

func TestOSPathResolverEvalSymlinksCollapsesAlias(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	var r OSPathResolver
	viaTarget, err := r.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("EvalSymlinks(target): %v", err)
	}
	viaLink, err := r.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("EvalSymlinks(link): %v", err)
	}
	if viaTarget != viaLink {
		t.Errorf("alias did not collapse: target=%q link=%q", viaTarget, viaLink)
	}
}

func TestOSPathResolverEvalSymlinksMissingErrors(t *testing.T) {
	var r OSPathResolver
	if _, err := r.EvalSymlinks(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("EvalSymlinks of a missing path: want error, got nil")
	}
}

func TestOSPathResolverAbsMakesRelativeAbsolute(t *testing.T) {
	var r OSPathResolver
	got, err := r.Abs("some/rel/path")
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("Abs(%q) = %q, want absolute", "some/rel/path", got)
	}
}

func TestOSPathResolverHomeAndGetenvDelegate(t *testing.T) {
	var r OSPathResolver

	home, err := r.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if want, _ := os.UserHomeDir(); home != want {
		t.Errorf("UserHomeDir() = %q, want %q", home, want)
	}

	t.Setenv("CREANCE_SMOKE_VAR", "value-123")
	if got := r.Getenv("CREANCE_SMOKE_VAR"); got != "value-123" {
		t.Errorf("Getenv() = %q, want %q", got, "value-123")
	}
	if got := r.Getenv("CREANCE_DEFINITELY_UNSET_VAR"); got != "" {
		t.Errorf("Getenv(unset) = %q, want empty", got)
	}
}
