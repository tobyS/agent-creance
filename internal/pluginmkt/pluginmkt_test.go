package pluginmkt_test

import (
	"io/fs"
	"reflect"
	"testing"

	"github.com/tobyS/agent-creance/internal/pluginmkt"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const home = "/home/dev"

// registryPath is where Detect looks for the marketplace registry.
const registryPath = home + "/.claude/plugins/known_marketplaces.json"

func newPaths() *sysdeptest.FakePathResolver {
	p := sysdeptest.NewFakePathResolver()
	p.HomeDir = home
	return p
}

// withDirs marks each path as an existing directory so Stat reports IsDir.
func withDirs(fsys *sysdeptest.FakeFileSystem, dirs ...string) {
	for _, d := range dirs {
		fsys.Dirs[d] = true
	}
}

func TestDetectMissingRegistryNoWarn(t *testing.T) {
	dirs, warns := pluginmkt.Detect(sysdeptest.NewFakeFileSystem(), newPaths())
	if len(dirs) != 0 || len(warns) != 0 {
		t.Fatalf("missing registry: got dirs=%v warns=%v, want both empty", dirs, warns)
	}
}

func TestDetectDirectorySource(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Files[registryPath] = []byte(`{
		"toby-plugins": {"source": {"source": "directory", "path": "/work/toby-plugins"}}
	}`)
	withDirs(fsys, "/work/toby-plugins")

	dirs, warns := pluginmkt.Detect(fsys, newPaths())
	if !reflect.DeepEqual(dirs, []string{"/work/toby-plugins"}) {
		t.Errorf("dirs = %v, want [/work/toby-plugins]", dirs)
	}
	if len(warns) != 0 {
		t.Errorf("warns = %v, want none", warns)
	}
}

func TestDetectFileSourceUsesInstallLocationFallback(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	// "file" source with no source.path falls back to installLocation.
	fsys.Files[registryPath] = []byte(`{
		"local": {"source": {"source": "file"}, "installLocation": "/work/local-mkt"}
	}`)
	withDirs(fsys, "/work/local-mkt")

	dirs, warns := pluginmkt.Detect(fsys, newPaths())
	if !reflect.DeepEqual(dirs, []string{"/work/local-mkt"}) {
		t.Errorf("dirs = %v, want [/work/local-mkt]", dirs)
	}
	if len(warns) != 0 {
		t.Errorf("warns = %v, want none", warns)
	}
}

func TestDetectSkipsGitSources(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Files[registryPath] = []byte(`{
		"official": {"source": {"source": "github", "repo": "anthropics/x"}, "installLocation": "/home/dev/.claude/plugins/marketplaces/official"},
		"remote":   {"source": {"source": "url", "url": "https://example.com/m.json"}}
	}`)
	withDirs(fsys, "/home/dev/.claude/plugins/marketplaces/official")

	dirs, warns := pluginmkt.Detect(fsys, newPaths())
	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want none (git/remote skipped)", dirs)
	}
	if len(warns) != 0 {
		t.Errorf("warns = %v, want none", warns)
	}
}

func TestDetectMixedSortedAndDeduped(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Files[registryPath] = []byte(`{
		"b": {"source": {"source": "directory", "path": "/work/b"}},
		"a": {"source": {"source": "directory", "path": "/work/a"}},
		"dup": {"source": {"source": "directory", "path": "/work/a"}},
		"git": {"source": {"source": "github", "repo": "x/y"}}
	}`)
	withDirs(fsys, "/work/a", "/work/b")

	dirs, warns := pluginmkt.Detect(fsys, newPaths())
	if !reflect.DeepEqual(dirs, []string{"/work/a", "/work/b"}) {
		t.Errorf("dirs = %v, want [/work/a /work/b] sorted+deduped", dirs)
	}
	if len(warns) != 0 {
		t.Errorf("warns = %v, want none", warns)
	}
}

func TestDetectMalformedJSONWarns(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Files[registryPath] = []byte(`{not json`)

	dirs, warns := pluginmkt.Detect(fsys, newPaths())
	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want none", dirs)
	}
	if len(warns) != 1 {
		t.Errorf("warns = %v, want exactly 1", warns)
	}
}

func TestDetectUnreadableRegistryWarns(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Errs[registryPath] = fs.ErrPermission

	dirs, warns := pluginmkt.Detect(fsys, newPaths())
	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want none", dirs)
	}
	if len(warns) != 1 {
		t.Errorf("warns = %v, want exactly 1", warns)
	}
}

func TestDetectMissingSourceDirWarnsAndSkips(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	fsys.Files[registryPath] = []byte(`{
		"stale": {"source": {"source": "directory", "path": "/work/gone"}}
	}`)
	// No Dirs entry for /work/gone → Stat returns fs.ErrNotExist.

	dirs, warns := pluginmkt.Detect(fsys, newPaths())
	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want none (missing source dir skipped)", dirs)
	}
	if len(warns) != 1 {
		t.Errorf("warns = %v, want exactly 1", warns)
	}
}

func TestDetectHomeErrorWarns(t *testing.T) {
	paths := newPaths()
	paths.HomeErr = fs.ErrPermission

	dirs, warns := pluginmkt.Detect(sysdeptest.NewFakeFileSystem(), paths)
	if len(dirs) != 0 {
		t.Errorf("dirs = %v, want none", dirs)
	}
	if len(warns) != 1 {
		t.Errorf("warns = %v, want exactly 1", warns)
	}
}
