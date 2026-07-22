package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseMintedGitHubApp: a github_app: credential parses, keeps its non-secret
// params, defaults the header, and carries no source.
func TestParseMintedGitHubApp(t *testing.T) {
	src := `credentials:
  gh-app:
    template: "Bearer {token}"
    github_app:
      key: keychain://agent-creance/ghapp-key
      client_id: "Iv1.0123456789abcdef"
      repo: tobyS/agent-creance
      permissions:
        contents: read
        issues: write
`
	cfg, err := Parse([]byte(src))
	require.NoError(t, err)
	cred, ok := cfg.Credentials["gh-app"]
	require.True(t, ok)
	require.True(t, cred.IsMinted())
	require.Empty(t, cred.Source)
	require.Equal(t, DefaultCredentialHeader, cred.Header, "header defaulted on parse")
	require.Equal(t, "Bearer {token}", cred.Template)
	require.NotNil(t, cred.GitHubApp)
	require.Nil(t, cred.OAuth2)
	require.Equal(t, "keychain://agent-creance/ghapp-key", cred.GitHubApp.Key)
	require.Equal(t, "Iv1.0123456789abcdef", cred.GitHubApp.ClientID)
	require.Equal(t, "tobyS/agent-creance", cred.GitHubApp.Repo)
	require.Equal(t, map[string]string{"contents": "read", "issues": "write"}, cred.GitHubApp.Permissions)
}

// TestParseMintedOAuth2Defaults: an oauth2: credential parses and picks up the
// Google token-endpoint and drive.file scope defaults.
func TestParseMintedOAuth2Defaults(t *testing.T) {
	src := `credentials:
  drive:
    template: "Bearer {token}"
    oauth2:
      refresh_token: keychain://agent-creance/drive-refresh
      client_id: "1234.apps.googleusercontent.com"
`
	cfg, err := Parse([]byte(src))
	require.NoError(t, err)
	cred, ok := cfg.Credentials["drive"]
	require.True(t, ok)
	require.True(t, cred.IsMinted())
	require.Empty(t, cred.Source)
	require.NotNil(t, cred.OAuth2)
	require.Nil(t, cred.GitHubApp)
	require.Equal(t, DefaultOAuth2TokenEndpoint, cred.OAuth2.TokenEndpoint, "endpoint defaulted")
	require.Equal(t, []string{DefaultOAuth2Scope}, cred.OAuth2.Scopes, "scope defaulted")
}

// TestParseMintedOAuth2ExplicitOverrides: explicit endpoint/scopes are not overwritten
// by the defaults.
func TestParseMintedOAuth2ExplicitOverrides(t *testing.T) {
	src := `credentials:
  custom:
    template: "Bearer {token}"
    oauth2:
      refresh_token: op://Private/custom/refresh
      client_id: "abc"
      token_endpoint: https://example.test/token
      scopes:
        - https://example.test/scope.a
        - https://example.test/scope.b
`
	cfg, err := Parse([]byte(src))
	require.NoError(t, err)
	cred := cfg.Credentials["custom"]
	require.Equal(t, "https://example.test/token", cred.OAuth2.TokenEndpoint)
	require.Equal(t, []string{"https://example.test/scope.a", "https://example.test/scope.b"}, cred.OAuth2.Scopes)
}

