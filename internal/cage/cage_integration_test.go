//go:build integration

// These tests drive the cage-built invocation through the real agent-safehouse
// (0.10.1) binary: they confirm safehouse accepts the constructed argv (both
// --append-profile fragments parse and compose), runs a trivial caged command to
// exit 0, that the injected env reaches inside the cage via --env-pass, and that
// the deny-all network baseline blocks direct egress. They run only under
// `make test-integration` and need macOS with `safehouse` on PATH (skip
// otherwise). The full isolation matrix is AC-0033's domain (M3 gate); this is the
// minimal "egress blocked except via proxy" smoke called for by AC-0023 step 4.
//
// safehouse applies a sandbox-exec policy, which a host that is itself already
// sandboxed cannot nest ("sandbox_apply: Operation not permitted"). When that is
// detected the tests skip rather than report a false pass/fail — run them on a
// host where safehouse can apply its policy.
package cage_test

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/cage"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/profile"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestLiveSafehouseInvocation(t *testing.T) {
	requireSafehouse(t)
	proj, layout := setupLayout(t)
	b := cage.New(sysdep.OSFileSystem{}, sysdep.OSPathResolver{})

	cfg := &config.Config{
		Agent: config.Agent{
			Command: []string{"/bin/sh", "-c", `echo ok; echo "PROXY=$HTTPS_PROXY"; echo "CA=$SSL_CERT_FILE"`},
			Workdir: proj,
		},
		Safehouse: config.Safehouse{AddDirsRW: []string{proj}},
	}
	out, err := runCaged(t, b, cfg, layout, 18081) // no real proxy runs; testing argv + env wiring
	require.NoError(t, err, "safehouse run failed:\n%s", out)
	require.Contains(t, out, "ok", "caged command should run to completion")
	require.Contains(t, out, "PROXY=http://127.0.0.1:18081", "--env-pass must forward HTTPS_PROXY into the cage")
	require.Contains(t, out, ".mitmproxy/mitmproxy-ca-cert.pem", "SSL_CERT_FILE must point at the mitmproxy CA")
}

// TestLiveSafehouseEgressDenied confirms the deny-all baseline blocks a direct
// outbound connection from inside the cage. Skips if `nc` is absent.
func TestLiveSafehouseEgressDenied(t *testing.T) {
	requireSafehouse(t)
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not on PATH")
	}
	proj, layout := setupLayout(t)
	b := cage.New(sysdep.OSFileSystem{}, sysdep.OSPathResolver{})

	// Raw connect to an external host:port (proxy env is irrelevant to raw nc); the
	// deny-all network baseline must refuse it. -G/-w cap the attempt at a few sec.
	cfg := &config.Config{
		Agent: config.Agent{
			Command: []string{"nc", "-G", "3", "-w", "3", "1.1.1.1", "443"},
			Workdir: proj,
		},
		Safehouse: config.Safehouse{AddDirsRW: []string{proj}},
	}
	out, err := runCaged(t, b, cfg, layout, 18081)
	require.Error(t, err, "direct egress should be denied by the deny-all baseline; got:\n%s", out)
	require.NotContains(t, out, "succeeded", "connection unexpectedly succeeded:\n%s", out)
}

func requireSafehouse(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("safehouse is macOS-only")
	}
	if _, err := exec.LookPath(cage.Binary); err != nil {
		t.Skipf("%s not on PATH", cage.Binary)
	}
}

// setupLayout resolves a per-project layout under a temp XDG_CACHE_HOME (so the
// test never touches the real ~/.cache), creates the state root, and stands in the
// deny-all network.sb baseline (normally the compile step's output).
func setupLayout(t *testing.T) (proj string, layout state.Layout) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	proj = t.TempDir()
	layout, err := state.New(sysdep.OSPathResolver{}).Resolve(proj)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(layout.Root, 0o700))
	require.NoError(t, os.WriteFile(layout.NetworkSB(), []byte(profile.RenderNetworkSB(nil)), 0o600))
	return proj, layout
}

