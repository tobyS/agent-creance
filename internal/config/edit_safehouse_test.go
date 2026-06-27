package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendDirFlowListPreservesComments(t *testing.T) {
	src := `safehouse:
  add_dirs_rw: [.]  # mount the project
  add_dirs_ro: [~/.config/git]
`
	got, changed, err := AppendDir([]byte(src), "./data", true)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(got), "# mount the project", "the key-line comment is not on add_dirs_rw")

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{".", "./data"}, cfg.Safehouse.AddDirsRW)
	require.Equal(t, []string{"~/.config/git"}, cfg.Safehouse.AddDirsRO, "the ro list is untouched")
}

func TestAppendDirEmptyFlowList(t *testing.T) {
	src := `safehouse:
  add_dirs_ro: []
`
	got, changed, err := AppendDir([]byte(src), "~/.cache/foo", false)
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{"~/.cache/foo"}, cfg.Safehouse.AddDirsRO)
}

func TestAppendDirBlockListAppendsItem(t *testing.T) {
	src := `safehouse:
  add_dirs_rw:
    - .            # project root
    - ./build
`
	got, changed, err := AppendDir([]byte(src), "./data", true)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(got), "# project root", "existing block comment survives")
	require.Contains(t, string(got), `    - "./data"`, "appended as a block item at the same indent (slashed path is quoted, as include does)")

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{".", "./build", "./data"}, cfg.Safehouse.AddDirsRW)
}

func TestAppendDirSynthesizesSafehouse(t *testing.T) {
	src := `agent:
  command: [claude]
`
	got, changed, err := AppendDir([]byte(src), "./data", true)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(got), "agent:", "original content preserved")

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{"./data"}, cfg.Safehouse.AddDirsRW)
}

func TestAppendDirSafehouseWithoutList(t *testing.T) {
	src := `safehouse:
  enable: [shell-init]
`
	got, changed, err := AppendDir([]byte(src), "~/.config/git", false)
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{"shell-init"}, cfg.Safehouse.Enable, "enable untouched")
	require.Equal(t, []string{"~/.config/git"}, cfg.Safehouse.AddDirsRO)
}

func TestAppendDirDuplicateNoOp(t *testing.T) {
	src := `safehouse:
  add_dirs_rw: [., ./data]
`
	got, changed, err := AppendDir([]byte(src), "./data", true)
	require.NoError(t, err)
	require.False(t, changed, "same path already present is a no-op")
	require.Equal(t, src, string(got))
}

func TestAppendDirSamePathDifferentListIsNotDuplicate(t *testing.T) {
	// "." in the rw list does not block adding "." to the ro list — they are distinct.
	src := `safehouse:
  add_dirs_rw: [.]
`
	got, changed, err := AppendDir([]byte(src), ".", false)
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{"."}, cfg.Safehouse.AddDirsRW)
	require.Equal(t, []string{"."}, cfg.Safehouse.AddDirsRO)
}

func TestAppendDirEmptyFile(t *testing.T) {
	got, changed, err := AppendDir([]byte(""), "./data", true)
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{"./data"}, cfg.Safehouse.AddDirsRW)
}

func TestAppendDirUnparseable(t *testing.T) {
	_, _, err := AppendDir([]byte("safehouse: [unterminated"), "./data", true)
	require.Error(t, err)
}
