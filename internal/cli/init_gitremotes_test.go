package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/gitremote"
)

func pp(s ...string) *[]string { return &s }

func TestBuildGitRemoteRules_GitHubReadOnly(t *testing.T) {
	res := buildGitRemoteRules([]gitremote.Remote{{Name: "origin", URL: "https://github.com/foo/bar.git"}}, false)

	reason := "project git remote (origin)"
	require.Equal(t, []config.Rule{
		{Host: "github.com", Paths: pp("/foo/bar/", "/foo/bar.git/"), Reason: reason},
		{Host: "api.github.com", Paths: pp("/repos/foo/bar/"), Reason: reason},
		{Host: "raw.githubusercontent.com", Paths: pp("/foo/bar/"), Reason: reason},
		{Host: "codeload.github.com", Paths: pp("/foo/bar/"), Reason: reason},
		{Host: "foo.github.io", Paths: pp("/bar/"), Reason: reason},
		{Host: "objects.githubusercontent.com", Reason: reason},
	}, res.Allow)

	require.Equal(t, []config.Rule{{
		Host:   "github.com",
		Paths:  pp("/foo/bar/git-receive-pack", "/foo/bar.git/git-receive-pack"),
		Reason: "read-only: push (git-receive-pack) to origin blocked; re-run init with --git-push to allow",
	}}, res.Deny)
	require.Empty(t, res.Notes)
}

func TestBuildGitRemoteRules_PushGrantedNoDeny(t *testing.T) {
	res := buildGitRemoteRules([]gitremote.Remote{{Name: "origin", URL: "https://github.com/foo/bar.git"}}, true)
	require.NotEmpty(t, res.Allow)
	require.Empty(t, res.Deny)
}

func TestBuildGitRemoteRules_SSHRemoteNoted(t *testing.T) {
	res := buildGitRemoteRules([]gitremote.Remote{{Name: "origin", URL: "git@github.com:foo/bar.git"}}, false)
	// Same HTTPS forge hosts as the https case (transport is irrelevant to the rules).
	require.Equal(t, "github.com", res.Allow[0].Host)
	require.Equal(t, "api.github.com", res.Allow[1].Host)
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "non-HTTPS transport")
}

func TestBuildGitRemoteRules_UnknownForge(t *testing.T) {
	res := buildGitRemoteRules([]gitremote.Remote{{Name: "origin", URL: "https://git.example.com/team/lib.git"}}, false)
	// One bare repo-host allow covering both path forms, no companions.
	require.Equal(t, []config.Rule{{
		Host:   "git.example.com",
		Paths:  pp("/team/lib/", "/team/lib.git/"),
		Reason: "project git remote (origin)",
	}}, res.Allow)
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "not a known forge")
	require.Len(t, res.Deny, 1)
}

func TestBuildGitRemoteRules_TwoRemotesDistinctRepos(t *testing.T) {
	res := buildGitRemoteRules([]gitremote.Remote{
		{Name: "origin", URL: "https://github.com/me/fork.git"},
		{Name: "upstream", URL: "https://github.com/org/repo.git"},
	}, true)
	// Both repos' github.com rules present (distinct paths, not collapsed by dedupe).
	var ghPaths [][]string
	for _, r := range res.Allow {
		if r.Host == "github.com" {
			ghPaths = append(ghPaths, *r.Paths)
		}
	}
	require.Equal(t, [][]string{
		{"/me/fork/", "/me/fork.git/"},
		{"/org/repo/", "/org/repo.git/"},
	}, ghPaths)
}

func TestBuildGitRemoteRules_NoRemotes(t *testing.T) {
	res := buildGitRemoteRules(nil, false)
	require.Empty(t, res.Allow)
	require.Empty(t, res.Deny)
	require.Empty(t, res.Notes)
}

func TestBuildGitRemoteRules_UnparseableRemoteSkipped(t *testing.T) {
	res := buildGitRemoteRules([]gitremote.Remote{{Name: "origin", URL: "https://github.com/onlyorg"}}, false)
	require.Empty(t, res.Allow)
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "couldn't parse")
}
