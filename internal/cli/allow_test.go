package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const (
	mutProjDir  = "/proj"
	mutHomeDir  = "/home/user"
	seededAllow = "network:\n  egress:\n    allow:\n      - host: seed.example\n"
)

type mutateFixture struct {
	app    *App
	fs     *sysdeptest.FakeFileSystem
	paths  *sysdeptest.FakePathResolver
	out    *bytes.Buffer
	layout state.Layout
}

func newMutateFixture(t *testing.T) *mutateFixture {
	t.Helper()
	fs := sysdeptest.NewFakeFileSystem()
	fs.Files[mutProjDir+"/.agent-creance.yaml"] = []byte(seededAllow)

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = mutHomeDir

	out := &bytes.Buffer{}
	app := &App{
		Stdout: out,
		Stderr: &bytes.Buffer{},
		FS:     fs,
		Paths:  paths,
		Clock:  sysdeptest.NewFakeClock(time.Unix(0, 0)),
		HTTP:   sysdeptest.NewFakeHTTPGetter(),
		Flock:  sysdeptest.NewFakeFlock(),
	}
	layout, err := state.New(paths).Resolve(mutProjDir)
	require.NoError(t, err)
	return &mutateFixture{app: app, fs: fs, paths: paths, out: out, layout: layout}
}

func (f *mutateFixture) file(t *testing.T, path string) []byte {
	t.Helper()
	b, ok := f.fs.Files[path]
	require.True(t, ok, "expected a file at %s; have: %v", path, keys(f.fs.Files))
	return b
}

func (f *mutateFixture) projectConfig(t *testing.T) []byte {
	return f.file(t, mutProjDir+"/.agent-creance.yaml")
}

func (f *mutateFixture) policyJSON(t *testing.T) []byte {
	return f.file(t, f.layout.PolicyJSON())
}

func findHost(t *testing.T, rules []config.Rule, host string) config.Rule {
	t.Helper()
	for _, r := range rules {
		if r.Host == host {
			return r
		}
	}
	t.Fatalf("host %q not found in rules", host)
	return config.Rule{}
}

func TestAllowProjectFile(t *testing.T) {
	f := newMutateFixture(t)

	require.NoError(t, runAllow(context.Background(), f.app, mutProjDir, "api.github.com/repos/foo/", false, false))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	r := findHost(t, cfg.Network.Egress.Allow, "api.github.com")
	require.NotNil(t, r.Paths)
	require.Equal(t, []string{"/repos/foo/"}, *r.Paths)

	// The mutation recompiled: the new rule is in policy.json (stronger than an mtime
	// check, and exactly what the proxy hot-reloads).
	require.Contains(t, string(f.policyJSON(t)), "api.github.com")
	require.Contains(t, f.out.String(), "policy recompiled")
}

func TestAllowBareHostNoPaths(t *testing.T) {
	f := newMutateFixture(t)

	require.NoError(t, runAllow(context.Background(), f.app, mutProjDir, "example.com", false, false))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Nil(t, findHost(t, cfg.Network.Egress.Allow, "example.com").Paths)
}

func TestAllowGlobalFile(t *testing.T) {
	f := newMutateFixture(t)
	globalPath := mutHomeDir + "/.config/agent-creance.yaml"

	require.NoError(t, runAllow(context.Background(), f.app, mutProjDir, "example.com", false /*once*/, true /*global*/))

	gcfg, err := config.Parse(f.file(t, globalPath))
	require.NoError(t, err)
	findHost(t, gcfg.Network.Egress.Allow, "example.com")

	// The project file is untouched — the rule landed in the global, not the project.
	require.Equal(t, seededAllow, string(f.projectConfig(t)))
}

func TestAllowOnceWritesOverlayNotProject(t *testing.T) {
	f := newMutateFixture(t)

	require.NoError(t, runAllow(context.Background(), f.app, mutProjDir, "docs.example/v2/", true /*once*/, false))

	ocfg, err := config.Parse(f.file(t, f.layout.SessionOverlay()))
	require.NoError(t, err)
	findHost(t, ocfg.Network.Egress.Allow, "docs.example")

	// AC criterion: a --once rule never touches the committed config.
	require.Equal(t, seededAllow, string(f.projectConfig(t)))
	require.NotContains(t, string(f.projectConfig(t)), "docs.example")
}

func TestAllowOnceAndGlobalConflict(t *testing.T) {
	f := newMutateFixture(t)
	err := runAllow(context.Background(), f.app, mutProjDir, "example.com", true, true)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot combine --once and --global")
}

func TestAllowDuplicateIsNoOp(t *testing.T) {
	f := newMutateFixture(t)

	require.NoError(t, runAllow(context.Background(), f.app, mutProjDir, "dup.example", false, false))
	after := string(f.projectConfig(t))
	f.out.Reset()

	// Second identical allow: reported no-op, file bytes unchanged.
	require.NoError(t, runAllow(context.Background(), f.app, mutProjDir, "dup.example", false, false))
	require.Equal(t, after, string(f.projectConfig(t)))
	require.Contains(t, f.out.String(), "already allowed")
}
