package config

import (
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const testHome = "/home/toby"

var globalPath = testHome + "/.config/agent-creance.yaml"

// newLoader wires a Loader over in-memory fakes seeded with the given path→YAML map.
// It returns the path resolver too so a test can script symlink aliases.
func newLoader(files map[string]string) (*Loader, *sysdeptest.FakePathResolver) {
	fsys := sysdeptest.NewFakeFileSystem()
	for path, body := range files {
		fsys.Files[path] = []byte(body)
	}
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	return NewLoader(fsys, paths), paths
}

func TestLoad_GlobalPresentMerged(t *testing.T) {
	l, _ := newLoader(map[string]string{
		globalPath: "" +
			"agent:\n  workdir: /global\n" +
			"network:\n  egress:\n    allow:\n      - host: api.anthropic.com\n",
		"/proj/.agent-creance.yaml": "" +
			"agent:\n  workdir: /proj\n" +
			"network:\n  egress:\n    allow:\n      - host: api.github.com\n",
	})

	cfg, err := l.Load("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Agent.Workdir != "/proj" {
		t.Errorf("Workdir = %q, want /proj (project overrides global)", cfg.Agent.Workdir)
	}
	hosts := allowHosts(cfg)
	if !reflect.DeepEqual(hosts, []string{"api.anthropic.com", "api.github.com"}) {
		t.Errorf("allow hosts = %v, want [anthropic, github] (global then project)", hosts)
	}
	if cfg.Include != nil {
		t.Errorf("Include = %v, want nil (resolved away)", cfg.Include)
	}
}

func TestLoad_GlobalAbsentIsNoOp(t *testing.T) {
	l, _ := newLoader(map[string]string{
		// no globalPath entry → ReadFile yields fs.ErrNotExist → skipped
		"/proj/.agent-creance.yaml": "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	})
	cfg, err := l.Load("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("Load with absent global: %v", err)
	}
	if hosts := allowHosts(cfg); !reflect.DeepEqual(hosts, []string{"react.dev"}) {
		t.Errorf("allow hosts = %v, want [react.dev] (project only)", hosts)
	}
}

func TestLoad_RecursiveInclude(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "" +
			"agent:\n  workdir: /proj\n" +
			"include:\n  - team.yaml\n" +
			"network:\n  egress:\n    allow:\n      - host: project.example\n",
		"/proj/team.yaml": "" +
			"agent:\n  workdir: /team\n" + // overridden by project's own
			"include:\n  - base.yaml\n" +
			"network:\n  egress:\n    allow:\n      - host: team.example\n",
		"/proj/base.yaml": "network:\n  egress:\n    allow:\n      - host: base.example\n",
	})
	cfg, err := l.Load("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Precedence low→high: base (deepest include) → team → project own.
	wantHosts := []string{"base.example", "team.example", "project.example"}
	if hosts := allowHosts(cfg); !reflect.DeepEqual(hosts, wantHosts) {
		t.Errorf("allow hosts = %v, want %v", hosts, wantHosts)
	}
	if cfg.Agent.Workdir != "/proj" {
		t.Errorf("Workdir = %q, want /proj (own beats included)", cfg.Agent.Workdir)
	}
}

func TestLoad_UnionDedupeAcrossGlobalAndInclude(t *testing.T) {
	dupRule := "      - host: api.github.com\n        paths: [\"/x\"]\n"
	l, _ := newLoader(map[string]string{
		globalPath:                  "network:\n  egress:\n    allow:\n" + dupRule,
		"/proj/.agent-creance.yaml": "include:\n  - team.yaml\n",
		"/proj/team.yaml":           "network:\n  egress:\n    allow:\n" + dupRule, // identical to global's
	})
	cfg, err := l.Load("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if hosts := allowHosts(cfg); !reflect.DeepEqual(hosts, []string{"api.github.com"}) {
		t.Errorf("allow hosts = %v, want one deduped rule", hosts)
	}
}

func TestLoad_EnvMerge(t *testing.T) {
	l, _ := newLoader(map[string]string{
		globalPath:                  "env:\n  SHARED: global\n  ONLY_GLOBAL: g\n",
		"/proj/.agent-creance.yaml": "env:\n  SHARED: project\n  ONLY_PROJECT: p\n",
	})
	cfg, err := l.Load("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]string{"SHARED": "project", "ONLY_GLOBAL": "g", "ONLY_PROJECT": "p"}
	if !reflect.DeepEqual(cfg.Env, want) {
		t.Errorf("Env = %v, want %v", cfg.Env, want)
	}
}

func TestLoad_CycleDetected(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/a.yaml": "include:\n  - b.yaml\n",
		"/proj/b.yaml": "include:\n  - a.yaml\n",
	})
	_, err := l.Load("/proj/a.yaml")
	if !errors.Is(err, ErrIncludeCycle) {
		t.Fatalf("err = %v, want ErrIncludeCycle", err)
	}
	if !strings.Contains(err.Error(), "/proj/a.yaml") || !strings.Contains(err.Error(), "/proj/b.yaml") {
		t.Errorf("cycle error missing chain paths: %v", err)
	}
}

func TestLoad_SymlinkDisguisedCycleDetected(t *testing.T) {
	l, paths := newLoader(map[string]string{
		"/proj/a.yaml": "include:\n  - b.yaml\n",
		"/proj/b.yaml": "include:\n  - a.yaml\n", // content irrelevant; cycle trips first
	})
	// b.yaml is a symlink to a.yaml's canonical identity.
	paths.Symlinks["/proj/b.yaml"] = "/proj/a.yaml"

	_, err := l.Load("/proj/a.yaml")
	if !errors.Is(err, ErrIncludeCycle) {
		t.Fatalf("err = %v, want ErrIncludeCycle (symlink alias)", err)
	}
}

func TestLoad_DepthLimitExceeded(t *testing.T) {
	files := map[string]string{}
	// f0 includes f1 ... f10 includes f11; resolving f11 is at depth 11 > 10.
	for i := 0; i <= maxIncludeDepth; i++ {
		files[fmt.Sprintf("/proj/f%d.yaml", i)] = fmt.Sprintf("include:\n  - f%d.yaml\n", i+1)
	}
	l, _ := newLoader(files)

	_, err := l.Load("/proj/f0.yaml")
	if !errors.Is(err, ErrMaxIncludeDepth) {
		t.Fatalf("err = %v, want ErrMaxIncludeDepth", err)
	}
	overPath := fmt.Sprintf("/proj/f%d.yaml", maxIncludeDepth+1)
	if !strings.Contains(err.Error(), overPath) {
		t.Errorf("depth error = %v, want offending path %s", err, overPath)
	}
}

func TestLoad_MissingIncludeIsError(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "include:\n  - nope.yaml\n",
	})
	_, err := l.Load("/proj/.agent-creance.yaml")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want errors.Is(fs.ErrNotExist)", err)
	}
	if !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("missing-include error missing path: %v", err)
	}
}

