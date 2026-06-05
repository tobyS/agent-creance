//go:build integration

// These tests perform real network lookups against registry.npmjs.org and
// repo.packagist.org, so they run only under `make test-integration` (the
// integration build tag), never in the hermetic unit suite.
package registry_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/generator/registry"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func liveDeps(t *testing.T) (sysdep.FileSystem, sysdep.Clock, sysdep.HTTPGetter, string) {
	t.Helper()
	return sysdep.OSFileSystem{}, sysdep.OSClock{}, sysdep.OSHTTPGetter{}, t.TempDir()
}

func TestLiveNPMLookup(t *testing.T) {
	fsys, clk, http, root := liveDeps(t)
	c := registry.NewNPM(fsys, clk, http, root)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	md, err := c.Lookup(ctx, "left-pad")
	require.NoError(t, err)
	require.NotEmpty(t, md.Homepage, "npm homepage")
	require.NotEmpty(t, md.Repository, "npm repository")

	// Second lookup is served from the cache written by the first.
	again, err := c.Lookup(ctx, "left-pad")
	require.NoError(t, err)
	require.Equal(t, md, again)
}

func TestLivePackagistLookup(t *testing.T) {
	fsys, clk, http, root := liveDeps(t)
	c := registry.NewPackagist(fsys, clk, http, root)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	md, err := c.Lookup(ctx, "monolog/monolog")
	require.NoError(t, err)
	require.NotEmpty(t, md.Homepage, "packagist homepage")
	require.NotEmpty(t, md.Repository, "packagist repository")

	again, err := c.Lookup(ctx, "monolog/monolog")
	require.NoError(t, err)
	require.Equal(t, md, again)
}
