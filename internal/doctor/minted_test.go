package doctor

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedProjectConfig writes .agent-creance.yaml at the fake cwd the loader reads.
func (h *checkerHarness) seedProjectConfig(yaml string) {
	h.fs.Files[filepath.Join(checkerCwd, ".agent-creance.yaml")] = []byte(yaml)
}

const mintedConfig = `credentials:
  gh-app:
    template: "Bearer {token}"
    github_app:
      key: keychain://agent-creance/ghapp-key
      client_id: Iv1.example
      repo: tobyS/agent-creance
      permissions:
        contents: read
  drive:
    template: "Bearer {token}"
    oauth2:
      refresh_token: keychain://agent-creance/drive-refresh
      client_id: 1234.apps.googleusercontent.com
network:
  egress:
    allow:
      - host: api.github.com
        inject: gh-app
      - host: www.googleapis.com
        inject: drive
`

func TestRun_MintedCredentialsUnauthorizedWarnsButNotActionable(t *testing.T) {
	h := newCheckerHarness().withCA()
	h.seedProjectConfig(mintedConfig)
	// The app key exists in the keychain; the Drive refresh token does not.
	h.kc.WithItem("agent-creance", "ghapp-key", "PEM")

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)

	require.Len(t, rep.Minted.Creds, 2)
	byName := map[string]MintedCredStatus{}
	for _, mc := range rep.Minted.Creds {
		byName[mc.Name] = mc
	}
	require.Equal(t, StatusOK, byName["gh-app"].State, "github-app key present ⇒ authorized")
	require.Equal(t, StatusWarn, byName["drive"].State, "missing refresh token ⇒ warning")
	require.Contains(t, byName["drive"].Detail, "credential authorize drive")

	// An unauthorized minted credential is advisory only — it must NOT drive a
	// non-zero exit (consistent with the broker-down warning).
	require.NotContains(t, rep.Actionable(), "credential unavailable")
	require.Empty(t, rep.Actionable())
}

func TestRun_MintedCredentialsAllAuthorizedNoWarning(t *testing.T) {
	h := newCheckerHarness().withCA()
	h.seedProjectConfig(mintedConfig)
	h.kc.WithItem("agent-creance", "ghapp-key", "PEM")
	h.kc.WithItem("agent-creance", "drive-refresh", "rt")

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	for _, mc := range rep.Minted.Creds {
		require.Equal(t, StatusOK, mc.State, "%s should be authorized", mc.Name)
	}
	// Rendered output shows the section only when minted credentials are present.
	require.Contains(t, Render(rep, nil), "Minted credentials:")
}

func TestRun_NoMintedCredentialsNoSection(t *testing.T) {
	h := newCheckerHarness().withCA()
	h.seedProjectConfig("credentials:\n  gh:\n    source: op://vault/gh\n    template: \"Bearer {token}\"\n")

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	require.Empty(t, rep.Minted.Creds, "a static-only project reports no minted credentials")
	require.NotContains(t, Render(rep, nil), "Minted credentials:")

	out, jerr := RenderJSON(rep)
	require.NoError(t, jerr)
	require.NotContains(t, out, "minted_credentials", "omitted from --json when none")
}

func TestCheckMintedRef_Matrix(t *testing.T) {
	h := newCheckerHarness()
	h.kc.WithItem("svc", "present", "x")
	h.paths.Env = map[string]string{"SET_KEY": "value"}

	// keychain present → authorized.
	require.Equal(t, StatusOK, h.chk.checkMintedRef("c", "oauth2", "keychain://svc/present").State)
	// keychain absent (oauth2) → warn with authorize hint.
	got := h.chk.checkMintedRef("drive", "oauth2", "keychain://svc/absent")
	require.Equal(t, StatusWarn, got.State)
	require.Contains(t, got.Detail, "credential authorize drive")
	// keychain absent (github-app) → warn about the key.
	got = h.chk.checkMintedRef("gh", "github-app", "keychain://svc/absent")
	require.Equal(t, StatusWarn, got.State)
	require.Contains(t, got.Detail, "app private key not found")
	// op:// → configured, no prompt.
	require.Equal(t, StatusOK, h.chk.checkMintedRef("c", "oauth2", "op://vault/x").State)
	// env set → authorized; env unset → warn.
	require.Equal(t, StatusOK, h.chk.checkMintedRef("c", "github-app", "env://SET_KEY").State)
	require.Equal(t, StatusWarn, h.chk.checkMintedRef("c", "oauth2", "env://UNSET_KEY").State)
}