func TestLoad_MissingProjectIsError(t *testing.T) {
	l, _ := newLoader(map[string]string{}) // nothing on disk
	_, err := l.Load("/proj/.agent-creance.yaml")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want errors.Is(fs.ErrNotExist) for missing project", err)
	}
}

func TestLoad_InvalidIncludedFileSurfaces(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "include:\n  - bad.yaml\n",
		"/proj/bad.yaml": "network:\n  egress:\n    allow:\n" +
			"      - host: api.anthropic.com\n        mode: passthrough\n        paths: [\"/x\"]\n",
	})
	_, err := l.Load("/proj/.agent-creance.yaml")
	if err == nil {
		t.Fatal("Load: want validation error from included file, got nil")
	}
	if !strings.Contains(err.Error(), "cannot carry paths") {
		t.Errorf("error = %v, want passthrough validation message", err)
	}
	if !strings.Contains(err.Error(), "bad.yaml") {
		t.Errorf("error = %v, want offending file path", err)
	}
}

func TestLoad_Deterministic(t *testing.T) {
	files := map[string]string{
		globalPath: "network:\n  egress:\n    allow:\n      - host: a.example\n      - host: b.example\n",
		"/proj/.agent-creance.yaml": "include:\n  - team.yaml\n" +
			"network:\n  egress:\n    allow:\n      - host: c.example\n",
		"/proj/team.yaml": "env:\n  X: \"1\"\n  Y: \"2\"\n",
	}
	l1, _ := newLoader(files)
	l2, _ := newLoader(files)
	c1, err := l1.Load("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("Load #1: %v", err)
	}
	c2, err := l2.Load("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("Load #2: %v", err)
	}
	if !reflect.DeepEqual(c1, c2) {
		t.Errorf("Load not deterministic:\n #1=%+v\n #2=%+v", c1, c2)
	}
}

