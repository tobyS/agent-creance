//go:build integration

// End-to-end proof of `agent-creance doctor --fix` orphan cleanup against a REAL
// proxy and lock, driving the real OS seams (OSFlock, OSProcessManager,
// OSPortAllocator, OSFileSystem). Gated behind the integration tag; run with
// `make test-integration`. This is AC-0031 ticket Verification step 4.
//
// The CA check short-circuits with no network: HOME points at a temp dir with no
// mitmproxy CA, so doctor reports "not generated" and never spawns the verification
// proxy. We assert on the cleanup side effects (orphan gone, lock cleared), not the
// exit code, since agent-safehouse may be absent on the host.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/buildinfo"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestDoctorFixCleansRealOrphan(t *testing.T) {
	var pm sysdep.OSProcessManager
	var pa sysdep.OSPortAllocator

	// Hermetic HOME (no CA → CA check makes no network call) and cache dir.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// cwd is the "project"; the lock lands under XDG_CACHE_HOME/agent-creance.
	proj := t.TempDir()
	t.Chdir(proj)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start a real listening proxy on a real port — this is the orphan.
	port, err := pa.Allocate()
	require.NoError(t, err)
	proxyPID, err := pm.Spawn(ctx, "mitmdump",
		"--listen-host", "127.0.0.1", "--listen-port", strconv.Itoa(port), "-q")
	require.NoError(t, err)
	defer func() { _ = pm.Signal(proxyPID, os.Interrupt) }() // best-effort cleanup
	if proxyPID == 0 {
		t.Skip("mitmdump not available; skipping real-orphan cleanup test")
	}
	require.Eventually(t, func() bool { return pa.Probe(port) }, 10*time.Second, 100*time.Millisecond,
		"proxy never started listening")

	// A definitely-dead attached agent PID: run `true` to completion and reuse its PID.
	dead := exec.Command("true")
	require.NoError(t, dead.Run())
	deadPID := dead.Process.Pid

	// Write the real proxy.lock recording the live proxy + the dead agent.
	resolver := state.New(sysdep.OSPathResolver{})
	layout, err := resolver.Resolve(".")
	require.NoError(t, err)
	require.NoError(t, sysdep.OSFileSystem{}.MkdirAll(layout.Root, 0o755))
	lock := map[string]any{
		"proxy_pid": proxyPID, "port": port, "policy_hash": "h",
		"agents": []map[string]any{{"pid": deadPID, "start": 1}},
	}
	data, err := json.Marshal(lock)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(layout.ProxyLock(), data, 0o600))
	// A session overlay to be purged by the cleanup.
	require.NoError(t, os.WriteFile(layout.SessionOverlay(), []byte("once: x"), 0o600))

	// Drive the real `doctor --fix`. We ignore the exit error (a missing
	// agent-safehouse prereq would make it non-nil independently of the cleanup).
	buf := &bytes.Buffer{}
	app := realApp(buf)
	doctorErr := runDoctor(ctx, app, true, false)
	t.Logf("doctor --fix err=%v\n--- stdout ---\n%s", doctorErr, buf.String())

	// The orphan proxy must stop listening. (We probe the socket rather than
	// kill(pid,0): this test process is the proxy's parent and never reaps it, so a
	// SIGTERM'd proxy lingers as a zombie that kill(pid,0) still reports alive — an
	// artifact of the harness. In production the short-lived spawning process exits
	// and launchd reaps the proxy. The closed listen socket is the true liveness signal.)
	require.Eventually(t, func() bool { return !pa.Probe(port) }, 10*time.Second, 100*time.Millisecond,
		"orphan proxy still listening after doctor --fix")
	require.Contains(t, buf.String(), "cleaned orphan proxy", "doctor should report the cleanup")

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

// realApp builds an App wired to the real OS seams (the same wiring as Main), with
// stdout redirected to buf.
func realApp(buf *bytes.Buffer) *App {
	return &App{
		Commander:      sysdep.ExecCommander{},
		Stdout:         buf,
		Stderr:         &bytes.Buffer{},
		Tested:         buildinfo.TestedVersions,
		FS:             sysdep.OSFileSystem{},
		Paths:          sysdep.OSPathResolver{},
		Keychain:       sysdep.OSKeychain{},
		Flock:          sysdep.OSFlock{},
		ProcessManager: sysdep.OSProcessManager{},
		PortAllocator:  sysdep.OSPortAllocator{},
		TLSProber:      sysdep.OSTLSProber{},
		Sleeper:        sysdep.OSSleeper{},
		FSType:         sysdep.OSFilesystemTyper{},
		Listeners:      sysdep.OSListenerScanner{},
	}
}
