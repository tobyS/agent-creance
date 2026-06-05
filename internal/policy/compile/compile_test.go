package compile

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// update regenerates the golden artifact: `go test ./... -update` (make golden).
var update = flag.Bool("update", false, "regenerate golden files")

const (
	testHome = "/home/toby"
	projDir  = "/proj"
)

// fakeRunner is a hermetic generatorRunner: it returns canned rules per generator name
// and counts calls so cache-hit tests can assert zero generator work.
type fakeRunner struct {
	rules map[string][]generator.Rule
	calls int
}

func (f *fakeRunner) Run(_ context.Context, name string, _ []byte) ([]generator.Rule, error) {
	f.calls++
	return f.rules[name], nil
}

// fixture wires a Compiler over in-memory fakes seeded with the given path→contents map,
// plus a fake generator runner. It returns the compiler, the filesystem (to assert on
// writes), and the runner (to assert call counts).
func fixture(t *testing.T, files map[string]string, runner generatorRunner) (*Compiler, *sysdeptest.FakeFileSystem) {
	t.Helper()
	fsys := sysdeptest.NewFakeFileSystem()
	for path, body := range files {
		fsys.Files[path] = []byte(body)
	}
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	return &Compiler{
		fs:     fsys,
		loader: config.NewLoader(fsys, paths),
		state:  state.New(paths),
		runner: runner,
	}, fsys
}

// representativeFiles is the config used by the golden + cache tests: explicit project
// allow+deny, a global rule, a package_json generator, and a session-overlay once-rule.
func representativeFiles() map[string]string {
	return map[string]string{
		testHome + "/.config/agent-creance.yaml": "" +
			"network:\n  egress:\n    allow:\n      - host: api.anthropic.com\n        mode: passthrough\n" +
			"    deny_always:\n      - host: w3schools.com\n        reason: low quality\n",
		projDir + "/.agent-creance.yaml": "" +
			"network:\n  egress:\n    generators:\n      - package_json\n" +
			"    allow:\n      - host: api.github.com\n        paths: [\"/repos/tobyS/x/\"]\n        methods: [GET, POST]\n" +
			"    deny_always:\n      - host: \"*\"\n        paths: [\"**/.env\"]\n        reason: secrets\n",
		projDir + "/package.json": `{"dependencies":{"react":"^18"}}`,
	}
}

func representativeRunner() *fakeRunner {
	return &fakeRunner{rules: map[string][]generator.Rule{
		generator.GeneratorPackageJSON: {
			{Rule: policy.Rule{Host: "react.dev"}, Source: "generated:package_json:react"},
			{Rule: policy.Rule{Host: "github.com", Paths: []string{"/facebook/react/"}}, Source: "generated:package_json:react"},
			{Rule: policy.Rule{Host: "objects.githubusercontent.com"}, Source: "generated:package_json:react", LowerTrust: true},
		},
	}}
}

func overlayYAML() string {
	return "network:\n  egress:\n    allow:\n      - host: docs.somelib.io\n        paths: [\"/v2/\"]\n"
}

func overlayPath() string {
	// Mirrors state.Layout.SessionOverlay() for the fake project dir.
	layout, _ := state.New(func() *sysdeptest.FakePathResolver {
		p := sysdeptest.NewFakePathResolver()
		p.HomeDir = testHome
		return p
	}()).Resolve(projDir)
	return layout.SessionOverlay()
}

