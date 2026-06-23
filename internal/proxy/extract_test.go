package proxy

import (
	"bytes"
	"errors"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const testEnforcerRoot = "/cache/agent-creance/enforcer"

// newExtractor wires an Extractor over fresh fakes with XDG_CACHE_HOME=/cache,
// so EnforcerRoot resolves deterministically to testEnforcerRoot.
func newExtractor(fs *sysdeptest.FakeFileSystem) *Extractor {
	paths := sysdeptest.NewFakePathResolver()
	paths.Env["XDG_CACHE_HOME"] = "/cache"
	return NewExtractor(fs, paths)
}

// embedded returns the embedded bytes of a module, failing the test if absent.
func embedded(t *testing.T, name string) []byte {
	t.Helper()
	b, err := enforcerFS.ReadFile(path.Join(embedDir, name))
	if err != nil {
		t.Fatalf("embedded %q: %v", name, err)
	}
	return b
}

func TestEmbedContainsAllRuntimeModules(t *testing.T) {
	for _, name := range enforcerModules {
		if b := embedded(t, name); len(b) == 0 {
			t.Errorf("embedded module %q is empty", name)
		}
	}
	// enforcer.py must keep importing its three siblings; if someone trims the
	// module set, this guards against shipping an addon that ImportErrors at load.
	src := string(embedded(t, "enforcer.py"))
	for _, imp := range []string{"import audit", "import policy", "import responses"} {
		if !strings.Contains(src, imp) {
			t.Errorf("enforcer.py missing %q — embedded module set is incomplete", imp)
		}
	}
	// Dev-only files must NOT be embedded.
	for _, name := range []string{"test_enforcer.py", "conftest.py", "requirements.txt"} {
		if _, err := enforcerFS.ReadFile(path.Join(embedDir, name)); err == nil {
			t.Errorf("dev-only file %q should not be embedded", name)
		}
	}
}

func TestExtractFirstRunWritesAllModules(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	got, err := newExtractor(fs).Extract()
	if err != nil {
		t.Fatal(err)
	}

	wantEntry := filepath.Join(testEnforcerRoot, entrypointName)
	if got != wantEntry {
		t.Errorf("Extract returned %q, want %q", got, wantEntry)
	}
	if !fs.Dirs[testEnforcerRoot] {
		t.Errorf("enforcer root %q was not created", testEnforcerRoot)
	}
	if perm := fs.Perms[testEnforcerRoot]; perm != dirPerm {
		t.Errorf("dir perm = %o, want %o", perm, dirPerm)
	}
	for _, name := range enforcerModules {
		dest := filepath.Join(testEnforcerRoot, name)
		if !bytes.Equal(fs.Files[dest], embedded(t, name)) {
			t.Errorf("extracted %q does not match embedded bytes", name)
		}
		if perm := fs.Perms[dest]; perm != filePerm {
			t.Errorf("file perm for %q = %o, want %o", name, perm, filePerm)
		}
		// No temp file should be left behind after a successful rename.
		if _, ok := fs.Files[dest+tmpSuffix]; ok {
			t.Errorf("temp file for %q was not cleaned up", name)
		}
	}
}

func TestExtractIsIdempotent(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	// Pre-seed every module with the exact embedded bytes, then arm the write and
	// rename paths to fail loudly: a true no-op never touches them, so Extract
	// must still succeed.
	for _, name := range enforcerModules {
		dest := filepath.Join(testEnforcerRoot, name)
		fs.Files[dest] = append([]byte(nil), embedded(t, name)...)
		fs.WriteErrs[dest+tmpSuffix] = errors.New("write must not happen")
		fs.RenameErrs[dest+tmpSuffix] = errors.New("rename must not happen")
	}

	if _, err := newExtractor(fs).Extract(); err != nil {
		t.Fatalf("idempotent re-run should not write, got error: %v", err)
	}
}

func TestExtractRefreshesChangedModule(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	stale := filepath.Join(testEnforcerRoot, "policy.py")
	fs.Files[stale] = []byte("# stale content from an older binary\n")

	if _, err := newExtractor(fs).Extract(); err != nil {
		t.Fatal(err)
	}
	for _, name := range enforcerModules {
		dest := filepath.Join(testEnforcerRoot, name)
		if !bytes.Equal(fs.Files[dest], embedded(t, name)) {
			t.Errorf("module %q not refreshed to embedded bytes", name)
		}
	}
}

func TestExtractSelfHealsMissingModule(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	// Seed all but one module with correct bytes; the missing one must be written.
	for _, name := range enforcerModules[:len(enforcerModules)-1] {
		dest := filepath.Join(testEnforcerRoot, name)
		fs.Files[dest] = append([]byte(nil), embedded(t, name)...)
	}
	missing := enforcerModules[len(enforcerModules)-1]

	if _, err := newExtractor(fs).Extract(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(testEnforcerRoot, missing)
	if !bytes.Equal(fs.Files[dest], embedded(t, missing)) {
		t.Errorf("missing module %q was not written", missing)
	}
}

func TestExtractC4StaysUnderEnforcerRoot(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	if _, err := newExtractor(fs).Extract(); err != nil {
		t.Fatal(err)
	}
	prefix := testEnforcerRoot + string(filepath.Separator)
	for p := range fs.Files {
		if p != testEnforcerRoot && !strings.HasPrefix(p, prefix) {
			t.Errorf("wrote file outside enforcer root: %q", p)
		}
	}
	for p := range fs.Dirs {
		// MkdirAll faithfully records the enforcer root's ancestors (e.g. /cache),
		// just as os.MkdirAll creates them; those are expected. Only a dir that is
		// neither the root, under it, nor an ancestor of it would be an escape.
		ancestor := strings.HasPrefix(testEnforcerRoot, p+string(filepath.Separator))
		if p != testEnforcerRoot && !strings.HasPrefix(p, prefix) && !ancestor {
			t.Errorf("created dir outside enforcer root: %q", p)
		}
	}
}

func TestExtractMkdirError(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	fs.MkdirErrs[testEnforcerRoot] = errors.New("mkdir boom")
	if _, err := newExtractor(fs).Extract(); err == nil {
		t.Error("want error when the enforcer dir cannot be created, got nil")
	}
}

func TestExtractWriteError(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	fs.WriteErrs[filepath.Join(testEnforcerRoot, "enforcer.py")+tmpSuffix] = errors.New("write boom")
	if _, err := newExtractor(fs).Extract(); err == nil {
		t.Error("want error when a module cannot be written, got nil")
	}
}

func TestExtractRenameErrorCleansUpTemp(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	dest := filepath.Join(testEnforcerRoot, "enforcer.py")
	fs.RenameErrs[dest+tmpSuffix] = errors.New("rename boom")

	if _, err := newExtractor(fs).Extract(); err == nil {
		t.Fatal("want error when the rename fails, got nil")
	}
	if _, ok := fs.Files[dest]; ok {
		t.Error("final file should not exist after a failed rename")
	}
	if _, ok := fs.Files[dest+tmpSuffix]; ok {
		t.Error("temp file should have been cleaned up after a failed rename")
	}
}

func TestExtractReadErrorSurfaced(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	dest := filepath.Join(testEnforcerRoot, "enforcer.py")
	fs.Errs[dest] = errors.New("read boom") // exists but unreadable
	if _, err := newExtractor(fs).Extract(); err == nil {
		t.Error("want error when an extracted module cannot be read, got nil")
	}
}

func TestExtractCacheRootError(t *testing.T) {
	fs := sysdeptest.NewFakeFileSystem()
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeErr = errors.New("no home") // and XDG_CACHE_HOME unset
	if _, err := NewExtractor(fs, paths).Extract(); err == nil {
		t.Error("want error when the cache root cannot be determined, got nil")
	}
}
