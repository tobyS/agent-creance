//go:build integration

// End-to-end proof of `agent-creance clean` against a REAL proxy and lock, driving
// the real OS seams (OSFlock, OSProcessManager, OSPortAllocator, OSFileSystem).
// Gated behind the integration tag; run with `make test-integration`. This is
// AC-0032 Verification step 3.
//
// We spawn a real mitmdump (the established precedent in
// doctor_fix_integration_test.go: driving the full `run` → `clean` path would need
// the entire cage stack — agent-safehouse, the CA, credentials — which is not
// hermetic; spawning the proxy directly proves the same teardown contract).
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestCleanStopsRealProxy(t *testing.T) {
	var pm sysdep.OSProcessManager
	var pa sysdep.OSPortAllocator

	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	proj := t.TempDir()
	t.Chdir(proj)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start a real listening proxy on a real port.
	port, err := pa.Allocate()
	require.NoError(t, err)
	proxyPID, err := pm.Spawn(ctx, "mitmdump",
		"--listen-host", "127.0.0.1", "--listen-port", strconv.Itoa(port), "-q")
	require.NoError(t, err)
	defer func() { _ = pm.Signal(proxyPID, os.Interrupt) }() // best-effort cleanup
	if proxyPID == 0 {
		t.Skip("mitmdump not available; skipping real-proxy clean test")
	}
	require.Eventually(t, func() bool { return pa.Probe(port) }, 10*time.Second, 100*time.Millisecond,
		"proxy never started listening")

	// Write the real proxy.lock recording the live proxy and no attached agents, so
	// clean tears it down without --force, plus an overlay that must be purged.
	resolver := state.New(sysdep.OSPathResolver{})
	layout, err := resolver.Resolve(".")
	require.NoError(t, err)
	require.NoError(t, sysdep.OSFileSystem{}.MkdirAll(layout.Root, 0o755))
	lock := map[string]any{
		"proxy_pid": proxyPID, "port": port, "policy_hash": "h", "agents": []int{},
		"canonical_path": layout.Canonical,
	}
	data, err := json.Marshal(lock)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(layout.ProxyLock(), data, 0o600))
	require.NoError(t, os.WriteFile(layout.SessionOverlay(), []byte("once: x"), 0o600))

	// Drive the real `clean`.
	buf := &bytes.Buffer{}
	app := realApp(buf)
	require.NoError(t, runClean(app, false))
	t.Logf("clean stdout:\n%s", buf.String())
	require.Contains(t, buf.String(), "stopped proxy", "clean should report the stopped proxy")

	// The proxy must stop listening (the closed socket is the true liveness signal —
	// see the note in doctor_fix_integration_test.go on zombies vs kill(pid,0)).
	require.Eventually(t, func() bool { return !pa.Probe(port) }, 10*time.Second, 100*time.Millisecond,
		"proxy still listening after clean")

	cleared, err := os.ReadFile(layout.ProxyLock())
	require.NoError(t, err)
	var got struct {
		ProxyPID int   `json:"proxy_pid"`
		Agents   []int `json:"agents"`
	}
	require.NoError(t, json.Unmarshal(cleared, &got))
	require.Zero(t, got.ProxyPID, "lock proxy_pid should be cleared")
	require.Empty(t, got.Agents, "lock agents should be cleared")

	_, err = os.Stat(layout.SessionOverlay())
	require.True(t, os.IsNotExist(err), "session overlay should be purged")
}
