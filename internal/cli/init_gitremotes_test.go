package cli

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// gitRemoteApp builds a minimal App whose FS holds a .git/config with the given body.
func gitRemoteApp(t *testing.T, gitConfig string) *App {
	t.Helper()
	fs := sysdeptest.NewFakeFileSystem()
	if gitConfig != "" {
		fs.Files[filepath.Join(gitRemoteDir, ".git", "config")] = []byte(gitConfig)
	}
	return &App{FS: fs}
}

const gitRemoteDir = "/proj"

func pp(s ...string) *[]string { return &s }

func TestGatherGitRemoteRules_GitHubReadOnly(t *testing.T) {
	app := gitRemoteApp(t, "[remote \"origin\"]\n\turl = https://github.com/foo/bar.git\n")

	res, err := gatherGitRemoteRules(app, gitRemoteDir, false)
	require.NoError(t, err)

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

func TestGatherGitRemoteRules_PushGrantedNoDeny(t *testing.T) {
	app := gitRemoteApp(t, "[remote \"origin\"]\n\turl = https://github.com/foo/bar.git\n")

	res, err := gatherGitRemoteRules(app, gitRemoteDir, true)
	require.NoError(t, err)
	require.NotEmpty(t, res.Allow)
	require.Empty(t, res.Deny)
}

func TestGatherGitRemoteRules_SSHRemoteNoted(t *testing.T) {
	app := gitRemoteApp(t, "[remote \"origin\"]\n\turl = git@github.com:foo/bar.git\n")

	res, err := gatherGitRemoteRules(app, gitRemoteDir, false)
	require.NoError(t, err)
	// Same HTTPS forge hosts as the https case (transport is irrelevant to the rules).
	require.Equal(t, "github.com", res.Allow[0].Host)
	require.Equal(t, "api.github.com", res.Allow[1].Host)
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "non-HTTPS transport")
}

func TestGatherGitRemoteRules_UnknownForge(t *testing.T) {
	app := gitRemoteApp(t, "[remote \"origin\"]\n\turl = https://git.example.com/team/lib.git\n")

	res, err := gatherGitRemoteRules(app, gitRemoteDir, false)
	require.NoError(t, err)
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

func TestGatherGitRemoteRules_TwoRemotesDistinctRepos(t *testing.T) {
	app := gitRemoteApp(t, "[remote \"origin\"]\n\turl = https://github.com/me/fork.git\n[remote \"upstream\"]\n\turl = https://github.com/org/repo.git\n")

	res, err := gatherGitRemoteRules(app, gitRemoteDir, true)
	require.NoError(t, err)
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

func TestGatherGitRemoteRules_NoRemotes(t *testing.T) {
	app := gitRemoteApp(t, "")
	res, err := gatherGitRemoteRules(app, gitRemoteDir, false)
	require.NoError(t, err)
	require.Empty(t, res.Allow)
	require.Empty(t, res.Deny)
	require.Empty(t, res.Notes)
}

func TestGatherGitRemoteRules_UnparseableRemoteSkipped(t *testing.T) {
	app := gitRemoteApp(t, "[remote \"origin\"]\n\turl = https://github.com/onlyorg\n")

	res, err := gatherGitRemoteRules(app, gitRemoteDir, false)
	require.NoError(t, err)
	require.Empty(t, res.Allow)
	require.Len(t, res.Notes, 1)
	require.Contains(t, res.Notes[0], "couldn't parse")
}