func TestGlobalPath(t *testing.T) {
	l, _ := newLoader(nil)
	got, err := l.GlobalPath()
	if err != nil {
		t.Fatalf("GlobalPath: %v", err)
	}
	if got != globalPath {
		t.Errorf("GlobalPath = %q, want %q", got, globalPath)
	}
}

func TestResolveLayer_FileAndIncludesNoImplicitGlobal(t *testing.T) {
	l, _ := newLoader(map[string]string{
		// A global exists, but ResolveLayer must NOT pull it in.
		globalPath:                  "network:\n  egress:\n    allow:\n      - host: global.example\n",
		"/proj/.agent-creance.yaml": "include:\n  - team.yaml\n" + "network:\n  egress:\n    allow:\n      - host: project.example\n",
		"/proj/team.yaml":           "network:\n  egress:\n    allow:\n      - host: team.example\n",
	})
	cfg, err := l.ResolveLayer("/proj/.agent-creance.yaml", false)
	if err != nil {
		t.Fatalf("ResolveLayer: %v", err)
	}
	// include resolved (team then own), but the implicit global is absent.
	wantHosts := []string{"team.example", "project.example"}
	if hosts := allowHosts(cfg); !reflect.DeepEqual(hosts, wantHosts) {
		t.Errorf("allow hosts = %v, want %v (no implicit global)", hosts, wantHosts)
	}
	if cfg.Include != nil {
		t.Errorf("Include = %v, want nil (resolved away)", cfg.Include)
	}
}

func TestResolveLayer_OptionalMissingIsEmpty(t *testing.T) {
	l, _ := newLoader(nil) // nothing on disk
	cfg, err := l.ResolveLayer("/no/such/overlay.yaml", true)
	if err != nil {
		t.Fatalf("ResolveLayer optional: %v", err)
	}
	if !reflect.DeepEqual(*cfg, Config{}) {
		t.Errorf("cfg = %+v, want empty Config (optional missing)", *cfg)
	}
}

func TestResolveLayer_RequiredMissingIsError(t *testing.T) {
	l, _ := newLoader(nil)
	if _, err := l.ResolveLayer("/no/such/file.yaml", false); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestResolveFiles_ProjectOnly(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	})
	got, err := l.ResolveFiles("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	if want := []string{"/proj/.agent-creance.yaml"}; !sortedEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestResolveFiles_GlobalAndNestedIncludes(t *testing.T) {
	l, _ := newLoader(map[string]string{
		globalPath:                  "network:\n  egress:\n    allow:\n      - host: global.example\n",
		"/proj/.agent-creance.yaml": "include:\n  - team.yaml\n",
		"/proj/team.yaml":           "include:\n  - base.yaml\n",
		"/proj/base.yaml":           "network:\n  egress:\n    allow:\n      - host: base.example\n",
	})
	got, err := l.ResolveFiles("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	want := []string{globalPath, "/proj/.agent-creance.yaml", "/proj/team.yaml", "/proj/base.yaml"}
	if !sortedEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestResolveFiles_GlobalAbsentOmitted(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "network:\n  egress:\n    allow:\n      - host: react.dev\n",
	})
	got, err := l.ResolveFiles("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	if want := []string{"/proj/.agent-creance.yaml"}; !sortedEqual(got, want) {
		t.Errorf("files = %v, want %v (absent global omitted)", got, want)
	}
}

