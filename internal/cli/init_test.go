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
	"github.com/tobyS/agent-creance/internal/setupcheck"
	"github.com/tobyS/agent-creance/internal/sysdep"
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
			got := renderConfigTemplate(tc.gens, nil, nil)
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
			cfg, err := config.Parse([]byte(renderConfigTemplate(tc.gens, nil, nil)))
			require.NoError(t, err)
			require.Equal(t, tc.gens, cfg.Network.Egress.Generators)
		})
	}
}

// initFixture wires an App from the sysdep fakes for the project dir initDir, with
// stdout redirected so the success lines can be asserted.
const initDir = "/proj"

type initFixture struct {
	app    *App
	fs     *sysdeptest.FakeFileSystem
	kc     *sysdeptest.FakeKeychain
	prober *sysdeptest.FakeTLSProber
	term   *sysdeptest.FakeTerminal
	out    *bytes.Buffer
}

// newInitFixture wires the App and defaults to an ALREADY-SET-UP host: the
// mitmproxy CA is in the keychain and the skill file is present, so the host-setup
// gate short-circuits (StatusOK) and the scaffold-focused tests stay about
// filesystem behavior. The setup seams are seeded like newSetupFixture so that a
// test which clears the keychain (setupMissing) and confirms the prompt can drive a
// successful runSetup. Stdin is empty and the terminal non-interactive by default;
// the bootstrap tests override term.Interactive and the stdin reader.
func newInitFixture() *initFixture {
	fs := sysdeptest.NewFakeFileSystem()
	fs.Files[skillPath] = []byte("# skill")                       // skill present (StatusOK half)
	fs.Files[setupCAPath] = []byte("-----BEGIN CERTIFICATE-----") // CA PEM → EnsureCA fast path

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = runHome

	proc := sysdeptest.NewFakeProcessManager()
	proc.SpawnPID = 7000
	ports := sysdeptest.NewFakePortAllocator()
	ports.AllocPort = 54321

	out := &bytes.Buffer{}
	app := &App{
		Stdout:         out,
		Stderr:         &bytes.Buffer{},
		Stdin:          strings.NewReader(""),
		FS:             fs,
		Paths:          paths,
		Keychain:       sysdeptest.NewFakeKeychain().WithCertificate(setupcheck.CACommonName, "-----BEGIN CERTIFICATE-----"),
		ProcessManager: proc,
		PortAllocator:  ports,
		TLSProber:      sysdeptest.NewFakeTLSProber(), // ProbeTrusted by default
		Sleeper:        &sysdeptest.FakeSleeper{},
		Terminal:       &sysdeptest.FakeTerminal{}, // non-interactive by default
	}
	return &initFixture{
		app:    app,
		fs:     fs,
		kc:     app.Keychain.(*sysdeptest.FakeKeychain),
		prober: app.TLSProber.(*sysdeptest.FakeTLSProber),
		term:   app.Terminal.(*sysdeptest.FakeTerminal),
		out:    out,
	}
}

// setupMissing flips the fixture to a host where setup has NOT run: an empty
// keychain (FindCertificate → ErrItemNotFound → StatusCANotTrusted) with the skill
// removed. The keychain is left ready to record a cert if a confirmed runSetup
// drives the install.
func (f *initFixture) setupMissing() {
	f.kc = sysdeptest.NewFakeKeychain()
	f.app.Keychain = f.kc
	delete(f.fs.Files, skillPath)
}

// withStdin sets the confirm prompt's input.
func (f *initFixture) withStdin(s string) {
	f.app.Stdin = strings.NewReader(s)
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

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false))

	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Empty(t, cfg.Network.Egress.Generators, "no manifests → generators commented")
	require.Contains(t, f.out.String(), "no manifests detected")
	// Already-set-up host (fixture default): the next step is run, not setup.
	require.Contains(t, f.out.String(), "Next: run `agent-creance run`.")
	// No torn temp file left behind.
	_, ok := f.fs.Files[filepath.Join(initDir, configFile)+".tmp"]
	require.False(t, ok, "temp file should be renamed away")
}

func TestInitPackageJSONOnly(t *testing.T) {
	f := newInitFixture()
	f.fs.Files[filepath.Join(initDir, "package.json")] = []byte(`{"dependencies":{}}`)

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false))

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

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false))

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

	err := runInit(context.Background(), f.app, initDir, false /*force*/, false /*noSetup*/)
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

	require.NoError(t, runInit(context.Background(), f.app, initDir, true /*force*/, false /*noSetup*/))

	require.NotContains(t, string(f.fs.Files[dest]), "mine", "force should overwrite")
	cfg, err := config.Parse(f.fs.Files[dest])
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

// TestInitTemplatePerm pins the written config to the expected file mode.
func TestInitTemplatePerm(t *testing.T) {
	f := newInitFixture()
	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false))
	require.Equal(t, configFilePerm, f.fs.Perms[filepath.Join(initDir, configFile)])
}

// guard against accidental gitignore-writing (design.md: init writes no gitignore).
func TestInitWritesNoGitignore(t *testing.T) {
	f := newInitFixture()
	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false))
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

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false))

	cfg, err := config.Parse(f.configAt(t))
	require.NoError(t, err)
	require.Equal(t, []config.Generator{
		{Type: generator.GeneratorPackageJSON, Path: "package.json"},
		{Type: generator.GeneratorPackageJSON, Path: "apps/web/package.json"},
	}, cfg.Network.Egress.Generators)
}

