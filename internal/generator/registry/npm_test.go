package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNPMSourceURL(t *testing.T) {
	cases := map[string]string{
		"left-pad":    "https://registry.npmjs.org/left-pad",
		"@types/node": "https://registry.npmjs.org/@types%2fnode",
	}
	for pkg, want := range cases {
		require.Equal(t, want, npmSource{}.url(pkg), "url(%q)", pkg)
	}
}

func TestNPMParse(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		homepage string
		repo     string
	}{
		{
			name:     "object repository hoisted to top level",
			fixture:  "npm-left-pad.json",
			homepage: "https://github.com/stevemao/left-pad#readme",
			repo:     "git+ssh://git@github.com/stevemao/left-pad.git",
		},
		{
			name:     "string repository form",
			fixture:  "npm-string-repo.json",
			homepage: "https://stringy.example/",
			repo:     "https://github.com/example/stringy.git",
		},
		{
			name:     "falls back to latest version when top level missing",
			fixture:  "npm-version-fallback.json",
			homepage: "https://hoist-lagged.example/docs",
			repo:     "git+https://github.com/example/hoist-lagged.git",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("testdata", tc.fixture))
			require.NoError(t, err)

			md, err := npmSource{}.parse(body)
			require.NoError(t, err)
			require.Equal(t, tc.homepage, md.Homepage, "homepage")
			require.Equal(t, tc.repo, md.Repository, "repository")
		})
	}
}

func TestNPMParseInvalidJSON(t *testing.T) {
	_, err := npmSource{}.parse([]byte("not json"))
	require.Error(t, err)
}

func TestNPMRepositoryUnmarshalNullAndOther(t *testing.T) {
	var r npmRepository
	require.NoError(t, r.UnmarshalJSON([]byte("null")))
	require.Empty(t, r.URL)

	// A non-string, non-object shape yields an empty URL, not an error.
	require.NoError(t, r.UnmarshalJSON([]byte("123")))
	require.Empty(t, r.URL)
}
