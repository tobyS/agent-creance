package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveRuleWholeRulePreservesNeighbours(t *testing.T) {
	src := `network:
  egress:
    allow:
      # keep me: this leads the github rule
      - host: api.github.com
        paths: ["/repos/"]
        methods: [GET]
      - host: react.dev   # trailing comment on the survivor
`
	got, changed, err := RemoveRule([]byte(src), AllowList, "api.github.com", "")
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Len(t, cfg.Network.Egress.Allow, 1)
	require.Equal(t, "react.dev", cfg.Network.Egress.Allow[0].Host)
	require.Contains(t, string(got), "# trailing comment on the survivor")
	// The standalone comment that led the removed rule belongs to it visually but is
	// not part of its node span, so it survives (conservative removal).
	require.Contains(t, string(got), "# keep me")
}

func TestRemoveRuleSinglePathFromMultiPath(t *testing.T) {
	src := `network:
  egress:
    allow:
      - host: api.github.com
        paths: ["/repos/", "/user/"]
`
	got, changed, err := RemoveRule([]byte(src), AllowList, "api.github.com", "/repos/")
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Len(t, cfg.Network.Egress.Allow, 1)
	require.Equal(t, []string{"/user/"}, *cfg.Network.Egress.Allow[0].Paths)
}

func TestRemoveRuleLastPathDropsWholeRule(t *testing.T) {
	src := `network:
  egress:
    allow:
      - host: api.github.com
        paths: ["/repos/"]
      - host: react.dev
`
	got, changed, err := RemoveRule([]byte(src), AllowList, "api.github.com", "/repos/")
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Len(t, cfg.Network.Egress.Allow, 1)
	require.Equal(t, "react.dev", cfg.Network.Egress.Allow[0].Host, "the whole rule was dropped, not left with empty paths")
}

func TestRemoveRuleFromDenyList(t *testing.T) {
	src := `network:
  egress:
    deny_always:
      - host: w3schools.com
        reason: "low quality"
`
	got, changed, err := RemoveRule([]byte(src), DenyList, "w3schools.com", "")
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Empty(t, cfg.Network.Egress.DenyAlways)
}

func TestRemoveRuleNotFound(t *testing.T) {
	src := `network:
  egress:
    allow:
      - host: react.dev
`
	_, changed, err := RemoveRule([]byte(src), AllowList, "missing.com", "")
	require.ErrorIs(t, err, ErrNotFound)
	require.False(t, changed)

	// Path absent from an existing host's rule is also not found.
	_, _, err = RemoveRule([]byte(src), AllowList, "react.dev", "/nope/")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestRemoveRuleLeavesOtherListUntouched(t *testing.T) {
	src := `network:
  egress:
    allow:
      - host: react.dev
    deny_always:
      - host: w3schools.com
        reason: "x"
`
	got, _, err := RemoveRule([]byte(src), AllowList, "react.dev", "")
	require.NoError(t, err)
	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Empty(t, cfg.Network.Egress.Allow)
	require.Len(t, cfg.Network.Egress.DenyAlways, 1, "deny_always untouched")
}

func TestRemoveHostServiceByPort(t *testing.T) {
	src := `network:
  host_services:
    - web:3000   # dev server
    - api:8080
`
	got, changed, err := RemoveHostService([]byte(src), 3000)
	require.NoError(t, err)
	require.True(t, changed)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []HostService{{Label: "api", Port: 8080}}, cfg.Network.HostServices)
}

func TestRemoveHostServiceNotFound(t *testing.T) {
	src := `network:
  host_services:
    - web:3000
`
	_, changed, err := RemoveHostService([]byte(src), 9999)
	require.ErrorIs(t, err, ErrNotFound)
	require.False(t, changed)
}

func TestRemoveDirFromFlowList(t *testing.T) {
	src := `safehouse:
  add_dirs_rw: [., ./data]  # mounts
`
	got, rw, ro, changed, err := RemoveDir([]byte(src), "./data")
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, rw)
	require.False(t, ro)
	require.Contains(t, string(got), "# mounts", "inline comment survives the flow rewrite")

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{"."}, cfg.Safehouse.AddDirsRW)
}

func TestRemoveDirFromBlockList(t *testing.T) {
	src := `safehouse:
  add_dirs_rw:
    - .          # project
    - ./data
`
	got, _, _, changed, err := RemoveDir([]byte(src), "./data")
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(got), "# project", "block comment survives")

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{"."}, cfg.Safehouse.AddDirsRW)
}

func TestRemoveDirFromBothLists(t *testing.T) {
	src := `safehouse:
  add_dirs_rw: [., ./shared]
  add_dirs_ro: [./shared, ~/.config/git]
`
	got, rw, ro, changed, err := RemoveDir([]byte(src), "./shared")
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, rw)
	require.True(t, ro)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Equal(t, []string{"."}, cfg.Safehouse.AddDirsRW)
	require.Equal(t, []string{"~/.config/git"}, cfg.Safehouse.AddDirsRO)
}

func TestRemoveDirNotFound(t *testing.T) {
	src := `safehouse:
  add_dirs_rw: [.]
`
	_, _, _, changed, err := RemoveDir([]byte(src), "./nope")
	require.ErrorIs(t, err, ErrNotFound)
	require.False(t, changed)
}

func TestRemoveDirLastEntryLeavesEmptyList(t *testing.T) {
	src := `safehouse:
  add_dirs_ro: [~/.config/git]
`
	got, _, ro, changed, err := RemoveDir([]byte(src), "~/.config/git")
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, ro)

	cfg, err := Parse(got)
	require.NoError(t, err)
	require.Empty(t, cfg.Safehouse.AddDirsRO)
}

func TestRemoveErrNotFoundIsCheckable(t *testing.T) {
	require.True(t, errors.Is(ErrNotFound, ErrNotFound))
}
