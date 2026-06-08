package cage_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/cage"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/profile"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func TestResolve(t *testing.T) {
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = "/home/test"
	b := cage.New(sysdeptest.NewFakeFileSystem(), paths)

	in, err := b.Resolve(&config.Config{}, state.Layout{Root: "/root"}, 18081)
	require.NoError(t, err)
	require.Equal(t, "/home/test", in.HomeDir)
	require.Equal(t, "/home/test/.mitmproxy/mitmproxy-ca-cert.pem", in.CACertPath)
	require.Equal(t, 18081, in.ProxyPort)
}

func TestResolveHomeDirError(t *testing.T) {
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeErr = errStub
	b := cage.New(sysdeptest.NewFakeFileSystem(), paths)

	_, err := b.Resolve(&config.Config{}, state.Layout{}, 18081)
	require.Error(t, err)
}

func TestPrepareSeedsAndWritesFragment(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	b := cage.New(fsys, sysdeptest.NewFakePathResolver())
	in := prepareInputs()

	require.NoError(t, b.Prepare(in))

	// Sanitized seed: empty object, nothing executable.
	settings := filepath.Join(in.Layout.ClaudeConfigDir(), "settings.json")
	require.Equal(t, "{}\n", string(fsys.Files[settings]))
	require.True(t, fsys.Dirs[in.Layout.ClaudeConfigDir()], "config dir created")

	// Proxy fragment matches the renderer for this port.
	wantFrag, err := profile.RenderProxyFragment(in.ProxyPort)
	require.NoError(t, err)
	require.Equal(t, wantFrag, string(fsys.Files[in.Layout.ProxyProfileSB()]))

	// CA read-grant fragment matches the renderer for the resolved CA path (AC-0034).
	wantCA, err := profile.RenderCAReadFragment(in.CACertPath)
	require.NoError(t, err)
	require.Equal(t, wantCA, string(fsys.Files[in.Layout.CAProfileSB()]))
}

func TestPreparePreservesExistingSettings(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	in := prepareInputs()
	settings := filepath.Join(in.Layout.ClaudeConfigDir(), "settings.json")
	fsys.Files[settings] = []byte(`{"theme":"dark"}`) // agent-written session state

	b := cage.New(fsys, sysdeptest.NewFakePathResolver())
	require.NoError(t, b.Prepare(in))

	require.Equal(t, `{"theme":"dark"}`, string(fsys.Files[settings]),
		"existing settings must be preserved, not reset")
	// The fragment is still (re)written.
	require.NotEmpty(t, fsys.Files[in.Layout.ProxyProfileSB()])
}

func TestPrepareRewritesFragmentOnPortChange(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	b := cage.New(fsys, sysdeptest.NewFakePathResolver())
	in := prepareInputs()

	in.ProxyPort = 18081
	require.NoError(t, b.Prepare(in))
	in.ProxyPort = 22222
	require.NoError(t, b.Prepare(in))

	want, err := profile.RenderProxyFragment(22222)
	require.NoError(t, err)
	require.Equal(t, want, string(fsys.Files[in.Layout.ProxyProfileSB()]),
		"fragment must reflect the latest port")
}

func prepareInputs() cage.Inputs {
	return cage.Inputs{
		Config:     &config.Config{},
		Layout:     state.Layout{Root: "/root"},
		ProxyPort:  18081,
		HomeDir:    "/home/test",
		CACertPath: "/home/test/.mitmproxy/mitmproxy-ca-cert.pem",
	}
}

type stubErr struct{}

func (stubErr) Error() string { return "stub error" }

var errStub = stubErr{}
