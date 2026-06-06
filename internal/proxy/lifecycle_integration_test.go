//go:build integration

// Package proxy integration test for the lifecycle manager. Gated behind the
// `integration` build tag (spike S3) so `make test` never runs it. It drives the
// REAL seams (OSFlock, OSProcessManager, OSPortAllocator, OSFileSystem) and a real
// mitmproxy + the extracted enforcer addon across two invocations.
//
// Run with: make test-integration  (= go test -race -tags=integration ./...)
package proxy_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestLifecycleStartAttachTeardownRealProxy(t *testing.T) {
	if _, err := exec.LookPath("mitmdump"); err != nil {
		t.Skip("mitmdump not installed; skipping real-proxy integration test")
	}

	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	projectDir := t.TempDir()
	paths := sysdep.OSPathResolver{}
	resolver := state.New(paths)
	lay, err := resolver.Resolve(projectDir)
	require.NoError(t, err)

	// Extract the enforcer addon the proxy is launched with.
	enforcerPy, err := proxy.NewExtractor(sysdep.OSFileSystem{}, paths).Extract()
	require.NoError(t, err)

	// Seed a minimal policy.json + a session overlay so we can assert the purge.
	require.NoError(t, os.MkdirAll(lay.Root, 0o755))
	require.NoError(t, os.WriteFile(lay.PolicyJSON(), []byte(`{"version":1,"rules":[]}`), 0o644))
	require.NoError(t, os.WriteFile(lay.SessionOverlay(), []byte("once: []\n"), 0o644))

	mgr := proxy.NewManager(sysdep.OSFileSystem{}, sysdep.OSFlock{}, sysdep.OSProcessManager{}, sysdep.OSPortAllocator{}, os.Stderr)

	cfg := func(pid int) proxy.StartConfig {
		return proxy.StartConfig{Layout: lay, EnforcerPy: enforcerPy, PolicyHash: "h1", SelfPID: pid}
	}

	// Invocation 1 starts the proxy.
	att1, err := mgr.Attach(context.Background(), cfg(os.Getpid()))
	require.NoError(t, err)
	require.NotZero(t, att1.ProxyPID)
	require.NotZero(t, att1.Port)

	// Give mitmproxy a moment to bind, then confirm it is listening.
	require.Eventually(t, func() bool {
		return sysdep.OSProcessManager{}.Alive(att1.ProxyPID) && sysdep.OSPortAllocator{}.Probe(att1.Port)
	}, 5*time.Second, 100*time.Millisecond, "proxy did not come up")

	// Invocation 2 attaches — no second proxy.
	att2, err := mgr.Attach(context.Background(), cfg(os.Getpid()+1))
	require.NoError(t, err)
	require.Equal(t, att1.ProxyPID, att2.ProxyPID, "second invocation must share the proxy")
	require.Equal(t, att1.Port, att2.Port)

	// First out: proxy stays up.
	require.NoError(t, mgr.Detach(lay, os.Getpid()))
	require.True(t, sysdep.OSProcessManager{}.Alive(att1.ProxyPID), "proxy must survive a non-final exit")

	// Last out: proxy torn down, overlay purged.
	require.NoError(t, mgr.Detach(lay, os.Getpid()+1))
	require.Eventually(t, func() bool {
		return !sysdep.OSProcessManager{}.Alive(att1.ProxyPID)
	}, 5*time.Second, 100*time.Millisecond, "proxy was not torn down on last-out")
	_, statErr := os.Stat(lay.SessionOverlay())
	require.True(t, os.IsNotExist(statErr), "session overlay should be purged on last-out")

	// C4: the lock lives under the out-of-tree cache, not the project tree.
	require.True(t, strings.HasPrefix(lay.ProxyLock(), cache))
	require.False(t, strings.HasPrefix(lay.ProxyLock(), projectDir))
}
