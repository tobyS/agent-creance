//go:build integration

// The AC-0033 adversarial cage-verification battery (WP-4.5): the executable form
// of docs/design.md's "What the cage prevents — and what it doesn't". It launches
// the REAL cage — a real mitmdump running the extracted enforcer addon, composed
// with a real agent-safehouse sandbox — and runs the hostile fake-agent script
// (testdata/fake-agent.sh) as the agent command from inside that cage. The script
// probes every threat-model vector and emits structured results; this harness
// parses them (internal/verify.Evaluate) and asserts the whole matrix, plus a
// negative control that proves the harness reports an escape against a
// deliberately-weakened cage.
//
// Gated behind `integration` so `make test` never runs it. It needs an UNSANDBOXED
// macOS host with safehouse + mitmdump + curl + nc (sandbox-exec does not nest;
// the harness skips when it detects it cannot apply a nested policy). The
// proxy-egress ALLOWED vectors additionally need network egress and skip offline,
// mirroring internal/proxy/enforcer/test_integration.py.
//
// Run with: make test-integration  (= go test -race -tags=integration ./...)
package verify_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/cage"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/profile"
	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
	"github.com/tobyS/agent-creance/internal/verify"
)

// Hosts used by the policy. The .test hosts never leave the proxy (mitmproxy
// returns the 403 locally); example.com/.org are the real-egress upstreams.
const (
	allowHost   = "example.com" // intercept → 200
	passHost    = "example.org" // passthrough → real upstream cert
	softHost    = "not-allowlisted.test"
	hardHost    = "blocked.test"
	offpathHost = "offpath.test"
)

func policyJSON() string {
	return `{
  "version": 1,
  "allow": [
    {"host": "` + allowHost + `", "mode": "intercept"},
    {"host": "` + passHost + `", "mode": "passthrough"},
    {"host": "` + offpathHost + `", "paths": ["/ok/"], "mode": "intercept"}
  ],
  "deny_always": [
    {"host": "` + hardHost + `", "mode": "intercept", "reason": "Blocked for cage verification."}
  ]
}`
}

// batteryRun is the result of one caged fake-agent run.
type batteryRun struct {
	verdict verify.Verdict
	out     string
	layout  state.Layout
	egress  bool
}

func TestCageVerificationBattery(t *testing.T) {
	requireUncagedHost(t)
	r := runBattery(t, false)
	t.Log("\n" + r.verdict.Summary())

	require.Falsef(t, r.verdict.Escaped,
		"the cage let a BLOCKED vector through:\n%s\n--- fake-agent output ---\n%s",
		r.verdict.Summary(), r.out)
	require.Falsef(t, r.verdict.Failed,
		"a vector did not behave as the threat model states:\n%s\n--- fake-agent output ---\n%s",
		r.verdict.Summary(), r.out)

	// doc-claude-rw (DOCUMENTED): the planted marker lives in the REAL ~/.claude —
	// the honest v0.1 config-persistence deferral (AC-0045/AC-0046). The marker is
	// cleaned up by the runBattery t.Cleanup.
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(home, ".claude", "creance-escape-marker.json"),
		"the real ~/.claude must be writable in-cage (v0.1 posture)")

	if r.egress {
		assertAuditedPOST(t, r.layout)
		assertPassthroughHostOnly(t, r.layout)
	}
}

func TestCageVerificationNegativeControl(t *testing.T) {
	requireUncagedHost(t)
	// Same battery, but the cage's deny-all network baseline is stripped. The
	// harness MUST detect the resulting escape; otherwise it would rubber-stamp a
	// broken cage instead of verifying a working one.
	r := runBattery(t, true)
	t.Log("\n" + r.verdict.Summary())

	require.Truef(t, r.verdict.Escaped,
		"a cage with no network deny-baseline must be detected as an escape, but the "+
			"battery reported none:\n%s\n--- fake-agent output ---\n%s",
		r.verdict.Summary(), r.out)

	// Name the leaked vector(s) for a clear failure message; at least one must be a
	// raw-egress kernel vector (the deny-baseline is what those rely on).
	var leaked []string
	for _, res := range r.verdict.Results {
		if res.Escape {
			leaked = append(leaked, res.ID)
		}
	}
	require.NotEmpty(t, leaked)
	t.Logf("negative control correctly detected escapes: %s", strings.Join(leaked, ", "))
}

