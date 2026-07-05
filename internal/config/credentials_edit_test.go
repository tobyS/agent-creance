package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAppendCredentialFreshBlock: adding the first credential synthesizes a top-level
// credentials: block and preserves surrounding comments.
func TestAppendCredentialFreshBlock(t *testing.T) {
	src := `# my project
network:
  egress:
    allow:
      - host: example.com
`
	cred := Credential{Source: "op://Private/GitHub/token", Template: "Bearer {token}"}
	out, changed, err := AppendCredential([]byte(src), "github", cred)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(out), "# my project", "comment preserved")
	require.Contains(t, string(out), "credentials:")

	cfg, err := Parse(out)
	require.NoError(t, err)
	got, ok := cfg.Credentials["github"]
	require.True(t, ok)
	require.Equal(t, "op://Private/GitHub/token", got.Source)
	require.Equal(t, "Bearer {token}", got.Template)
	require.Equal(t, DefaultCredentialHeader, got.Header, "header defaulted on parse")
	require.Len(t, cfg.Network.Egress.Allow, 1, "egress untouched")
}

// TestAppendCredentialIntoExistingBlock: a second credential is appended to an
// existing credentials: mapping without disturbing the first or its comment.
func TestAppendCredentialIntoExistingBlock(t *testing.T) {
	src := `credentials:
  # the github token
  github:
    source: op://Private/GitHub/token
    template: "Bearer {token}"
`
	cred := Credential{Source: "op://Private/PyPI/token", Template: "Basic base64({user}:{token})", Username: "__token__"}
	out, changed, err := AppendCredential([]byte(src), "pypi", cred)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(out), "# the github token", "existing comment preserved")

	cfg, err := Parse(out)
	require.NoError(t, err)
	require.Len(t, cfg.Credentials, 2)
	require.Equal(t, "__token__", cfg.Credentials["pypi"].Username)
}

// TestAppendCredentialCustomHeader: a non-default header is rendered; the default is
// omitted (relying on the parse-time default).
func TestAppendCredentialCustomHeader(t *testing.T) {
	src := "network:\n  egress:\n    allow:\n      - host: example.com\n"
	cred := Credential{Source: "op://Private/Anthropic/key", Template: "{token}", Header: "x-api-key"}
	out, _, err := AppendCredential([]byte(src), "anthropic", cred)
	require.NoError(t, err)
	require.Contains(t, string(out), "header: x-api-key")

	cfg, err := Parse(out)
	require.NoError(t, err)
	require.Equal(t, "x-api-key", cfg.Credentials["anthropic"].Header)
}

// TestAppendCredentialDuplicate: adding a name that already exists is a no-op.
func TestAppendCredentialDuplicate(t *testing.T) {
	src := `credentials:
  github:
    source: op://Private/GitHub/token
    template: "Bearer {token}"
`
	out, changed, err := AppendCredential([]byte(src), "github",
		Credential{Source: "op://other", Template: "{token}"})
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, src, string(out))
}

// TestRemoveCredential: removing an entry deletes just its lines; a sibling and its
// comment survive.
func TestRemoveCredential(t *testing.T) {
	src := `credentials:
  github:
    source: op://Private/GitHub/token
    template: "Bearer {token}"
  # pypi upload token
  pypi:
    source: op://Private/PyPI/token
    template: "Basic base64({user}:{token})"
    username: __token__
`
	out, changed, err := RemoveCredential([]byte(src), "github")
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(out), "# pypi upload token", "sibling comment preserved")

	cfg, err := Parse(out)
	require.NoError(t, err)
	require.Len(t, cfg.Credentials, 1)
	_, ok := cfg.Credentials["github"]
	require.False(t, ok)
	require.Contains(t, cfg.Credentials, "pypi")
}

// TestRemoveCredentialNotFound: removing an absent entry reports ErrNotFound and
// leaves the bytes unchanged.
func TestRemoveCredentialNotFound(t *testing.T) {
	src := `credentials:
  github:
    source: op://Private/GitHub/token
    template: "Bearer {token}"
`
	out, changed, err := RemoveCredential([]byte(src), "nope")
	require.ErrorIs(t, err, ErrNotFound)
	require.False(t, changed)
	require.Equal(t, src, string(out))
}