func TestCompile_Golden(t *testing.T) {
	files := representativeFiles()
	files[overlayPath()] = overlayYAML()

	c, fsys := fixture(t, files, representativeRunner())
	res, err := c.Compile(context.Background(), projDir)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Skipped {
		t.Fatal("first compile reported Skipped")
	}

	got := fsys.Files[res.PolicyPath]
	golden := filepath.Join("testdata", "policy.golden")
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("policy.json mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestCompile_CacheHitSkipsGenerators(t *testing.T) {
	files := representativeFiles()
	runner := representativeRunner()
	c, fsys := fixture(t, files, runner)

	res1, err := c.Compile(context.Background(), projDir)
	if err != nil {
		t.Fatalf("Compile #1: %v", err)
	}
	if res1.Skipped {
		t.Fatal("#1 should not be a cache hit")
	}
	callsAfterFirst := runner.calls
	before := append([]byte(nil), fsys.Files[res1.PolicyPath]...)

	res2, err := c.Compile(context.Background(), projDir)
	if err != nil {
		t.Fatalf("Compile #2: %v", err)
	}
	if !res2.Skipped {
		t.Error("#2 should be a cache hit (Skipped)")
	}
	if runner.calls != callsAfterFirst {
		t.Errorf("generator runner called %d more times on cache hit, want 0", runner.calls-callsAfterFirst)
	}
	if res2.InputHash != res1.InputHash {
		t.Errorf("hash changed across identical compiles: %q != %q", res2.InputHash, res1.InputHash)
	}
	if string(fsys.Files[res1.PolicyPath]) != string(before) {
		t.Error("policy.json rewritten on cache hit")
	}
}

func TestCompile_CacheMissOnInputChange(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(fsys *sysdeptest.FakeFileSystem)
	}{
		{"project yaml", func(fsys *sysdeptest.FakeFileSystem) {
			fsys.Files[projDir+"/.agent-creance.yaml"] = append(
				fsys.Files[projDir+"/.agent-creance.yaml"], []byte("      - host: added.example\n")...)
		}},
		{"manifest", func(fsys *sysdeptest.FakeFileSystem) {
			fsys.Files[projDir+"/package.json"] = []byte(`{"dependencies":{"react":"^18","vue":"^3"}}`)
		}},
		{"overlay content", func(fsys *sysdeptest.FakeFileSystem) {
			fsys.Files[overlayPath()] = []byte("network:\n  egress:\n    allow:\n      - host: changed.example\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := representativeFiles()
			files[overlayPath()] = overlayYAML()
			runner := representativeRunner()
			c, fsys := fixture(t, files, runner)

			res1, err := c.Compile(context.Background(), projDir)
			if err != nil {
				t.Fatalf("Compile #1: %v", err)
			}
			callsAfterFirst := runner.calls

			tc.mutate(fsys)

			res2, err := c.Compile(context.Background(), projDir)
			if err != nil {
				t.Fatalf("Compile #2: %v", err)
			}
			if res2.Skipped {
				t.Error("mutated input should force regeneration, got cache hit")
			}
			if res2.InputHash == res1.InputHash {
				t.Error("input hash unchanged after mutating an input")
			}
			if runner.calls == callsAfterFirst {
				t.Error("generators not re-run after cache miss")
			}
		})
	}
}

// TestCompile_C4NoInTreeWrite guards cross-cutting C4: compiling must never create any
// file or directory inside the project tree — the artifact lives out-of-tree.
func TestCompile_C4NoInTreeWrite(t *testing.T) {
	files := representativeFiles()
	files[overlayPath()] = overlayYAML()
	c, fsys := fixture(t, files, representativeRunner())

	res, err := c.Compile(context.Background(), projDir)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !strings.HasPrefix(res.PolicyPath, testHome) {
		t.Errorf("policy path %q not under the out-of-tree state root", res.PolicyPath)
	}
	// The seeded in-tree inputs (.agent-creance.yaml, package.json) are reads; assert no
	// new path under projDir appeared as a written file or created dir.
	seeded := map[string]bool{
		projDir + "/.agent-creance.yaml": true,
		projDir + "/package.json":        true,
	}
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

func TestCompile_Annotations(t *testing.T) {
	files := representativeFiles()
	files[overlayPath()] = overlayYAML()
	c, fsys := fixture(t, files, representativeRunner())

	res, err := c.Compile(context.Background(), projDir)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var compiled policy.Compiled
	if err := json.Unmarshal(fsys.Files[res.PolicyPath], &compiled); err != nil {
		t.Fatalf("unmarshal artifact: %v", err)
	}
	if compiled.Version != policy.CompiledVersion {
		t.Errorf("version = %d, want %d", compiled.Version, policy.CompiledVersion)
	}

	wantAllow := map[string]string{
		"api.anthropic.com":             "global",
		"api.github.com":                "explicit",
		"react.dev":                     "generated:package_json:react",
		"github.com":                    "generated:package_json:react",
		"objects.githubusercontent.com": "generated:package_json:react",
		"docs.somelib.io":               "once",
	}
	for _, r := range compiled.Allow {
		if want, ok := wantAllow[r.Host]; ok && r.Source != want {
			t.Errorf("allow %q source = %q, want %q", r.Host, r.Source, want)
		}
		delete(wantAllow, r.Host)
		if r.Host == "objects.githubusercontent.com" && !r.LowerTrust {
			t.Error("lower_trust not preserved on the CDN companion rule")
		}
	}
	if len(wantAllow) != 0 {
		t.Errorf("missing annotated allow rules: %v", wantAllow)
	}

	wantDeny := map[string]string{"w3schools.com": "global", "*": "explicit"}
	for _, r := range compiled.DenyAlways {
		if want, ok := wantDeny[r.Host]; ok && r.Source != want {
			t.Errorf("deny %q source = %q, want %q", r.Host, r.Source, want)
		}
		delete(wantDeny, r.Host)
	}
	if len(wantDeny) != 0 {
		t.Errorf("missing annotated deny rules: %v", wantDeny)
	}
}

func TestCompile_UnknownGeneratorErrors(t *testing.T) {
	files := map[string]string{
		projDir + "/.agent-creance.yaml": "network:\n  egress:\n    generators:\n      - bogus_gen\n",
	}
	c, _ := fixture(t, files, representativeRunner())
	if _, err := c.Compile(context.Background(), projDir); err == nil {
		t.Fatal("expected error for unknown generator")
	}
}

func TestCompile_AbsentManifestIsNoRules(t *testing.T) {
	// package_json listed but no package.json on disk → generator contributes nothing,
	// and the runner is never invoked (no manifest to feed it).
	files := map[string]string{
		projDir + "/.agent-creance.yaml": "network:\n  egress:\n    generators:\n      - package_json\n" +
			"    allow:\n      - host: only.explicit\n",
	}
	runner := representativeRunner()
	c, fsys := fixture(t, files, runner)
	res, err := c.Compile(context.Background(), projDir)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if runner.calls != 0 {
		t.Errorf("runner called %d times with no manifest, want 0", runner.calls)
	}
	var compiled policy.Compiled
	if err := json.Unmarshal(fsys.Files[res.PolicyPath], &compiled); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(compiled.Allow) != 1 || compiled.Allow[0].Host != "only.explicit" {
		t.Errorf("allow = %+v, want only the explicit rule", compiled.Allow)
	}
}