// runBattery composes the real proxy + cage, runs the fake agent inside it, and
// returns the evaluated verdict. weakened strips the (deny network*) baseline for
// the negative control.
func runBattery(t *testing.T, weakened bool) batteryRun {
	t.Helper()

	// Real-location guard (AC-0035): place the cache under $HOME — NOT $TMPDIR — so it
	// falls outside safehouse's base RW grants (/tmp, $TMPDIR, toolchain dirs), matching
	// the production ~/.cache/agent-creance where the security-critical artifacts
	// (fragments, policy, audit log) live out-of-tree. A t.TempDir() cache lives under
	// $TMPDIR, which safehouse grants, and would not match production.
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	cacheDir, err := os.MkdirTemp(home, ".agent-creance-battery-")
	if errors.Is(err, os.ErrPermission) {
		// $HOME is unwritable: this process is itself sandboxed (e.g. a caged dev
		// session). sandbox-exec does not nest, so the battery cannot run here —
		// mirror runCaged's nested-sandbox skip rather than failing.
		t.Skipf("cannot create the battery cache dir under $HOME (%v); run on an unsandboxed macOS host", err)
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(cacheDir) })
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	proj := t.TempDir()

	paths := sysdep.OSPathResolver{}
	layout, err := state.New(paths).Resolve(proj)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(layout.Root, 0o700))

	// Fixtures: a planted secret OUTSIDE every cage mount (under real $HOME, a
	// non-granted subpath → denied), and dual-stack loopback listeners for the
	// allowlisted host-service port and a non-allowlisted blocked port.
	secretDir, err := os.MkdirTemp(home, ".creance-verify-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(secretDir) })
	secret := filepath.Join(secretDir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("top-secret"), 0o600))

	svcPort := dualStackListener(t)
	blockedPort := dualStackListener(t)

	// AC-0045: a THROWAWAY generic-password item in the real login keychain for the
	// kc-read/kc-write vectors — never the real Claude Code-credentials. The login
	// keychain (the default add target) is required for kc-write to exercise the
	// login.keychain-db file-write grant.
	kcService := fmt.Sprintf("agent-creance-verify-%d-%d", os.Getpid(), time.Now().UnixNano())
	kcAccount := os.Getenv("USER")
	require.NotEmpty(t, kcAccount, "$USER must be set to plant the throwaway keychain item")
	require.NoError(t,
		exec.Command("security", "add-generic-password",
			"-a", kcAccount, "-s", kcService, "-w", "verify-secret").Run(),
		"plant the throwaway keychain item")
	t.Cleanup(func() {
		_ = exec.Command("security", "delete-generic-password",
			"-a", kcAccount, "-s", kcService).Run()
	})

	// doc-claude-rw plants a marker in the REAL ~/.claude; clean it (and any
	// leftover claude-json-rw probe files) up afterwards.
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(home, ".claude", "creance-escape-marker.json"))
		leftovers, _ := filepath.Glob(filepath.Join(home, ".claude.json.creance-probe-*"))
		for _, f := range leftovers {
			_ = os.Remove(f)
		}
	})

	// Copy the fake agent + the mitmproxy CA into the RW project mount: the repo
	// path and ~/.mitmproxy are both unreadable inside the cage, so the agent runs
	// and trusts the CA from the one directory it CAN read.
	scriptSrc, err := os.ReadFile("testdata/fake-agent.sh")
	require.NoError(t, err)
	scriptPath := filepath.Join(proj, "fake-agent.sh")
	require.NoError(t, os.WriteFile(scriptPath, scriptSrc, 0o755))

	caSrc, err := os.ReadFile(filepath.Join(home, ".mitmproxy", "mitmproxy-ca-cert.pem"))
	require.NoError(t, err, "mitmproxy CA must exist (run mitmdump once / `agent-creance setup`)")
	caPath := filepath.Join(proj, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, caSrc, 0o644))

	// Compiled policy + the network baseline (weakened = baseline stripped).
	require.NoError(t, os.WriteFile(layout.PolicyJSON(), []byte(policyJSON()), 0o600))
	netSB := profile.RenderNetworkSB([]config.HostService{{Label: "svc", Port: svcPort}})
	if weakened {
		netSB = strings.Replace(netSB, profile.DenyBaseline+"\n", "", 1)
		require.NotContains(t, netSB, profile.DenyBaseline, "weakening must remove the deny baseline")
	}
	require.NoError(t, os.WriteFile(layout.NetworkSB(), []byte(netSB), 0o600))

	enforcerPy, err := proxy.NewExtractor(sysdep.OSFileSystem{}, paths).Extract()
	require.NoError(t, err)

	// Start (or attach to) the real proxy; tear it down at the end.
	mgr := proxy.NewManager(sysdep.OSFileSystem{}, sysdep.OSFlock{}, sysdep.OSProcessManager{}, sysdep.OSPortAllocator{}, os.Stderr)
	att, err := mgr.Attach(context.Background(), proxy.StartConfig{
		Layout: layout, EnforcerPy: enforcerPy, PolicyHash: "verify", SelfPID: os.Getpid(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Detach(layout, os.Getpid()) })
	require.Eventually(t, func() bool { return sysdep.OSPortAllocator{}.Probe(att.Port) },
		15*time.Second, 100*time.Millisecond, "proxy did not come up")

	egress := egressOK(t)

	cfg := &config.Config{
		Agent:     config.Agent{Command: []string{"/bin/sh", scriptPath}, Workdir: proj},
		Safehouse: config.Safehouse{AddDirsRW: []string{proj}},
		Network:   config.Network{HostServices: []config.HostService{{Label: "svc", Port: svcPort}}},
		Env: map[string]string{
			"CREANCE_PROJ":         proj,
			"CREANCE_SECRET":       secret,
			"CREANCE_HOME":         home,
			"CREANCE_CA":           caPath,
			"CREANCE_SVC_PORT":     strconv.Itoa(svcPort),
			"CREANCE_BLOCKED_PORT": strconv.Itoa(blockedPort),
			"CREANCE_ALLOW_HOST":   allowHost,
			"CREANCE_PASS_HOST":    passHost,
			"CREANCE_SOFT_HOST":    softHost,
			"CREANCE_HARD_HOST":    hardHost,
			"CREANCE_OFFPATH_URL":  "https://" + offpathHost + "/denied",
			"CREANCE_EGRESS":       boolFlag(egress),
			"CREANCE_KC_SERVICE":   kcService,
			"CREANCE_KC_ACCOUNT":   kcAccount,
		},
	}

	out := runCaged(t, cfg, layout, att.Port)
	verdict := verify.Evaluate(verify.ParseProbeOutput(out))
	return batteryRun{verdict: verdict, out: out, layout: layout, egress: egress}
}

