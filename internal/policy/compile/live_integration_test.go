//go:build integration

// This test compiles a real project with the *real* generator runner, so it performs a
// live npm registry lookup (via the registry client) against registry.npmjs.org. It runs
// only under `make test-integration` (the integration build tag), never in the hermetic
// unit suite. HOME and XDG_CACHE_HOME are redirected to temp dirs so it neither reads the
// developer's real global config nor pollutes the real cache; it asserts on emitted hosts
// and sources rather than exact org/repo, which can drift upstream.
package compile_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/policy"
	"github.com/tobyS/agent-creance/internal/policy/compile"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestLiveCompileWithRealGenerators(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)                  // no real global config bleeds in
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // artifact + caches land in isolation

	projDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(projDir, ".agent-creance.yaml"),
		[]byte("network:\n  egress:\n    generators:\n      - package_json\n"+
			"    allow:\n      - host: api.github.com\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(projDir, "package.json"),
		[]byte(`{"dependencies":{"react":"*"}}`), 0o644))

	c, err := compile.New(sysdep.OSFileSystem{}, sysdep.OSPathResolver{}, sysdep.OSClock{}, sysdep.OSHTTPGetter{}, nil /*silent*/)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := c.Compile(ctx, projDir)
	require.NoError(t, err)
	require.False(t, res.Skipped, "first compile should regenerate")

	data, err := os.ReadFile(res.PolicyPath)
	require.NoError(t, err)
	var compiled policy.Compiled
	require.NoError(t, json.Unmarshal(data, &compiled))
	require.Equal(t, policy.CompiledVersion, compiled.Version)

	// The explicit project rule and react's generated GitHub companion hosts are present.
	require.True(t, hasSource(compiled.Allow, "api.github.com", "explicit"))
	require.True(t, hasSource(compiled.Allow, "github.com", "generated:package_json:react"))
	require.True(t, hasSource(compiled.Allow, "objects.githubusercontent.com", "generated:package_json:react"))

	// Re-compiling with identical inputs is a cache hit (no regeneration).
	res2, err := c.Compile(ctx, projDir)
	require.NoError(t, err)
	require.True(t, res2.Skipped, "second compile should be served from the input-hash cache")
	require.Equal(t, res.InputHash, res2.InputHash)
}

func hasSource(rules []policy.Rule, host, source string) bool {
	for _, r := range rules {
		if r.Host == host && r.Source == source {
			return true
		}
	}
	return false
}