// configWritten reports whether the scaffold was written at initDir.
func (f *initFixture) configWritten() bool {
	_, ok := f.fs.Files[filepath.Join(initDir, configFile)]
	return ok
}

// --- host-setup bootstrap gate (AC-0038) ------------------------------------
//
// These cover the keychain/terminal-dependent paths the testscript can't reach
// hermetically (cli.Main wires OSKeychain → the absolute /usr/bin/security), so
// they drive runInit directly against the fakes — the run_test.go / setup_test.go
// pattern.

func TestInitAlreadySetUp(t *testing.T) {
	f := newInitFixture() // default: CA in keychain + skill present → StatusOK

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false))

	// The cheap gate ran, but no setup work was driven and no prompt was shown.
	require.Equal(t, []string{setupcheck.CACommonName}, f.kc.CertLookups, "gate should probe the CA")
	require.Empty(t, f.kc.AddedCerts, "no keychain write on the already-set-up path")
	require.Empty(t, f.prober.Calls, "no Bootstrap probe on the already-set-up path")
	require.True(t, f.configWritten())
	got := f.out.String()
	require.NotContains(t, got, "[y/N]", "no prompt when setup is already complete")
	require.NotContains(t, got, "hasn't completed the one-time")
	require.Contains(t, got, "Next: run `agent-creance run`.")
}

func TestInitInteractiveConfirmDrivesSetup(t *testing.T) {
	f := newInitFixture()
	f.setupMissing() // CA not trusted → StatusCANotTrusted
	f.term.Interactive = true
	f.withStdin("y\n")
	// Verify-first untrusted then trusted post-install → the install path runs.
	f.prober.Outcomes = []sysdep.ProbeOutcome{sysdep.ProbeUntrusted, sysdep.ProbeTrusted}

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, false))

	// The shared setup orchestration actually ran: CA installed, skill (re)written.
	require.Equal(t, []string{setupCAPath}, f.kc.AddedCerts, "confirmed setup should install the CA")
	require.NotEmpty(t, f.prober.Calls, "Bootstrap should verify trust")
	_, ok := f.fs.Files[skillPath]
	require.True(t, ok, "skill should be installed by the driven setup")
	require.True(t, f.configWritten(), "config written after setup succeeds")
	got := f.out.String()
	require.Contains(t, got, "hasn't completed the one-time", "explanation before the prompt")
	require.Contains(t, got, "CA installed and verified", "setup's own messages are reused")
	require.Contains(t, got, "Next: run `agent-creance run`.")
}

func TestInitInteractiveDeclineAborts(t *testing.T) {
	f := newInitFixture()
	f.setupMissing()
	f.term.Interactive = true
	f.withStdin("n\n")

	err := runInit(context.Background(), f.app, initDir, false, false)

	require.Error(t, err)
	require.Contains(t, err.Error(), "declined")
	require.Empty(t, f.kc.AddedCerts, "no setup work after a decline")
	require.False(t, f.configWritten(), "config must not be written on decline")
}

func TestInitSetupFailureAborts(t *testing.T) {
	f := newInitFixture()
	f.setupMissing()
	f.term.Interactive = true
	f.withStdin("y\n")
	f.prober.Outcome = sysdep.ProbeUntrusted // post-install verify keeps failing

	err := runInit(context.Background(), f.app, initDir, false, false)

	require.Error(t, err)
	require.Contains(t, err.Error(), "CA verification failed", "setup's actionable error surfaces")
	require.False(t, f.configWritten(), "config must not be written when setup fails")
}

func TestInitKeychainLockedAborts(t *testing.T) {
	f := newInitFixture()
	f.kc.Locked = true // FindCertificate → ErrKeychainLocked → StatusKeychainLocked

	err := runInit(context.Background(), f.app, initDir, false, false)

	require.Error(t, err)
	got := f.out.String()
	require.Contains(t, got, "login keychain is locked", "surface the unlock instruction")
	require.NotContains(t, got, "[y/N]", "no prompt when the keychain is locked")
	require.False(t, f.configWritten(), "config must not be written when the keychain is locked")
}

func TestInitNonInteractiveMissingAborts(t *testing.T) {
	f := newInitFixture()
	f.setupMissing()
	f.term.Interactive = false // no TTY to confirm on

	err := runInit(context.Background(), f.app, initDir, false, false)

	require.Error(t, err)
	require.Empty(t, f.kc.AddedCerts, "never silently sudo in a non-interactive run")
	got := f.out.String()
	require.Contains(t, got, "agent-creance setup", "actionable instruction")
	require.Contains(t, got, "--no-setup", "config-only escape hatch hint")
	require.NotContains(t, got, "[y/N]", "no prompt without a TTY")
	require.False(t, f.configWritten())
}

func TestInitNoSetupSkipsGate(t *testing.T) {
	f := newInitFixture()
	f.setupMissing() // setup is missing, but --no-setup skips the gate entirely

	require.NoError(t, runInit(context.Background(), f.app, initDir, false, true /*noSetup*/))

	require.Empty(t, f.kc.CertLookups, "--no-setup must not even probe the keychain")
	require.Empty(t, f.kc.AddedCerts)
	require.Empty(t, f.prober.Calls)
	require.True(t, f.configWritten())
	// Setup is still pending, so keep pointing the user at it.
	require.Contains(t, f.out.String(), "Next: run `agent-creance setup`, then `agent-creance run`.")
}