// TestLiveSafehouseGroupTeardown drives the AC-0024 process-group teardown through
// the WHOLE real chain — OSProcessGroup.Start runs the safehouse bash wrapper in a
// new group, which runs sandbox-exec → env → sh, which backgrounds a long-lived
// `sleep`. A single kill(-pgid, SIGTERM) must reap every node, validating the
// research finding that safehouse does not detach its child into its own group.
// Liveness is probed with kill(pid, 0) (sandbox shares the host PID space), so no
// ps/pgrep is needed.
func TestLiveSafehouseGroupTeardown(t *testing.T) {
	requireSafehouse(t)
	proj, layout := setupLayout(t)
	b := cage.New(sysdep.OSFileSystem{}, sysdep.OSPathResolver{})

	// Gate: if this host cannot nest sandbox-exec, runCaged skips the test for us.
	out, err := runCaged(t, b, &config.Config{
		Agent:     config.Agent{Command: []string{"/bin/sh", "-c", "echo ok"}, Workdir: proj},
		Safehouse: config.Safehouse{AddDirsRW: []string{proj}},
	}, layout, 18081)
	require.NoError(t, err, "trivial caged probe failed:\n%s", out)

	// The caged sh records the backgrounded sleep's PID into the project's RW mount.
	pidFile := proj + "/sleep.pid"
	cfg := &config.Config{
		Agent: config.Agent{
			Command: []string{"/bin/sh", "-c", "sleep 300 & echo $! > " + pidFile + "; wait"},
			Workdir: proj,
		},
		Safehouse: config.Safehouse{AddDirsRW: []string{proj}},
	}
	in, err := b.Resolve(cfg, layout, 18081)
	require.NoError(t, err)
	require.NoError(t, b.Prepare(in))
	inv, err := cage.Build(in)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proc, err := sysdep.OSProcessGroup{}.Start(ctx, inv.Env, inv.Path, inv.Args...)
	require.NoError(t, err)
	pgid := proc.Pgid()
	require.Positive(t, pgid)

	var alive sysdep.OSProcessManager
	var sleepPID int
	require.Eventually(t, func() bool {
		sleepPID = readCagedPID(pidFile)
		return sleepPID > 0 && alive.Alive(sleepPID)
	}, 10*time.Second, 50*time.Millisecond, "caged child tree never came up")

	require.NoError(t, proc.Signal(syscall.SIGTERM))
	require.Error(t, proc.Wait(), "a SIGTERM'd caged group should not exit 0")

	require.Eventually(t, func() bool { return !alive.Alive(sleepPID) && !alive.Alive(pgid) },
		10*time.Second, 50*time.Millisecond, "a caged descendant survived the group SIGTERM")
}

// readCagedPID reads a PID an in-cage process wrote to path, or 0 if absent/unparseable.
func readCagedPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}

// runCaged builds + prepares the cage invocation for cfg and runs it through real
// safehouse, returning combined output and the run error. It skips the test if the
// host cannot apply a nested sandbox-exec policy (so neither a false pass nor a
// false fail results from an unsandboxable environment).
func runCaged(t *testing.T, b *cage.Builder, cfg *config.Config, layout state.Layout, port int) (string, error) {
	t.Helper()
	in, err := b.Resolve(cfg, layout, port)
	require.NoError(t, err)
	require.NoError(t, b.Prepare(in))
	inv, err := cage.Build(in)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, inv.Path, inv.Args...)
	cmd.Env = append(os.Environ(), inv.Env...)
	out, runErr := cmd.CombinedOutput()
	if strings.Contains(string(out), "sandbox_apply: Operation not permitted") {
		t.Skip("host cannot apply a nested sandbox-exec policy; run on an unsandboxed macOS host")
	}
	return string(out), runErr
}