// TestValidateMintedCredentials exercises the pass/fail branches of the minted-form
// validation without golden coupling.
func TestValidateMintedCredentials(t *testing.T) {
	cases := []struct {
		name         string
		yaml         string
		wantErr      bool
		wantContains string
	}{
		{
			name: "valid github_app",
			yaml: "credentials:\n  gh:\n    template: \"Bearer {token}\"\n    github_app:\n      key: keychain://svc/key\n      client_id: cid\n      repo: owner/name\n      permissions:\n        contents: read\n",
		},
		{
			name: "valid oauth2",
			yaml: "credentials:\n  d:\n    template: \"Bearer {token}\"\n    oauth2:\n      refresh_token: keychain://svc/rt\n      client_id: cid\n",
		},
		{
			name:         "no form set",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n",
			wantErr:      true,
			wantContains: "defines no form",
		},
		{
			name:         "two forms set (source + github_app)",
			yaml:         "credentials:\n  x:\n    source: op://a/b\n    template: \"Bearer {token}\"\n    github_app:\n      key: keychain://svc/key\n      client_id: cid\n      repo: owner/name\n",
			wantErr:      true,
			wantContains: "more than one form",
		},
		{
			name:         "two forms set (github_app + oauth2)",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    github_app:\n      key: keychain://svc/key\n      client_id: cid\n      repo: owner/name\n    oauth2:\n      refresh_token: keychain://svc/rt\n      client_id: cid\n",
			wantErr:      true,
			wantContains: "more than one form",
		},
		{
			name:         "github_app missing key",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    github_app:\n      client_id: cid\n      repo: owner/name\n",
			wantErr:      true,
			wantContains: "github_app is missing a key",
		},
		{
			name:         "github_app bad key ref",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    github_app:\n      key: not-a-ref\n      client_id: cid\n      repo: owner/name\n",
			wantErr:      true,
			wantContains: "invalid key",
		},
		{
			name:         "github_app missing client_id",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    github_app:\n      key: keychain://svc/key\n      repo: owner/name\n",
			wantErr:      true,
			wantContains: "missing a client_id",
		},
		{
			name:         "github_app repo not owner/name",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    github_app:\n      key: keychain://svc/key\n      client_id: cid\n      repo: justname\n",
			wantErr:      true,
			wantContains: "invalid repo",
		},
		{
			name:         "github_app unknown permission level",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    github_app:\n      key: keychain://svc/key\n      client_id: cid\n      repo: owner/name\n      permissions:\n        contents: sudo\n",
			wantErr:      true,
			wantContains: `unknown level "sudo"`,
		},
		{
			name:         "oauth2 missing refresh_token",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    oauth2:\n      client_id: cid\n",
			wantErr:      true,
			wantContains: "missing a refresh_token",
		},
		{
			name:         "oauth2 bad refresh_token ref",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    oauth2:\n      refresh_token: nope\n      client_id: cid\n",
			wantErr:      true,
			wantContains: "invalid refresh_token",
		},
		{
			name:         "oauth2 non-https token_endpoint",
			yaml:         "credentials:\n  x:\n    template: \"Bearer {token}\"\n    oauth2:\n      refresh_token: keychain://svc/rt\n      client_id: cid\n      token_endpoint: http://insecure.test/token\n",
			wantErr:      true,
			wantContains: "invalid token_endpoint",
		},
		{
			name:         "minted still needs a template",
			yaml:         "credentials:\n  x:\n    github_app:\n      key: keychain://svc/key\n      client_id: cid\n      repo: owner/name\n",
			wantErr:      true,
			wantContains: "missing a template",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantContains != "" {
					require.Contains(t, err.Error(), tc.wantContains)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestAppendCredentialGitHubApp round-trips a minted github_app credential through the
// text-splice writer: rendered YAML re-parses to the same credential.
func TestAppendCredentialGitHubApp(t *testing.T) {
	src := "network:\n  egress:\n    allow:\n      - host: api.github.com\n"
	cred := Credential{
		Template: "Bearer {token}",
		GitHubApp: &GitHubAppMint{
			Key:         "keychain://agent-creance/ghapp-key",
			ClientID:    "Iv1.0123456789abcdef",
			Repo:        "tobyS/agent-creance",
			Permissions: map[string]string{"contents": "read", "issues": "write"},
		},
	}
	out, changed, err := AppendCredential([]byte(src), "gh-app", cred)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(out), "github_app:")
	require.Contains(t, string(out), "permissions:")

	cfg, err := Parse(out)
	require.NoError(t, err)
	got := cfg.Credentials["gh-app"]
	require.NotNil(t, got.GitHubApp)
	require.Equal(t, cred.GitHubApp.Key, got.GitHubApp.Key)
	require.Equal(t, cred.GitHubApp.Repo, got.GitHubApp.Repo)
	require.Equal(t, cred.GitHubApp.Permissions, got.GitHubApp.Permissions)
	require.Len(t, cfg.Network.Egress.Allow, 1, "egress untouched")
}

// TestAppendCredentialOAuth2 round-trips a minted oauth2 credential (defaulted
// endpoint/scope) through the writer.
func TestAppendCredentialOAuth2(t *testing.T) {
	src := "network:\n  egress:\n    allow:\n      - host: www.googleapis.com\n"
	cred := Credential{
		Template: "Bearer {token}",
		OAuth2: &OAuth2Mint{
			RefreshToken:  "keychain://agent-creance/drive-refresh",
			ClientID:      "1234.apps.googleusercontent.com",
			TokenEndpoint: DefaultOAuth2TokenEndpoint,
			Scopes:        []string{DefaultOAuth2Scope},
		},
	}
	out, changed, err := AppendCredential([]byte(src), "drive", cred)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(out), "oauth2:")
	require.Contains(t, string(out), "scopes:")

	cfg, err := Parse(out)
	require.NoError(t, err)
	got := cfg.Credentials["drive"]
	require.NotNil(t, got.OAuth2)
	require.Equal(t, cred.OAuth2.RefreshToken, got.OAuth2.RefreshToken)
	require.Equal(t, DefaultOAuth2TokenEndpoint, got.OAuth2.TokenEndpoint)
	require.Equal(t, []string{DefaultOAuth2Scope}, got.OAuth2.Scopes)
}
