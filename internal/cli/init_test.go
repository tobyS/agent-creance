package cli

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// update regenerates the init-template golden files. Run
//
//	go test ./internal/cli -run TestRenderConfigTemplate -update
//
// (or `make golden`) after an intentional template change, then eyeball the diff.
var update = flag.Bool("update", false, "regenerate golden files")

// renderCases pins the template variants: no manifests (commented placeholder),
// package.json only, both root manifests, and a monorepo (root + sub-packages).
var renderCases = []struct {
	name   string
	gens   []config.Generator
	golden string
}{
	{"none", nil, "none.golden"},
	{"package_only", []config.Generator{{Type: generator.GeneratorPackageJSON, Path: "package.json"}}, "package_only.golden"},
	{"both", []config.Generator{
		{Type: generator.GeneratorPackageJSON, Path: "package.json"},
		{Type: generator.GeneratorComposerJSON, Path: "composer.json"},
	}, "both.golden"},
	{"monorepo", []config.Generator{
		{Type: generator.GeneratorPackageJSON, Path: "package.json"},
		{Type: generator.GeneratorPackageJSON, Path: "apps/web/package.json"},
		{Type: generator.GeneratorComposerJSON, Path: "services/api/composer.json"},
	}, "monorepo.golden"},
}

// TestRenderConfigTemplate golden-tests the generated template (a generated
// artifact, per the project's testing conventions).
func TestRenderConfigTemplate(t *testing.T) {
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderConfigTemplate(tc.gens)
			golden := filepath.Join("testdata", "init", tc.golden)

			if *update {
				require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
				return
			}

			want, err := os.ReadFile(golden)
			require.NoError(t, err, "missing golden file; run with -update to create it")
			require.Equal(t, string(want), got)
		})
	}
}

// TestRenderConfigTemplateParses guards acceptance criterion 1 (and the null-egress
// concern): every variant of the emitted template must parse + validate cleanly, and
// the parsed generators must round-trip the scanned entries (object form, paths kept).
func TestRenderConfigTemplateParses(t *testing.T) {
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(renderConfigTemplate(tc.gens)))
			require.NoError(t, err)
			require.Equal(t, tc.gens, cfg.Network.Egress.Generators)
		})
	}
}

// initFixture wires an App from the sysdep fakes for the project dir initDir, with
// stdout redirected so the success lines can be asserted.
const initDir = "/proj"

type initFixture struct {
	app *App
	fs  *sysdeptest.FakeFileSystem
	out *bytes.Buffer
}

func newInitFixture() *initFixture {
	fs := sysdeptest.NewFakeFileSystem()
	out := &bytes.Buffer{}
	return &initFixture{
		app: &App{
			Stdout: out,
			Stderr: &bytes.Buffer{},
			FS:     fs,
			Paths:  sysdeptest.NewFakePathResolver(),
		},
		fs:  fs,
		out: out,
	}
}

// configAt returns the written config bytes at initDir, failing if absent.
func (f *initFixture) configAt(t *testing.T) []byte {
	t.Helper()
	b, ok := f.fs.Files[filepath.Join(initDir, configFile)]
	require.True(t, ok, "%s not written; files: %v", configFile, keys(f.fs.Files))
	return b
}

func TestInitEmptyDir(t *testing.T) {
	f := newInitFixture()

	require.NoError(t, runInit(context.Background(), f.app, initDir, false))

	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Empty(t, cfg.Network.Egress.Generators, "no manifests → generators commented")
	require.Contains(t, f.out.String(), "no manifests detected")
	require.Contains(t, f.out.String(), "Next: run `agent-creance setup`")
	// No torn temp file left behind.
	_, ok := f.fs.Files[filepath.Join(initDir, configFile)+".tmp"]
	require.False(t, ok, "temp file should be renamed away")
}

func TestInitPackageJSONOnly(t *testing.T) {
	f := newInitFixture()
	f.fs.Files[filepath.Join(initDir, "package.json")] = []byte(`{"dependencies":{}}`)

	require.NoError(t, runInit(context.Background(), f.app, initDir, false))

	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Equal(t,
		[]config.Generator{{Type: generator.GeneratorPackageJSON, Path: "package.json"}},
		cfg.Network.Egress.Generators)
	require.Contains(t, f.out.String(), "package.json")
}

func TestInitBothManifests(t *testing.T) {
	f := newInitFixture()
	f.fs.Files[filepath.Join(initDir, "package.json")] = []byte(`{}`)
	f.fs.Files[filepath.Join(initDir, "composer.json")] = []byte(`{}`)

	require.NoError(t, runInit(context.Background(), f.app, initDir, false))

	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Equal(t,
		[]config.Generator{
			{Type: generator.GeneratorPackageJSON, Path: "package.json"},
			{Type: generator.GeneratorComposerJSON, Path: "composer.json"},
		},
		cfg.Network.Egress.Generators)
}

func TestInitRefusesExistingConfig(t *testing.T) {
	f := newInitFixture()
	original := []byte("agent:\n  command: [mine]\n")
	dest := filepath.Join(initDir, configFile)
	f.fs.Files[dest] = original

	err := runInit(context.Background(), f.app, initDir, false /*force*/)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
	require.Contains(t, err.Error(), "--force")
	// The hand-authored config is left untouched.
	require.Equal(t, original, f.fs.Files[dest])
}

