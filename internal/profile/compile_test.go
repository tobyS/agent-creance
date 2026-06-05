package profile

import (
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const (
	testHome = "/home/toby"
	projDir  = "/proj"
)

// compileFixture wires a Compiler (via the real New) over in-memory fakes, seeding the
// project config file. It returns the compiler and the filesystem (to assert on writes).
func compileFixture(t *testing.T, projectYAML string) (*Compiler, *sysdeptest.FakeFileSystem) {
	t.Helper()
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Files[projDir+"/.agent-creance.yaml"] = []byte(projectYAML)
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	return New(fsys, paths), fsys
}

func TestCompile_WritesNetworkSB(t *testing.T) {
	yaml := "network:\n  host_services:\n    - mysql:3306\n    - redis:6379\n"
	c, fsys := compileFixture(t, yaml)

	res, err := c.Compile(projDir)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.AllowCount != 2 {
		t.Errorf("AllowCount = %d, want 2", res.AllowCount)
	}

	got := string(fsys.Files[res.ProfilePath])
	want := RenderNetworkSB([]config.HostService{{Label: "mysql", Port: 3306}, {Label: "redis", Port: 6379}})
	if got != want {
		t.Errorf("network.sb mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if perm := fsys.Perms[res.ProfilePath]; perm != filePerm {
		t.Errorf("network.sb perm = %o, want %o", perm, filePerm)
	}
}

// TestCompile_C4_NoInTreeWrite mirrors the policy compiler's C4 guard: the artifact
// lands under the out-of-tree state root and nothing is written inside the project tree.
func TestCompile_C4_NoInTreeWrite(t *testing.T) {
	yaml := "network:\n  host_services:\n    - mysql:3306\n"
	c, fsys := compileFixture(t, yaml)

	res, err := c.Compile(projDir)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !strings.HasPrefix(res.ProfilePath, testHome) {
		t.Errorf("ProfilePath %q is not under the out-of-tree state root %q", res.ProfilePath, testHome)
	}

	seeded := map[string]bool{projDir + "/.agent-creance.yaml": true}
	for path := range fsys.Files {
		if strings.HasPrefix(path, projDir+"/") && !seeded[path] {
			t.Errorf("compile wrote inside the project tree: %q", path)
		}
	}
	for dir := range fsys.Dirs {
		if dir == projDir || strings.HasPrefix(dir, projDir+"/") {
			t.Errorf("compile created a directory inside the project tree: %q", dir)
		}
	}
}

// TestCompile_RegeneratesEachTime proves the cache-less behaviour: a wiped artifact is
// rewritten on the next compile, and the rendered bytes are deterministic.
func TestCompile_RegeneratesEachTime(t *testing.T) {
	yaml := "network:\n  host_services:\n    - mysql:3306\n"
	c, fsys := compileFixture(t, yaml)

	res1, err := c.Compile(projDir)
	if err != nil {
		t.Fatalf("first Compile: %v", err)
	}
	first := string(fsys.Files[res1.ProfilePath])

	delete(fsys.Files, res1.ProfilePath)

	res2, err := c.Compile(projDir)
	if err != nil {
		t.Fatalf("second Compile: %v", err)
	}
	second, ok := fsys.Files[res2.ProfilePath]
	if !ok {
		t.Fatal("second compile did not rewrite network.sb (cache-less compiler must rewrite)")
	}
	if string(second) != first {
		t.Errorf("re-render not deterministic:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
}

func TestCompile_MissingProjectConfig(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	c := New(fsys, paths)

	if _, err := c.Compile(projDir); err == nil {
		t.Fatal("expected an error when the project config is absent")
	}
}