func TestResolveFiles_DiamondDeduped(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "include:\n  - a.yaml\n  - b.yaml\n",
		"/proj/a.yaml":              "include:\n  - shared.yaml\n",
		"/proj/b.yaml":              "include:\n  - shared.yaml\n",
		"/proj/shared.yaml":         "network:\n  egress:\n    allow:\n      - host: shared.example\n",
	})
	got, err := l.ResolveFiles("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	want := []string{"/proj/.agent-creance.yaml", "/proj/a.yaml", "/proj/b.yaml", "/proj/shared.yaml"}
	if !sortedEqual(got, want) {
		t.Errorf("files = %v, want %v (shared.yaml deduped)", got, want)
	}
}

func TestResolveFiles_AbsoluteAndHomeIncludes(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "include:\n  - /etc/ac/abs.yaml\n  - ~/frag.yaml\n",
		"/etc/ac/abs.yaml":          "network:\n  egress:\n    allow:\n      - host: abs.example\n",
		testHome + "/frag.yaml":     "network:\n  egress:\n    allow:\n      - host: home.example\n",
	})
	got, err := l.ResolveFiles("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	want := []string{"/proj/.agent-creance.yaml", "/etc/ac/abs.yaml", testHome + "/frag.yaml"}
	if !sortedEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

func TestResolveFiles_MissingIncludeIsError(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "include:\n  - nope.yaml\n",
	})
	_, err := l.ResolveFiles("/proj/.agent-creance.yaml")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("error missing path: %v", err)
	}
}

func TestResolveFiles_MissingProjectIsError(t *testing.T) {
	l, _ := newLoader(nil)
	if _, err := l.ResolveFiles("/proj/.agent-creance.yaml"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist for missing project", err)
	}
}

func TestResolveFiles_CycleDetected(t *testing.T) {
	l, _ := newLoader(map[string]string{
		"/proj/a.yaml": "include:\n  - b.yaml\n",
		"/proj/b.yaml": "include:\n  - a.yaml\n",
	})
	_, err := l.ResolveFiles("/proj/a.yaml")
	if !errors.Is(err, ErrIncludeCycle) {
		t.Fatalf("err = %v, want ErrIncludeCycle", err)
	}
}

func TestResolveFiles_DepthLimitExceeded(t *testing.T) {
	files := map[string]string{}
	for i := 0; i <= maxIncludeDepth; i++ {
		files[fmt.Sprintf("/proj/f%d.yaml", i)] = fmt.Sprintf("include:\n  - f%d.yaml\n", i+1)
	}
	l, _ := newLoader(files)
	_, err := l.ResolveFiles("/proj/f0.yaml")
	if !errors.Is(err, ErrMaxIncludeDepth) {
		t.Fatalf("err = %v, want ErrMaxIncludeDepth", err)
	}
}

func TestResolveFiles_SymlinkCanonicalised(t *testing.T) {
	l, paths := newLoader(map[string]string{
		"/proj/.agent-creance.yaml": "include:\n  - link.yaml\n",
		"/proj/link.yaml":           "network:\n  egress:\n    allow:\n      - host: real.example\n",
	})
	// link.yaml is a symlink to the real fragment; ResolveFiles reports the target.
	paths.Symlinks["/proj/link.yaml"] = "/real/frag.yaml"

	got, err := l.ResolveFiles("/proj/.agent-creance.yaml")
	if err != nil {
		t.Fatalf("ResolveFiles: %v", err)
	}
	want := []string{"/proj/.agent-creance.yaml", "/real/frag.yaml"}
	if !sortedEqual(got, want) {
		t.Errorf("files = %v, want %v (symlink canonicalised)", got, want)
	}
}

func sortedEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string{}, a...)
	bs := append([]string{}, b...)
	sort.Strings(as)
	sort.Strings(bs)
	return reflect.DeepEqual(as, bs)
}

func allowHosts(c *Config) []string {
	var hosts []string
	for _, r := range c.Network.Egress.Allow {
		hosts = append(hosts, r.Host)
	}
	return hosts
}