func TestInitForceOverwrites(t *testing.T) {
	f := newInitFixture()
	dest := filepath.Join(initDir, configFile)
	f.fs.Files[dest] = []byte("agent:\n  command: [mine]\n")

	require.NoError(t, runInit(context.Background(), f.app, initDir, true /*force*/))

	require.NotContains(t, string(f.fs.Files[dest]), "mine", "force should overwrite")
	cfg, err := config.Parse(f.fs.Files[dest])
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

// TestInitTemplatePerm pins the written config to the expected file mode.
func TestInitTemplatePerm(t *testing.T) {
	f := newInitFixture()
	require.NoError(t, runInit(context.Background(), f.app, initDir, false))
	require.Equal(t, configFilePerm, f.fs.Perms[filepath.Join(initDir, configFile)])
}

// guard against accidental gitignore-writing (design.md: init writes no gitignore).
func TestInitWritesNoGitignore(t *testing.T) {
	f := newInitFixture()
	require.NoError(t, runInit(context.Background(), f.app, initDir, false))
	for name := range f.fs.Files {
		require.False(t, strings.HasSuffix(name, ".gitignore"),
			"init must not write a .gitignore (%s)", name)
	}
}

// seedManifest writes a manifest at a path relative to initDir, creating the ancestor
// directories the fake's ReadDir needs to surface them as directory entries.
func (f *initFixture) seedManifest(rel string) {
	full := filepath.Join(initDir, rel)
	f.fs.Files[full] = []byte(`{}`)
	for d := filepath.Dir(full); d != initDir && d != "." && d != string(filepath.Separator); d = filepath.Dir(d) {
		f.fs.Dirs[d] = true
	}
}

func TestScanGenerators_MonorepoRootAndSubPackages(t *testing.T) {
	f := newInitFixture()
	f.seedManifest("package.json")               // depth 0
	f.seedManifest("composer.json")              // depth 0
	f.seedManifest("apps/web/package.json")      // depth 2
	f.seedManifest("apps/api/package.json")      // depth 2
	f.seedManifest("services/php/composer.json") // depth 2

	got := scanGenerators(f.fs, initDir)

	require.Equal(t, []config.Generator{
		// depth 0 first, then within a depth by type (package_json before
		// composer_json), then by path.
		{Type: generator.GeneratorPackageJSON, Path: "package.json"},
		{Type: generator.GeneratorComposerJSON, Path: "composer.json"},
		{Type: generator.GeneratorPackageJSON, Path: "apps/api/package.json"},
		{Type: generator.GeneratorPackageJSON, Path: "apps/web/package.json"},
		{Type: generator.GeneratorComposerJSON, Path: "services/php/composer.json"},
	}, got)
}

func TestScanGenerators_DepthBound(t *testing.T) {
	f := newInitFixture()
	f.seedManifest("package.json")             // depth 0 — kept
	f.seedManifest("apps/web/package.json")    // depth 2 — kept
	f.seedManifest("apps/web/ui/package.json") // depth 3 — too deep

	got := scanGenerators(f.fs, initDir)

	require.Equal(t, []config.Generator{
		{Type: generator.GeneratorPackageJSON, Path: "package.json"},
		{Type: generator.GeneratorPackageJSON, Path: "apps/web/package.json"},
	}, got)
}

func TestScanGenerators_SkipsDependencyDirs(t *testing.T) {
	f := newInitFixture()
	f.seedManifest("package.json")
	f.seedManifest("composer.json")
	// The stated trap: installed dependencies under node_modules/ and vendor/ must NOT
	// be mistaken for monorepo packages.
	f.seedManifest("node_modules/react/package.json")
	f.seedManifest("vendor/monolog/monolog/composer.json")

	got := scanGenerators(f.fs, initDir)

	require.Equal(t, []config.Generator{
		{Type: generator.GeneratorPackageJSON, Path: "package.json"},
		{Type: generator.GeneratorComposerJSON, Path: "composer.json"},
	}, got)
}

func TestScanGenerators_SkipsSymlinkedDirs(t *testing.T) {
	f := newInitFixture()
	f.seedManifest("package.json")
	// A symlinked subdirectory with a manifest inside it must not be followed.
	f.fs.Symlinks[filepath.Join(initDir, "linked")] = true
	f.fs.Files[filepath.Join(initDir, "linked", "package.json")] = []byte(`{}`)

	got := scanGenerators(f.fs, initDir)

	require.Equal(t, []config.Generator{
		{Type: generator.GeneratorPackageJSON, Path: "package.json"},
	}, got)
}

func TestInitMonorepoWritesObjectForm(t *testing.T) {
	f := newInitFixture()
	f.seedManifest("package.json")
	f.seedManifest("apps/web/package.json")

	require.NoError(t, runInit(context.Background(), f.app, initDir, false))

	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Equal(t, []config.Generator{
		{Type: generator.GeneratorPackageJSON, Path: "package.json"},
		{Type: generator.GeneratorPackageJSON, Path: "apps/web/package.json"},
	}, cfg.Network.Egress.Generators)
}
