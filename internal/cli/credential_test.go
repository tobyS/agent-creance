package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
)

// TestCredentialAddReachesPolicyJSON: `credential add` writes the entry to the config
// and recompiles, so the reference (never a value) lands in policy.json — exactly what
// the enforcer hot-reloads.
func TestCredentialAddReachesPolicyJSON(t *testing.T) {
	f := newMutateFixture(t)

	err := runCredentialAdd(context.Background(), f.app, mutProjDir, "github", credentialAddOpts{
		source: "op://Private/GitHub/token", bearer: true,
	})
	require.NoError(t, err)

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	got, ok := cfg.Credentials["github"]
	require.True(t, ok)
	require.Equal(t, "op://Private/GitHub/token", got.Source)
	require.Equal(t, "Bearer {token}", got.Template)

	pj := string(f.policyJSON(t))
	require.Contains(t, pj, "op://Private/GitHub/token", "reference carried into policy.json")
	require.NotContains(t, pj, "secret-value", "no resolved value in the artifact")
	require.Contains(t, f.out.String(), "policy recompiled")
}

// TestCredentialAddBasicRequiresUsername: --basic without --username is rejected before
// any write.
func TestCredentialAddBasicRequiresUsername(t *testing.T) {
	f := newMutateFixture(t)
	err := runCredentialAdd(context.Background(), f.app, mutProjDir, "pypi", credentialAddOpts{
		source: "op://Private/PyPI/token", basic: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--basic needs --username")
	// The config is untouched (no credentials block written).
	require.NotContains(t, string(f.projectConfig(t)), "credentials:")
}

// TestCredentialRemoveBlockedWhileBound: removing a credential a rule still injects is
// refused with an actionable message, and the config is left unchanged.
func TestCredentialRemoveBlockedWhileBound(t *testing.T) {
	f := newMutateFixture(t)
	seeded := "network:\n  egress:\n    allow:\n" +
		"      - host: api.github.com\n" +
		"        paths: [\"/graphql\"]\n" +
		"        inject: github\n" +
		"credentials:\n" +
		"  github:\n" +
		"    source: op://Private/GitHub/token\n" +
		"    template: \"Bearer {token}\"\n"
	f.fs.Files[mutProjDir+"/.agent-creance.yaml"] = []byte(seeded)

	err := runCredentialRemove(context.Background(), f.app, mutProjDir, "github", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "still injected by api.github.com")
	require.Contains(t, err.Error(), "unbind it first")
	require.Equal(t, seeded, string(f.projectConfig(t)), "config unchanged when removal is refused")
}

// TestCredentialList: list shows the merged entries by name/source/shape and never a
// resolved value.
func TestCredentialList(t *testing.T) {
	f := newMutateFixture(t)
	f.fs.Files[mutProjDir+"/.agent-creance.yaml"] = []byte(
		"credentials:\n" +
			"  github:\n" +
			"    source: op://Private/GitHub/token\n" +
			"    template: \"Bearer {token}\"\n")

	require.NoError(t, runCredentialList(f.app, mutProjDir))
	out := f.out.String()
	require.True(t, strings.Contains(out, "github"))
	require.True(t, strings.Contains(out, "op://Private/GitHub/token"))
	require.True(t, strings.Contains(out, "Bearer {token}"))
}
