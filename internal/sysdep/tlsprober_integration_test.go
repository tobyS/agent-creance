//go:build integration

// Real-curl proof of OSTLSProber's load-bearing property (AC-0059 F10): the probe
// validates the proxy's re-signed leaf against the system trust store ONLY. The unit
// test asserts the argv carries no -k/--cacert/--proxy-insecure; this test proves the
// behaviour end-to-end against a real mitmproxy + curl, which is why it lives behind
// the integration tag.
//
// Requires `mitmdump` and `curl` on PATH and outbound HTTPS to example.com; it skips
// (not fails) when any is unavailable. Run via `make test-integration`.
package sysdep_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

const probeTarget = "https://example.com"

func TestOSTLSProberReportsUntrustedForFreshCA(t *testing.T) {
	requireTools(t)

	// A throwaway confdir makes mitmproxy mint a brand-new CA that the system trust
	// store has never seen — so the re-signed leaf must fail validation. This is the
	// deterministic, host-independent half of the proof: it does not depend on what
	// the host happens to trust.
	confdir := t.TempDir()
	port := freePort(t)
	stopProxy := startMitmdump(t, confdir, port)
	defer stopProxy()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outcome, err := sysdep.OSTLSProber{}.ProbeViaProxy(ctx, proxyURL(port), probeTarget)
	require.NoError(t, err)
	if outcome == sysdep.ProbeError {
		t.Skip("probe returned an environment error (likely no outbound HTTPS); skipping")
	}
	require.Equal(t, sysdep.ProbeUntrusted, outcome,
		"a fresh, system-untrusted mitmproxy CA must yield ProbeUntrusted — if it does not, the prober is validating too leniently")
}

func TestOSTLSProberReportsTrustedWhenHostTrustsCA(t *testing.T) {
	requireTools(t)

	// Use the host's default mitmproxy confdir (~/.mitmproxy). If the developer has
	// already installed and trusted that CA, the probe must report ProbeTrusted —
	// proving the trusted branch works against a real curl. Otherwise we cannot set up
	// a system-trusted CA without touching the keychain, so we skip.
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	confdir := filepath.Join(home, ".mitmproxy")
	if _, err := os.Stat(filepath.Join(confdir, "mitmproxy-ca-cert.pem")); err != nil {
		t.Skip("no ~/.mitmproxy CA present; skipping trusted-branch assertion")
	}

	port := freePort(t)
	stopProxy := startMitmdump(t, confdir, port)
	defer stopProxy()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outcome, err := sysdep.OSTLSProber{}.ProbeViaProxy(ctx, proxyURL(port), probeTarget)
	require.NoError(t, err)
	switch outcome {
	case sysdep.ProbeTrusted:
		// Trusted branch proven against a real curl.
	case sysdep.ProbeUntrusted:
		t.Skip("host does not trust its ~/.mitmproxy CA; skipping trusted-branch assertion")
	default:
		t.Skip("probe returned an environment error (likely no outbound HTTPS); skipping")
	}
}

func requireTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"mitmdump", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed; skipping real-proxy probe test", tool)
		}
	}
}

func proxyURL(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

// freePort grabs an ephemeral loopback port and releases it. There is a small race
// before mitmdump rebinds it, but the port is ours for the moment and good enough for
// a single-process test.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// startMitmdump launches a bare mitmdump on the given confdir and port, waits until
// it is listening and has generated its CA, and returns a teardown func.
func startMitmdump(t *testing.T, confdir string, port int) func() {
	t.Helper()
	cmd := exec.Command("mitmdump",
		"--set", "confdir="+confdir,
		"--listen-host", "127.0.0.1",
		"--listen-port", strconv.Itoa(port),
		"-q")
	require.NoError(t, cmd.Start())

	stop := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}

	caCert := filepath.Join(confdir, "mitmproxy-ca-cert.pem")
	ok := false
	require.Eventually(t, func() bool {
		if _, err := os.Stat(caCert); err != nil {
			return false
		}
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		ok = true
		return true
	}, 15*time.Second, 100*time.Millisecond, "mitmdump did not become ready")
	if !ok {
		stop()
		t.Fatal("mitmdump not ready")
	}
	return stop
}
