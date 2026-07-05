package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
)

// seedWithCredential is a project config with one credential defined, used by the
// binding tests.
const seedWithCredential = "network:\n  egress:\n    allow:\n" +
	"      - host: seed.example\n" +
	"credentials:\n" +
	"  github:\n" +
	"    source: op://Private/GitHub/token\n" +
	"    template: \"Bearer {token}\"\n"

// TestAllowInjectFreshPath: --inject on a not-yet-allowed path appends a rule carrying
// the binding, and it reaches policy.json.
func TestAllowInjectFreshPath(t *testing.T) {
	f := newMutateFixture(t)
	f.fs.Files[mutProjDir+"/.agent-creance.yaml"] = []byte(seedWithCredential)

	err := runAllowWith(context.Background(), f.app, mutProjDir, "api.github.com/graphql",
		domainAddOpts{inject: "github"})
	require.NoError(t, err)

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	r := findHost(t, cfg.Network.Egress.Allow, "api.github.com")
	require.Equal(t, "github", r.Inject)
	require.Contains(t, string(f.policyJSON(t)), "\"inject\": \"github\"")
}

// TestAllowInjectUpdatesInPlace: binding an already-allowed host/path updates that one
// rule rather than appending a duplicate.
func TestAllowInjectUpdatesInPlace(t *testing.T) {
	f := newMutateFixture(t)
	f.fs.Files[mutProjDir+"/.agent-creance.yaml"] = []byte(seedWithCredential)

	require.NoError(t, runAllowWith(context.Background(), f.app, mutProjDir, "foo.example/api", domainAddOpts{}))
	require.NoError(t, runAllowWith(context.Background(), f.app, mutProjDir, "foo.example/api",
		domainAddOpts{inject: "github"}))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	count := 0
	for _, r := range cfg.Network.Egress.Allow {
		if r.Host == "foo.example" {
			count++
			require.Equal(t, "github", r.Inject)
		}
	}
	require.Equal(t, 1, count, "the existing rule was updated, not duplicated")
}

// TestAllowInjectUndefinedRejected: binding to an undefined credential errors before any
// write.
func TestAllowInjectUndefinedRejected(t *testing.T) {
	f := newMutateFixture(t)
	f.fs.Files[mutProjDir+"/.agent-creance.yaml"] = []byte(seedWithCredential)

	err := runAllowWith(context.Background(), f.app, mutProjDir, "api.github.com/x",
		domainAddOpts{inject: "nope"})
	require.Error(t, err)
	require.Contains(t, err.Error(), `no credential named "nope" is defined`)
	require.Equal(t, seedWithCredential, string(f.projectConfig(t)), "config unchanged")
}

// TestAllowInCage: --in-cage writes in_cage: true and does not require a credential.
func TestAllowInCage(t *testing.T) {
	f := newMutateFixture(t)

	require.NoError(t, runAllowWith(context.Background(), f.app, mutProjDir, "bedrock.example",
		domainAddOpts{inCage: true}))

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	require.True(t, findHost(t, cfg.Network.Egress.Allow, "bedrock.example").InCage)
}

// TestAllowInjectAndInCageConflict: --inject with --in-cage is rejected.
func TestAllowInjectAndInCageConflict(t *testing.T) {
	f := newMutateFixture(t)
	f.fs.Files[mutProjDir+"/.agent-creance.yaml"] = []byte(seedWithCredential)

	err := runAllowWith(context.Background(), f.app, mutProjDir, "x.example",
		domainAddOpts{inject: "github", inCage: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot combine --inject and --in-cage")
}
