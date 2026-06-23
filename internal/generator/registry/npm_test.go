package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNPMSourceURL(t *testing.T) {
	cases := map[string]string{
		"left-pad":    "https://registry.npmjs.org/left-pad/latest",
		"@types/node": "https://registry.npmjs.org/@types%2fnode/latest",
	}
	for pkg, want := range cases {
		require.Equal(t, want, npmSource{}.url(pkg), "url(%q)", pkg)
	}
}

func TestNPMValidate(t *testing.T) {
	valid := []string{"left-pad", "@types/node", "lodash.merge", "foo_bar", "a", "React"}
	for _, pkg := range valid {
		require.NoError(t, npmSource{}.validate(pkg), "valid %q", pkg)
	}
	invalid := []string{
		"", "left-pad?x", "left pad", ".hidden", "_priv", "a/b",
		"@/x", "@scope/", "@scope/a/b", "evil%2f..", "foo#frag", "foo@1",
	}
	for _, pkg := range invalid {
		require.Error(t, npmSource{}.validate(pkg), "invalid %q", pkg)
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
			name:     "object repository with directory field",
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
			name:     "missing homepage tolerated",
			fixture:  "npm-no-homepage.json",
			homepage: "",
			repo:     "git+https://github.com/example/no-homepage.git",
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
