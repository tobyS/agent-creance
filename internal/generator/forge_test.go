package generator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/policy"
)

func TestNormalizeRepoURL(t *testing.T) {
	cases := []struct {
		name            string
		raw             string
		host, org, repo string
		ok              bool
	}{
		{"git+https with .git", "git+https://github.com/facebook/react.git", "github.com", "facebook", "react", true},
		{"plain https", "https://github.com/facebook/react", "github.com", "facebook", "react", true},
		{"git scheme", "git://github.com/a/b.git", "github.com", "a", "b", true},
		{"scp-like ssh", "git@github.com:a/b.git", "github.com", "a", "b", true},
		{"ssh url", "ssh://git@gitlab.com/a/b.git", "gitlab.com", "a", "b", true},
		{"trailing slash", "https://github.com/a/b/", "github.com", "a", "b", true},
		{"fragment", "https://github.com/a/b#readme", "github.com", "a", "b", true},
		{"uppercase host lowercased", "https://GitHub.com/a/b", "github.com", "a", "b", true},
		{"non-forge host", "https://example.com/x/y", "example.com", "x", "y", true},
		{"deeper path keeps first two", "https://gitlab.com/group/sub/proj", "gitlab.com", "group", "sub", true},
		{"empty", "", "", "", "", false},
		{"no host", "not a url", "", "", "", false},
		{"single segment", "https://github.com/onlyorg", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, org, repo, ok := normalizeRepoURL(tc.raw)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.host, host)
			require.Equal(t, tc.org, org)
			require.Equal(t, tc.repo, repo)
		})
	}
}

func TestRepositoryRules_GitHubCompanionSet(t *testing.T) {
	got := repositoryRules("git+https://github.com/facebook/react.git", "generated:package_json:react")

	want := []Rule{
		{Rule: policy.Rule{Host: "github.com", Paths: []string{"/facebook/react/"}}, Source: "generated:package_json:react"},
		{Rule: policy.Rule{Host: "raw.githubusercontent.com", Paths: []string{"/facebook/react/"}}, Source: "generated:package_json:react"},
		{Rule: policy.Rule{Host: "codeload.github.com", Paths: []string{"/facebook/react/"}}, Source: "generated:package_json:react"},
		{Rule: policy.Rule{Host: "facebook.github.io", Paths: []string{"/react/"}}, Source: "generated:package_json:react"},
		{Rule: policy.Rule{Host: "objects.githubusercontent.com"}, Source: "generated:package_json:react", LowerTrust: true},
	}
	require.Equal(t, want, got)
}

func TestRepositoryRules_GitLab(t *testing.T) {
	got := repositoryRules("https://gitlab.com/group/proj", "generated:composer_json:group/proj")

	want := []Rule{
		{Rule: policy.Rule{Host: "gitlab.com", Paths: []string{"/group/proj/"}}, Source: "generated:composer_json:group/proj"},
		{Rule: policy.Rule{Host: "group.gitlab.io", Paths: []string{"/proj/"}}, Source: "generated:composer_json:group/proj"},
	}
	require.Equal(t, want, got)
}

func TestRepositoryRules_NonForge(t *testing.T) {
	got := repositoryRules("https://git.sr.ht/~user/lib", "generated:package_json:lib")
	want := []Rule{
		{Rule: policy.Rule{Host: "git.sr.ht", Paths: []string{"/~user/lib/"}}, Source: "generated:package_json:lib"},
	}
	require.Equal(t, want, got)
}

func TestRepositoryRules_NoRuleWhenUnparseable(t *testing.T) {
	require.Nil(t, repositoryRules("", "src"))
	require.Nil(t, repositoryRules("https://github.com/onlyorg", "src"))
}
