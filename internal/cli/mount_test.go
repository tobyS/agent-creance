package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func TestMountAddReadWrite(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runMountAdd(f.app, mutProjDir, "./data", true /*rw*/, false))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Equal(t, []string{"./data"}, cfg.Safehouse.AddDirsRW)
	require.Contains(t, f.out.String(), "mounted read-write ./data")
}

func TestMountAddReadOnly(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runMountAdd(f.app, mutProjDir, "~/.config/git", false, true /*ro*/))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Equal(t, []string{"~/.config/git"}, cfg.Safehouse.AddDirsRO)
}

func TestMountAddRWROConflict(t *testing.T) {
	f := newMutateFixture(t)
	err := runMountAdd(f.app, mutProjDir, "./data", true, true)
	require.ErrorContains(t, err, "cannot combine --rw and --ro")
}

func TestMountAddPromptsMode(t *testing.T) {
	f := newMutateFixture(t)
	f.app.Terminal = &sysdeptest.FakeTerminal{Interactive: true}
	f.app.Stdin = strings.NewReader("2\n") // choose read-only

	require.NoError(t, runMountAdd(f.app, mutProjDir, "./shared", false, false))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Equal(t, []string{"./shared"}, cfg.Safehouse.AddDirsRO)
	require.Empty(t, cfg.Safehouse.AddDirsRW)
}

func TestMountAddNonInteractiveNoModeHint(t *testing.T) {
	f := newMutateFixture(t)
	f.app.Terminal = &sysdeptest.FakeTerminal{Interactive: false}
	f.app.Stdin = strings.NewReader("")

	err := runMountAdd(f.app, mutProjDir, "./data", false, false)
	require.ErrorContains(t, err, "--rw or --ro")
}

func TestMountRemove(t *testing.T) {
	f := newMutateFixture(t)
	require.NoError(t, runMountAdd(f.app, mutProjDir, "./data", true, false))

	require.NoError(t, runMountRemove(f.app, mutProjDir, "./data"))
	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.Empty(t, cfg.Safehouse.AddDirsRW)
}

func TestMountRemoveNotFound(t *testing.T) {
	f := newMutateFixture(t)
	err := runMountRemove(f.app, mutProjDir, "./nope")
	require.ErrorContains(t, err, "nothing to remove")
}
