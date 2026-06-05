//go:build integration

// These tests perform real registry lookups (via the registry client) against
// registry.npmjs.org and repo.packagist.org, so they run only under
// `make test-integration` (the integration build tag), never in the hermetic unit
// suite. They assert on the emitted hosts rather than exact org/repo, which can drift
// upstream, to stay robust.
package generator_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/generator"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func hasHost(rules []generator.Rule, host string) bool {
	for _, r := range rules {
		if r.Rule.Host == host {
			return true
		}
	}
	return false
}

func TestLivePackageJSONGenerate(t *testing.T) {
	g, err := generator.New("package_json",
		sysdep.OSFileSystem{}, sysdep.OSClock{}, sysdep.OSHTTPGetter{},
		t.TempDir(), t.TempDir())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	manifest := []byte(`{"dependencies":{"react":"*"}}`)
	rules, err := g.Generate(ctx, manifest)
	require.NoError(t, err)
	require.NotEmpty(t, rules)

	// react's repository is on GitHub, so the companion content hosts are emitted.
	require.True(t, hasHost(rules, "github.com"), "github.com repository rule")
	require.True(t, hasHost(rules, "raw.githubusercontent.com"), "raw companion host")
	require.True(t, hasHost(rules, "objects.githubusercontent.com"), "release-CDN companion host")

	// Second run is served from the output cache (fast, no error, identical result).
	again, err := g.Generate(ctx, manifest)
	require.NoError(t, err)
	require.Equal(t, rules, again)
}

func TestLiveComposerJSONGenerate(t *testing.T) {
	g, err := generator.New("composer_json",
		sysdep.OSFileSystem{}, sysdep.OSClock{}, sysdep.OSHTTPGetter{},
		t.TempDir(), t.TempDir())
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// php is a platform requirement and must be filtered out (no Packagist lookup).
	manifest := []byte(`{"require":{"php":">=8.1","monolog/monolog":"*"}}`)
	rules, err := g.Generate(ctx, manifest)
	require.NoError(t, err)
	require.NotEmpty(t, rules)

	// monolog's repository is on GitHub.
	require.True(t, hasHost(rules, "github.com"), "github.com repository rule")
	require.True(t, hasHost(rules, "raw.githubusercontent.com"), "raw companion host")
}
