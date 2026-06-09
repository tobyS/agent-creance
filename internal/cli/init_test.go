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

// renderCases pins the three template variants: no manifests (commented
// placeholder), package.json only, and both manifests.
var renderCases = []struct {
	name   string
	gens   []string
	golden string
}{
	{"none", nil, "none.golden"},
	{"package_only", []string{generator.GeneratorPackageJSON}, "package_only.golden"},
	{"both", []string{generator.GeneratorPackageJSON, generator.GeneratorComposerJSON}, "both.golden"},
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

// wantGenerators converts bare generator names to the parsed config.Generator shape
// (bare form → empty Path), nil for none.
func wantGenerators(names []string) []config.Generator {
	if len(names) == 0 {
		return nil
	}
	out := make([]config.Generator, len(names))
	for i, n := range names {
		out[i] = config.Generator{Type: n}
	}
	return out
}

// TestRenderConfigTemplateParses guards acceptance criterion 1 (and the null-egress
// concern): every variant of the emitted template must parse + validate cleanly.
func TestRenderConfigTemplateParses(t *testing.T) {
	for _, tc := range renderCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := config.Parse([]byte(renderConfigTemplate(tc.gens)))
			require.NoError(t, err)
			require.Equal(t, wantGenerators(tc.gens), cfg.Network.Egress.Generators)
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
	require.Equal(t, wantGenerators([]string{generator.GeneratorPackageJSON}), cfg.Network.Egress.Generators)
	require.Contains(t, f.out.String(), generator.GeneratorPackageJSON)
}

func TestInitBothManifests(t *testing.T) {
	f := newInitFixture()
	f.fs.Files[filepath.Join(initDir, "package.json")] = []byte(`{}`)
	f.fs.Files[filepath.Join(initDir, "composer.json")] = []byte(`{}`)

	require.NoError(t, runInit(context.Background(), f.app, initDir, false))

	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Equal(t,
		wantGenerators([]string{generator.GeneratorPackageJSON, generator.GeneratorComposerJSON}),
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
