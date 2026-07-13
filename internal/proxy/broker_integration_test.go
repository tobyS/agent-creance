//go:build integration

// End-to-end test of the real credential-broker daemon (AC-0069b): the actual
// agent-creance binary, re-executed as `broker`, receiving its secrets over the
// inherited descriptor and serving them on a real unix socket.
//
// The unit tests cover the store, the protocol, and the lifecycle's spawn/teardown
// against fakes; the Python suite covers the addon's side against a stub broker.
// What is only true if this test passes is that the two halves meet: that the
// binary really does read fd 3, really does bind a 0600 socket, really does answer
// the wire protocol, and really does take its secrets to the grave on SIGTERM.
//
// Run with: make test-integration
package proxy_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/broker"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// brokerDaemon is a running `agent-creance broker` and the socket it serves.
type brokerDaemon struct {
	pid  int
	sock string
}

// startBroker builds the CLI, re-executes it as the broker with secrets on fd 3
// (exactly as proxy.Manager.Attach does), and waits for the socket to answer.
func startBroker(t *testing.T, secrets map[string]string) brokerDaemon {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("the broker is macOS-only")
	}

	bin := buildCLI(t)

	// A short dir: sun_path is 104 bytes, and t.TempDir() embeds the test name on top
	// of an already-long TMPDIR. The production path is guarded the same way.
	dir, err := os.MkdirTemp("", "ac")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "broker.sock")

	payload, err := json.Marshal(secrets)
	require.NoError(t, err)

	pid, err := sysdep.OSProcessManager{}.SpawnWithSecret(
		context.Background(), payload, bin, "broker", "--socket", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	require.Eventually(t, func() bool { return sysdep.OSUnixSocket{}.Probe(sock) },
		10*time.Second, 50*time.Millisecond, "broker never started listening on %s", sock)

	return brokerDaemon{pid: pid, sock: sock}
}

// buildCLI compiles the real binary under test.
func buildCLI(t *testing.T) string {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "agent-creance")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/tobyS/agent-creance/cmd/agent-creance")
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "building the CLI: %s", out)
	return bin
}

// ask speaks the wire protocol once, exactly as the enforcer's broker.py does.
func ask(t *testing.T, sock, credential string) broker.Response {
	t.Helper()

	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, json.NewEncoder(conn).Encode(broker.Request{Credential: credential}))

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	require.NoError(t, err)

	var resp broker.Response
	require.NoError(t, json.Unmarshal(line, &resp))
	return resp
}

func TestBrokerDaemonServesSecretsFromInheritedFD(t *testing.T) {
	d := startBroker(t, map[string]string{"gh": "ghs_real", "deploy": "d3ploy"})

	assert.Equal(t, broker.Response{Token: "ghs_real"}, ask(t, d.sock, "gh"))
	assert.Equal(t, broker.Response{Token: "d3ploy"}, ask(t, d.sock, "deploy"))
	assert.Equal(t, broker.Response{Error: broker.ErrUnknownCredential}, ask(t, d.sock, "nope"))
}

// The socket's mode IS the access control (there is no bearer token and no peer-uid
// check — the caged agent shares mitmproxy's uid, so a uid check would prove
// nothing). If this regresses, the broker is readable by any process on the box.
func TestBrokerDaemonSocketIsNotWorldAccessible(t *testing.T) {
	d := startBroker(t, map[string]string{"gh": "ghs_real"})

	fi, err := os.Stat(d.sock)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

// The secrets must not be visible in the process table: the payload rides fd 3
// precisely because argv is world-readable via ps.
func TestBrokerDaemonDoesNotLeakSecretsInArgv(t *testing.T) {
	d := startBroker(t, map[string]string{"gh": "ghs_TOPSECRET"})

	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(d.pid)).CombinedOutput()
	require.NoError(t, err)

	assert.Contains(t, string(out), "broker", "sanity: we are looking at the broker's argv")
	assert.NotContains(t, string(out), "ghs_TOPSECRET", "the token must never reach argv")
}

// SIGTERM is what makes the broker wipe the tokens it custodied — the reason its
// lifetime is pinned to the proxy's, and the reason last-out Detach signals it.
func TestBrokerDaemonExitsAndRemovesSocketOnSIGTERM(t *testing.T) {
	d := startBroker(t, map[string]string{"gh": "ghs_real"})

	require.NoError(t, syscall.Kill(d.pid, syscall.SIGTERM))

	// Reap it: the broker is a child of this test process (SpawnWithSecret releases
	// the handle but does not detach parentage), so an exited-but-unreaped broker is a
	// zombie — and kill(pid, 0) reports a zombie as alive. Wait4 is what actually
	// establishes that it exited.
	require.Eventually(t, func() bool {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(d.pid, &ws, syscall.WNOHANG, nil)
		return err == nil && wpid == d.pid && ws.Exited()
	}, 5*time.Second, 50*time.Millisecond, "broker did not exit on SIGTERM")
	assert.False(t, sysdep.OSUnixSocket{}.Probe(d.sock), "the socket must stop answering")
	assert.NoFileExists(t, d.sock, "the socket file is removed on the way out")
}

// A broker that cannot read its payload still starts and still answers — every
// lookup simply misses, so the enforcer answers 472 per request. Refusing to start
// would take the whole cage down over one unresolvable credential.
func TestBrokerDaemonWithoutPayloadFailsClosedNotShut(t *testing.T) {
	d := startBroker(t, map[string]string{}) // an empty payload

	assert.Equal(t, broker.Response{Error: broker.ErrUnknownCredential}, ask(t, d.sock, "gh"))
}
