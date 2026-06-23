package gitremote

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

func TestParseConfig(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []Remote
	}{
		{
			name: "single origin https",
			text: "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = https://github.com/foo/bar.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n",
			want: []Remote{{Name: "origin", URL: "https://github.com/foo/bar.git"}},
		},
		{
			name: "scp-like ssh url",
			text: "[remote \"origin\"]\n\turl = git@github.com:tobyS/agent-creance.git\n",
			want: []Remote{{Name: "origin", URL: "git@github.com:tobyS/agent-creance.git"}},
		},
		{
			name: "multiple remotes keep file order",
			text: "[remote \"origin\"]\n\turl = https://github.com/me/fork.git\n[remote \"upstream\"]\n\turl = https://github.com/org/repo.git\n[remote \"mirror\"]\n\turl = https://gitlab.com/g/p.git\n",
			want: []Remote{
				{Name: "origin", URL: "https://github.com/me/fork.git"},
				{Name: "upstream", URL: "https://github.com/org/repo.git"},
				{Name: "mirror", URL: "https://gitlab.com/g/p.git"},
			},
		},
		{
			name: "no remote sections",
			text: "[core]\n\tbare = false\n[branch \"main\"]\n\tremote = origin\n",
			want: nil,
		},
		{
			name: "comments and blank lines ignored",
			text: "# a comment\n\n; another\n[remote \"origin\"]\n\t# inline-ish full-line comment\n\turl = https://github.com/foo/bar.git\n",
			want: []Remote{{Name: "origin", URL: "https://github.com/foo/bar.git"}},
		},
		{
			name: "remote with only pushurl is skipped (no url)",
			text: "[remote \"origin\"]\n\tpushurl = https://github.com/foo/bar.git\n",
			want: nil,
		},
		{
			name: "url key is case-insensitive",
			text: "[remote \"origin\"]\n\tURL = https://github.com/foo/bar.git\n",
			want: []Remote{{Name: "origin", URL: "https://github.com/foo/bar.git"}},
		},
		{
			name: "repeated url for a name keeps last",
			text: "[remote \"origin\"]\n\turl = https://github.com/old/repo.git\n[remote \"origin\"]\n\turl = https://github.com/new/repo.git\n",
			want: []Remote{{Name: "origin", URL: "https://github.com/new/repo.git"}},
		},
		{
			name: "url after a non-remote section is ignored",
			text: "[remote \"origin\"]\n\turl = https://github.com/foo/bar.git\n[http]\n\turl = https://example.com/decoy\n",
			want: []Remote{{Name: "origin", URL: "https://github.com/foo/bar.git"}},
		},
		{
			name: "empty url value is skipped",
			text: "[remote \"origin\"]\n\turl =\n",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, parseConfig(tc.text))
		})
	}
}

func TestDetect_ReadsGitConfig(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	require.NoError(t, fsys.MkdirAll(filepath.Join("/proj", ".git"), 0o755))
	require.NoError(t, fsys.WriteFile(filepath.Join("/proj", ".git", "config"),
		[]byte("[remote \"origin\"]\n\turl = git@github.com:tobyS/agent-creance.git\n"), 0o644))

	got, err := Detect(fsys, "/proj")
	require.NoError(t, err)
	require.Equal(t, []Remote{{Name: "origin", URL: "git@github.com:tobyS/agent-creance.git"}}, got)
}

func TestDetect_MissingConfigIsNoError(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	got, err := Detect(fsys, "/proj")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestDetect_ReadErrorSurfaced(t *testing.T) {
	fsys := sysdeptest.NewFakeFileSystem()
	sentinel := errors.New("boom")
	fsys.Errs[filepath.Join("/proj", ".git", "config")] = sentinel

	_, err := Detect(fsys, "/proj")
	require.ErrorIs(t, err, sentinel)
}
