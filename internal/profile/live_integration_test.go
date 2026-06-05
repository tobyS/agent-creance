//go:build integration

// This test ships the S3 (AC-0003) localhost-refusal self-test against the *generated*
// rules: it wraps profile.RenderNetworkSB's output in a minimal standalone Seatbelt
// profile and drives it through the real /usr/bin/sandbox-exec, confirming a
// non-allowlisted loopback port is EPERM-refused over both IPv4 and IPv6 while the
// allowlisted port stays reachable. It runs only under `make test-integration` (the
// integration build tag), never in the hermetic unit suite, and needs macOS with
// sandbox-exec and nc on PATH (it skips otherwise). Safehouse --append-profile
// composition is S5's/AC-0023's domain and is not re-tested here.
package profile_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/profile"
)

func TestLiveLocalhostRefusal(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not on PATH")
	}
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not on PATH")
	}
	requireSandboxApplies(t)

	// A = allowlisted port, O = a non-allowlisted port. Both get real listeners so a
	// refusal can only be the sandbox (EPERM), never a closed port (ECONNREFUSED).
	allowed := freePort(t)
	other := freePort(t)

	startListener(t, "-4", "-k", "-l", "127.0.0.1", strconv.Itoa(allowed))
	startListener(t, "-4", "-k", "-l", "127.0.0.1", strconv.Itoa(other))
	startListener(t, "-6", "-k", "-l", "::1", strconv.Itoa(other))

	// Wait until each listener is genuinely up (this doubles as the unsandboxed control:
	// if these connects fail, a later sandbox EPERM would be a false pass).
	waitListening(t, "-4", "127.0.0.1", allowed)
	waitListening(t, "-4", "127.0.0.1", other)
	waitListening(t, "-6", "::1", other)

	// A minimal standalone profile: broad non-network grants (so nc can run) followed by
	// the generated network fragment (its (deny network*) baseline + the localhost:allowed
	// allow). network.sb itself is a headerless append fragment, so the test supplies the
	// (version 1)/(deny default) header that safehouse's base would otherwise provide.
	const header = "(version 1)\n(deny default)\n" +
		"(allow process-fork) (allow process-exec*) (allow sysctl-read)\n" +
		"(allow file-read* file-read-metadata file-read-data)\n" +
		"(allow mach-lookup) (allow signal) (allow system-socket)\n"
	body := header + profile.RenderNetworkSB([]config.HostService{{Label: "probe", Port: allowed}})

	profilePath := filepath.Join(t.TempDir(), "probe.sb")
	require.NoError(t, os.WriteFile(profilePath, []byte(body), 0o644))

	// Allowlisted port: reachable inside the cage.
	outA, errA := probe(t, profilePath, "-4", "127.0.0.1", allowed)
	require.NoError(t, errA, "allowlisted port must be reachable from the cage:\n%s", outA)

	// Non-allowlisted port over IPv4: refused with EPERM (not ECONNREFUSED).
	outO4, errO4 := probe(t, profilePath, "-4", "127.0.0.1", other)
	require.Error(t, errO4, "non-allowlisted v4 port must be refused:\n%s", outO4)
	require.Contains(t, outO4, "Operation not permitted",
		"refusal must be EPERM (sandbox enforcement), not ECONNREFUSED:\n%s", outO4)

	// Same port over IPv6: the core "does ::1 slip past?" check — it must not.
	outO6, errO6 := probe(t, profilePath, "-6", "::1", other)
	require.Error(t, errO6, "non-allowlisted v6 port must be refused:\n%s", outO6)
	require.Contains(t, outO6, "Operation not permitted",
		"refusal must be EPERM (sandbox enforcement), not ECONNREFUSED:\n%s", outO6)
}

// requireSandboxApplies skips the test when the environment cannot apply *any* nested
// Seatbelt profile (e.g. when the test process itself is already sandboxed, which fails
// even a trivial (allow default) with "sandbox_apply: Operation not permitted"). A
// malformed profile would instead produce a *compile* error, so this gate skips only on
// the environmental limitation, never masking a real rule regression.
func requireSandboxApplies(t *testing.T) {
	t.Helper()
	sb := filepath.Join(t.TempDir(), "preflight.sb")
	require.NoError(t, os.WriteFile(sb, []byte("(version 1)\n(allow default)\n"), 0o644))
	out, err := exec.Command("sandbox-exec", "-f", sb, "/usr/bin/true").CombinedOutput()
	if err != nil && strings.Contains(string(out), "sandbox_apply") {
		t.Skipf("environment cannot apply nested sandbox profiles: %s", out)
	}
	require.NoError(t, err, "sandbox-exec preflight failed: %s", out)
}

// freePort returns an unused loopback TCP port (best-effort; closed immediately so a
// listener can claim it).
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// startListener launches a backgrounded `nc` listener and arranges its teardown.
func startListener(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("nc", args...)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
}

// waitListening blocks until an unsandboxed connect to addr:port succeeds, proving the
// listener is up before the sandboxed probes run.
func waitListening(t *testing.T, family, addr string, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if exec.Command("nc", "-z", "-G1", "-w1", family, addr, strconv.Itoa(port)).Run() == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("listener %s %s:%d did not come up", family, addr, port)
}

// probe runs `sandbox-exec -f <profile> nc -vz ... <addr> <port>` and returns the
// combined output plus the exit error (nil on a successful connection).
func probe(t *testing.T, profilePath, family, addr string, port int) (string, error) {
	t.Helper()
	cmd := exec.Command("sandbox-exec", "-f", profilePath,
		"nc", "-vz", "-G2", "-w2", family, addr, strconv.Itoa(port))
	out, err := cmd.CombinedOutput()
	return string(out), err
}
