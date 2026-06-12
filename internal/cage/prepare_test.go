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

func TestPrepareWritesFragments(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	b := cage.New(fsys, sysdeptest.NewFakePathResolver())
	in := prepareInputs()

	require.NoError(t, b.Prepare(in))

	// The real ~/.claude mount target exists (created when absent, never seeded).
	claudeDir := filepath.Join(in.HomeDir, ".claude")
	require.True(t, fsys.Dirs[claudeDir], "~/.claude created as the mount target")

	// Proxy fragment matches the renderer for this port.
	wantFrag, err := profile.RenderProxyFragment(in.ProxyPort)
	require.NoError(t, err)
	require.Equal(t, wantFrag, string(fsys.Files[in.Layout.ProxyProfileSB()]))

	// CA read-grant fragment matches the renderer for the resolved CA path (AC-0034).
	wantCA, err := profile.RenderCAReadFragment(in.CACertPath)
	require.NoError(t, err)
	require.Equal(t, wantCA, string(fsys.Files[in.Layout.CAProfileSB()]))

	// Keychain + claude-state fragments match the renderers for the home dir (AC-0045).
	wantKC, err := profile.RenderKeychainFragment(in.HomeDir)
	require.NoError(t, err)
	require.Equal(t, wantKC, string(fsys.Files[in.Layout.KeychainProfileSB()]))
	wantCS, err := profile.RenderClaudeStateFragment(in.HomeDir)
	require.NoError(t, err)
	require.Equal(t, wantCS, string(fsys.Files[in.Layout.ClaudeProfileSB()]))
}

func TestPrepareNeverWritesIntoRealClaude(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	in := prepareInputs()
	b := cage.New(fsys, sysdeptest.NewFakePathResolver())
	require.NoError(t, b.Prepare(in))

	// Prepare ensures the dir exists but must not seed/touch any file in it —
	// the host's real config is used as-is (AC-0045).
	claudeDir := filepath.Join(in.HomeDir, ".claude")
	for path := range fsys.Files {
		require.NotContains(t, path, claudeDir,
			"Prepare must not write files into the real ~/.claude")
	}
}

func TestPrepareResolvesHomeForFragments(t *testing.T) {
	// Seatbelt matches kernel-resolved paths (macOS firmlinks), so the home dir
	// embedded in the keychain/claude-state grants must be symlink-resolved.
	fsys := sysdeptest.NewFakeFileSystem()
	paths := sysdeptest.NewFakePathResolver()
	paths.Symlinks["/home/test"] = "/sysvol/home/test"
	b := cage.New(fsys, paths)
	in := prepareInputs()

	require.NoError(t, b.Prepare(in))

	wantKC, err := profile.RenderKeychainFragment("/sysvol/home/test")
	require.NoError(t, err)
	require.Equal(t, wantKC, string(fsys.Files[in.Layout.KeychainProfileSB()]))
	wantCS, err := profile.RenderClaudeStateFragment("/sysvol/home/test")
	require.NoError(t, err)
	require.Equal(t, wantCS, string(fsys.Files[in.Layout.ClaudeProfileSB()]))
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
