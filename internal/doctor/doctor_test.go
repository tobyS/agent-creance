package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/setup"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const (
	checkerHome = "/home/toby"
	checkerCwd  = "/home/toby/proj"
	checkerProj = "/home/toby/proj"
)

// errBoom is a sentinel used to drive the environment-failure branches.
var errBoom = errors.New("boom")

// caCertPath mirrors the path setup.Installer.caCertPath resolves, given the home.
var caCertPath = filepath.Join(checkerHome, ".mitmproxy", "mitmproxy-ca-cert.pem")

// lockJSON mirrors proxy's unexported lockState wire format.
type lockJSON struct {
	ProxyPID   int            `json:"proxy_pid"`
	Port       int            `json:"port"`
	PolicyHash string         `json:"policy_hash"`
	Agents     []agentRefJSON `json:"agents"`
}

// agentRefJSON mirrors proxy's unexported agentRef (PID + start-time identity).
type agentRefJSON struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"start"`
}

// checkerHarness bundles a Checker and its fakes.
type checkerHarness struct {
	chk    *Checker
	fs     *sysdeptest.FakeFileSystem
	proc   *sysdeptest.FakeProcessManager
	ports  *sysdeptest.FakePortAllocator
	prober *sysdeptest.FakeTLSProber
	flock  *sysdeptest.FakeFlock
	paths  *sysdeptest.FakePathResolver
	fstype *sysdeptest.FakeFilesystemTyper
	listen *sysdeptest.FakeListenerScanner
}

func newCheckerHarness() *checkerHarness {
	fsys := sysdeptest.NewFakeFileSystem()
	kc := sysdeptest.NewFakeKeychain()
	proc := sysdeptest.NewFakeProcessManager()
	ports := sysdeptest.NewFakePortAllocator()
	prober := sysdeptest.NewFakeTLSProber()
	sleeper := &sysdeptest.FakeSleeper{}
	flock := sysdeptest.NewFakeFlock()
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = checkerHome
	paths.Cwd = checkerCwd
	fstype := sysdeptest.NewFakeFilesystemTyper()
	listen := &sysdeptest.FakeListenerScanner{}

	inst := setup.NewInstaller(fsys, kc, proc, ports, prober, sleeper, paths)
	mgr := proxy.NewManager(fsys, flock, proc, ports, &sysdeptest.FakeSleeper{}, nil)
	cmd := sysdeptest.NewFakeCommander().
		WithTool("agent-safehouse", "/usr/local/bin/agent-safehouse", "agent-safehouse 1.0.0").
		WithTool("mitmproxy", "/usr/local/bin/mitmproxy", "Mitmproxy: 12.0.0")
	chk := &Checker{
		Commander: cmd,
		Tested:    map[string]string{"agent-safehouse": "1.0.0", "mitmproxy": "12.0.0"},
		Installer: inst,
		Manager:   mgr,
		Resolver:  state.New(paths),
		Listeners: listen,
		FSType:    fstype,
		Paths:     paths,
	}
	return &checkerHarness{
		chk: chk, fs: fsys, proc: proc, ports: ports, prober: prober,
		flock: flock, paths: paths, fstype: fstype, listen: listen,
	}
}

// projectLock returns the proxy.lock path the Resolver computes for checkerProj.
func (h *checkerHarness) projectLock(t *testing.T) string {
	t.Helper()
	layout, err := h.chk.Resolver.Resolve(checkerProj)
	require.NoError(t, err)
	return layout.ProxyLock()
}

func (h *checkerHarness) seedLock(t *testing.T, ls lockJSON) {
	t.Helper()
	data, err := json.Marshal(ls)
	require.NoError(t, err)
	// Resolve(".") and Resolve(checkerProj) hash the same canonical cwd.
	h.flock.Contents[h.projectLock(t)] = data
}

// withCA marks the mitmproxy CA as generated so checkCA proceeds to Verify.
func (h *checkerHarness) withCA() *checkerHarness {
	h.fs.Files[caCertPath] = []byte("-----BEGIN CERTIFICATE-----")
	return h
}

func TestRun_HealthyTrustedCA(t *testing.T) {
	h := newCheckerHarness().withCA() // Verify defaults to ProbeTrusted

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusOK, rep.CA.State)
	assert.Equal(t, "trusted", rep.CA.Detail)
	assert.Empty(t, rep.Actionable())
}

func TestRun_UntrustedCAIsActionable(t *testing.T) {
	h := newCheckerHarness().withCA()
	h.prober.Outcome = sysdep.ProbeUntrusted

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusProblem, rep.CA.State)
	assert.Contains(t, rep.Actionable(), "untrusted CA")
}

func TestRun_CANotGeneratedIsWarning(t *testing.T) {
	h := newCheckerHarness() // no CA file

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusWarn, rep.CA.State)
	assert.NotContains(t, rep.Actionable(), "untrusted CA")
	// Read-only: must not have spawned mitmdump to generate the CA.
	assert.Empty(t, h.proc.Spawned, "doctor must not generate a CA")
}

func TestRun_CAVerifyEnvErrorIsWarning(t *testing.T) {
	h := newCheckerHarness().withCA()
	h.proc.SpawnErr = errBoom // Verify can't spawn mitmdump

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusWarn, rep.CA.State)
	assert.Empty(t, rep.Actionable())
}

func TestRun_OrphanActionableThenFixed(t *testing.T) {
	h := newCheckerHarness().withCA()
	h.seedLock(t, lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []agentRefJSON{{PID: 999, StartTime: 999000}}})
	h.proc.AlivePIDs[111] = true   // proxy alive
	h.ports.Listening[8080] = true // and listening; 999 dead ⇒ orphan

	// Without --fix: orphan is actionable.
	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	assert.True(t, rep.Proxy.Diag.Orphan)
	assert.Contains(t, rep.Actionable(), "orphan proxy")

	// With --fix: orphan cleaned, no longer actionable.
	repFixed, err := h.chk.Run(context.Background(), true)
	require.NoError(t, err)
	require.NotNil(t, repFixed.Proxy.Cleaned)
	assert.True(t, repFixed.Proxy.Cleaned.Cleaned)
	assert.NotContains(t, repFixed.Actionable(), "orphan proxy")
	// The orphan proxy (pid 111) was SIGTERM'd. (Verify also tears down its own
	// throwaway mitmdump, so filter to the orphan's PID.)
	var orphanSignals int
	for _, s := range h.proc.Signaled {
		if s.PID == 111 {
			orphanSignals++
			assert.Equal(t, syscall.SIGTERM, s.Sig)
		}
	}
	assert.Equal(t, 1, orphanSignals, "orphan proxy 111 should be signalled exactly once")
}

func TestRun_ExposedListenersWarn(t *testing.T) {
	h := newCheckerHarness().withCA()
	h.listen.List = []sysdep.Listener{
		{Command: "node", PID: 501, Address: "*:8080"},         // exposed
		{Command: "redis", PID: 22, Address: "127.0.0.1:6379"}, // loopback, not exposed
	}

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusWarn, rep.Exposed.State)
	require.Len(t, rep.Exposed.Listeners, 1)
	assert.Equal(t, "node", rep.Exposed.Listeners[0].Command)
	assert.Empty(t, rep.Actionable(), "exposed services are a warning, not actionable")
}

func TestRun_ExposedScanFailedSkipped(t *testing.T) {
	h := newCheckerHarness().withCA()
	h.listen.Err = errBoom

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusSkipped, rep.Exposed.State)
}

func TestRun_FilesystemWarnsOnNetworkAndICloud(t *testing.T) {
	h := newCheckerHarness().withCA()
	// cwd on SMB; cache dir defaults to local apfs but resolves under an iCloud path.
	h.fstype.Types[checkerCwd] = sysdep.FSInfo{Name: "smbfs", Local: false}
	cacheDir, err := h.chk.Resolver.CacheDir()
	require.NoError(t, err)
	h.paths.Symlinks = map[string]string{
		cacheDir: "/home/toby/Library/Mobile Documents/com~apple~CloudDocs/.cache/agent-creance",
	}

	rep, err := h.chk.Run(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, StatusWarn, rep.FS.State)
	require.Len(t, rep.FS.Warnings, 2)
	assert.Equal(t, "network mount", rep.FS.Warnings[0].Reason)
	assert.Equal(t, "iCloud Drive", rep.FS.Warnings[1].Reason)
	assert.Empty(t, rep.Actionable())
}