// runCaged builds the safehouse invocation and runs the agent through the real
// cage, returning combined output. It skips the test if the host cannot nest a
// sandbox-exec policy (so an unsandboxable CI box yields neither a false pass nor
// a false fail), mirroring internal/cage/cage_integration_test.go.
func runCaged(t *testing.T, cfg *config.Config, layout state.Layout, port int) string {
	t.Helper()
	b := cage.New(sysdep.OSFileSystem{}, sysdep.OSPathResolver{})
	in, err := b.Resolve(cfg, layout, port)
	require.NoError(t, err)
	require.NoError(t, b.Prepare(in))
	inv, err := cage.Build(in)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, inv.Path, inv.Args...)
	cmd.Env = append(os.Environ(), inv.Env...)
	out, _ := cmd.CombinedOutput() // a non-zero rc is normal (probes fail by design)
	if strings.Contains(string(out), "sandbox_apply: Operation not permitted") {
		t.Skip("host cannot apply a nested sandbox-exec policy; run on an unsandboxed macOS host")
	}
	return string(out)
}

func requireUncagedHost(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("the cage is macOS-only")
	}
	for _, tool := range []string{cage.Binary, "mitmdump", "curl", "nc", "security"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH; skipping the cage-verification battery", tool)
		}
	}
}

// egressOK reports whether the host can reach the real test upstream. The egress
// ALLOWED vectors are skipped (not failed) when it cannot, matching the enforcer
// integration test's convention.
func egressOK(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "curl", "-sS", "-o", os.DevNull, "--max-time", "8",
		"https://"+allowHost+"/").Run()
	if err != nil {
		t.Logf("no network egress to %s (%v); egress ALLOWED vectors will be skipped", allowHost, err)
	}
	return err == nil
}

// dualStackListener binds a loopback listener on the same free port over both
// IPv4 (127.0.0.1) and IPv6 (::1), accept-and-closing connections so a caged
// connect that is NOT refused proves the sandbox blocked it (rather than nothing
// listening). Returns the shared port; both listeners are closed at test end.
func dualStackListener(t *testing.T) int {
	t.Helper()
	for attempt := 0; attempt < 50; attempt++ {
		port := freePort(t)
		l4, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		l6, err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port))
		if err != nil {
			_ = l4.Close()
			continue
		}
		go acceptLoop(l4)
		go acceptLoop(l6)
		t.Cleanup(func() { _ = l4.Close(); _ = l6.Close() })
		return port
	}
	t.Fatal("could not bind a dual-stack loopback listener after 50 attempts")
	return 0
}

func acceptLoop(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// assertAuditedPOST verifies the DOCUMENTED residual-exfil surface is honestly
// recorded: the POST to the allowlisted host appears in the audit log as an
// allowed request, and the agent-controlled body is NOT captured.
func assertAuditedPOST(t *testing.T, layout state.Layout) {
	t.Helper()
	log := readAudit(t, layout)
	require.Contains(t, log, `"method":"POST"`, "the POST should be audited")
	require.Contains(t, log, `"decision":"allow"`)
	require.NotContains(t, log, "creance-exfil-marker", "the POST body must NOT be recorded")
}

// assertPassthroughHostOnly verifies a passthrough host is audited host-only
// (TLS was never terminated, so no path/method/status is recorded).
func assertPassthroughHostOnly(t *testing.T, layout state.Layout) {
	t.Helper()
	for _, line := range strings.Split(readAudit(t, layout), "\n") {
		if strings.Contains(line, passHost) && strings.Contains(line, `"host"`) {
			assert.NotContains(t, line, `"method"`, "a passthrough entry must be host-only")
			assert.NotContains(t, line, `"url"`)
			return
		}
	}
	t.Errorf("no passthrough host-only audit entry for %s", passHost)
}

func readAudit(t *testing.T, layout state.Layout) string {
	t.Helper()
	var data []byte
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(layout.EgressJSONL())
		if err != nil {
			return false
		}
		data = b
		return len(b) > 0
	}, 5*time.Second, 100*time.Millisecond, "audit log never appeared")
	return string(data)
}

func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
