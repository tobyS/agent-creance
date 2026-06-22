package proxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// lockJSON mirrors the manager's unexported lockState wire format so blackbox
// tests can seed and inspect proxy.lock contents.
type lockJSON struct {
	ProxyPID      int    `json:"proxy_pid"`
	Port          int    `json:"port"`
	PolicyHash    string `json:"policy_hash"`
	Agents        []int  `json:"agents"`
	CanonicalPath string `json:"canonical_path"`
}

const projectRoot = "/cache/agent-creance/projects/abcd1234ef567890"

func testLayout() state.Layout {
	return state.Layout{
		Canonical: "/home/toby/proj",
		Hash:      "abcd1234ef567890",
		Root:      projectRoot,
	}
}

// harness bundles a Manager and its fakes for a test.
type harness struct {
	mgr   *proxy.Manager
	flock *sysdeptest.FakeFlock
	proc  *sysdeptest.FakeProcessManager
	ports *sysdeptest.FakePortAllocator
	fs    *sysdeptest.FakeFileSystem
	sleep *sysdeptest.FakeSleeper
	warn  *bytes.Buffer
	lay   state.Layout
}

func newHarness() *harness {
	fl := sysdeptest.NewFakeFlock()
	pm := sysdeptest.NewFakeProcessManager()
	pa := sysdeptest.NewFakePortAllocator()
	fs := sysdeptest.NewFakeFileSystem()
	sl := &sysdeptest.FakeSleeper{}
	warn := &bytes.Buffer{}
	return &harness{
		mgr:   proxy.NewManager(fs, fl, pm, pa, sl, warn),
		flock: fl,
		proc:  pm,
		ports: pa,
		fs:    fs,
		sleep: sl,
		warn:  warn,
		lay:   testLayout(),
	}
}

func (h *harness) seedLock(ls lockJSON) {
	data, err := json.Marshal(ls)
	if err != nil {
		panic(err)
	}
	h.flock.Contents[h.lay.ProxyLock()] = data
}

func (h *harness) readLock(t *testing.T) lockJSON {
	t.Helper()
	var ls lockJSON
	require.NoError(t, json.Unmarshal(h.flock.Contents[h.lay.ProxyLock()], &ls))
	return ls
}

func (h *harness) cfg(selfPID int) proxy.StartConfig {
	return proxy.StartConfig{
		Layout:     h.lay,
		EnforcerPy: "/cache/agent-creance/enforcer/enforcer.py",
		PolicyHash: "hash-v1",
		SelfPID:    selfPID,
	}
}

func TestAttachStartsProxyWhenNone(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	h.proc.AlivePIDs[111] = true   // the spawned proxy is alive...
	h.ports.Listening[8080] = true // ...and listening, so the readiness wait passes

	att, err := h.mgr.Attach(context.Background(), h.cfg(222))
	require.NoError(t, err)

	assert.Equal(t, 8080, att.Port)
	assert.Equal(t, 111, att.ProxyPID)
	assert.False(t, att.PortChanged)

	// Exactly one mitmproxy started, with the right port + addon.
	require.Len(t, h.proc.Spawned, 1)
	spawned := h.proc.Spawned[0]
	assert.Equal(t, "mitmdump", spawned.Name)
	assert.Contains(t, spawned.Args, "8080")
	assert.Contains(t, spawned.Args, "/cache/agent-creance/enforcer/enforcer.py")
	assert.Contains(t, spawned.Args, "creance_policy="+h.lay.PolicyJSON())

	// Lock records the proxy + the self PID.
	ls := h.readLock(t)
	assert.Equal(t, 111, ls.ProxyPID)
	assert.Equal(t, 8080, ls.Port)
	assert.Equal(t, "hash-v1", ls.PolicyHash)
	assert.Equal(t, []int{222}, ls.Agents)
	assert.Equal(t, h.lay.Canonical, ls.CanonicalPath, "Attach records the project path for status")

	// Lock was acquired and released around the work.
	assert.Equal(t, []string{h.lay.ProxyLock()}, h.flock.Acquired)
	assert.Equal(t, []string{h.lay.ProxyLock()}, h.flock.Released)
}

func TestSecondAttachDoesNotStartSecondProxy(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: []int{555}})
	h.proc.AlivePIDs[111] = true // proxy alive
	h.proc.AlivePIDs[555] = true // existing agent alive
	h.ports.Listening[8080] = true

	att, err := h.mgr.Attach(context.Background(), h.cfg(666))
	require.NoError(t, err)

	assert.Equal(t, 8080, att.Port)
	assert.Equal(t, 111, att.ProxyPID)
	assert.Empty(t, h.proc.Spawned, "no second proxy should start")

	ls := h.readLock(t)
	assert.Equal(t, []int{555, 666}, ls.Agents)
}

func TestLastOutTearsDownAndPurgesOverlay(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: []int{222}})
	h.proc.AlivePIDs[111] = true
	h.fs.Files[h.lay.SessionOverlay()] = []byte("once: rules") // overlay present

	require.NoError(t, h.mgr.Detach(h.lay, 222))

	// Proxy SIGTERM'd by PID.
	require.Len(t, h.proc.Signaled, 1)
	assert.Equal(t, 111, h.proc.Signaled[0].PID)
	assert.Equal(t, syscall.SIGTERM, h.proc.Signaled[0].Sig)

	// Overlay purged.
	_, ok := h.fs.Files[h.lay.SessionOverlay()]
	assert.False(t, ok, "session overlay should be removed on last-out")

	// Lock cleared of proxy + agents.
	ls := h.readLock(t)
	assert.Zero(t, ls.ProxyPID)
	assert.Empty(t, ls.Agents)
}

func TestNonFinalExitLeavesProxyAndOverlay(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: []int{222, 333}, CanonicalPath: h.lay.Canonical})
	h.proc.AlivePIDs[111] = true
	h.fs.Files[h.lay.SessionOverlay()] = []byte("once: rules")

	require.NoError(t, h.mgr.Detach(h.lay, 222))

	assert.Empty(t, h.proc.Signaled, "proxy must not be signalled on a non-final exit")
	_, ok := h.fs.Files[h.lay.SessionOverlay()]
	assert.True(t, ok, "overlay must survive a non-final exit")

	ls := h.readLock(t)
	assert.Equal(t, 111, ls.ProxyPID)
	assert.Equal(t, 8080, ls.Port)
	assert.Equal(t, []int{333}, ls.Agents)
	assert.Equal(t, h.lay.Canonical, ls.CanonicalPath, "the project path survives a non-final exit")
}

func TestDeadAgentPidPruned(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: []int{999, 222}})
	h.proc.AlivePIDs[111] = true
	h.proc.AlivePIDs[222] = true // 999 is absent ⇒ dead
	h.ports.Listening[8080] = true

	_, err := h.mgr.Attach(context.Background(), h.cfg(333))
	require.NoError(t, err)

	assert.Empty(t, h.proc.Spawned, "live proxy should not be restarted")
	ls := h.readLock(t)
	assert.Equal(t, []int{222, 333}, ls.Agents, "stale PID 999 should be pruned")
}

func TestCrashRestartReclaimsPort(t *testing.T) {
	h := newHarness()
	// Proxy PID dead (absent), but an agent is still attached → crash.
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: []int{222}})
	h.proc.AlivePIDs[222] = true
	h.ports.ReclaimOK[8080] = true // old port reclaimable
	h.proc.SpawnPID = 333
	h.proc.AlivePIDs[333] = true   // the restarted proxy is alive...
	h.ports.Listening[8080] = true // ...and listening on the reclaimed port (readiness)

	att, err := h.mgr.Attach(context.Background(), h.cfg(444))
	require.NoError(t, err)

	assert.Equal(t, 8080, att.Port, "reclaimed the recorded port")
	assert.False(t, att.PortChanged)
	require.Len(t, h.proc.Spawned, 1)
	assert.Contains(t, h.proc.Spawned[0].Args, "8080")
	assert.Empty(t, h.proc.Signaled, "attached agent 222 must not be signalled")
	assert.Empty(t, h.warn.String(), "no warning when the port is reclaimed")
	assert.Equal(t, 0, h.ports.Allocations, "no fresh allocation when reclaim succeeds")

	ls := h.readLock(t)
	assert.Equal(t, []int{222, 444}, ls.Agents)
	assert.Equal(t, 333, ls.ProxyPID)
}

func TestCrashRestartReclaimFailWarnsNeverKills(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: []int{222, 777}})
	h.proc.AlivePIDs[222] = true
	h.proc.AlivePIDs[777] = true
	h.ports.Listening[8080] = false
	h.ports.ReclaimOK[8080] = false // cannot reclaim
	h.ports.AllocPort = 9090
	h.proc.SpawnPID = 333
	h.proc.AlivePIDs[333] = true   // the restarted proxy is alive...
	h.ports.Listening[9090] = true // ...and listening on the fresh port (readiness)

	att, err := h.mgr.Attach(context.Background(), h.cfg(444))
	require.NoError(t, err)

	assert.Equal(t, 9090, att.Port, "fell back to a fresh port")
	assert.True(t, att.PortChanged)
	require.Len(t, h.proc.Spawned, 1)
	assert.Contains(t, h.proc.Spawned[0].Args, "9090")

	// Warn-never-kill: the surviving agents are named and NOT signalled.
	assert.Empty(t, h.proc.Signaled, "attached agents must never be signalled")
	warn := h.warn.String()
	assert.Contains(t, warn, "222")
	assert.Contains(t, warn, "777")
	assert.Contains(t, warn, "9090")
	assert.Contains(t, warn, "8080")
}

func TestAttachCorruptLockSelfHeals(t *testing.T) {
	h := newHarness()
	h.flock.Contents[h.lay.ProxyLock()] = []byte("{ this is not json")
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true

	att, err := h.mgr.Attach(context.Background(), h.cfg(222))
	require.NoError(t, err)
	assert.Equal(t, 8080, att.Port)
	require.Len(t, h.proc.Spawned, 1, "corrupt lock should cold-start")

	ls := h.readLock(t)
	assert.Equal(t, []int{222}, ls.Agents)
}

// TestAttachFailsWhenProxyExitsDuringStartup: the proxy is spawned but exits before it
// listens (e.g. the enforcer refused a corrupt initial policy.json and exited non-zero).
// Attach must surface a hard error rather than report the proxy "ready", and best-effort
// reap the dead PID (AC-0058 / B2).
func TestAttachFailsWhenProxyExitsDuringStartup(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	// 111 absent from AlivePIDs ⇒ the spawned proxy already exited; nothing listening.

	_, err := h.mgr.Attach(context.Background(), h.cfg(222))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited during startup")
	require.Len(t, h.proc.Spawned, 1)
	// Best-effort cleanup of the half-started proxy.
	require.Len(t, h.proc.Signaled, 1)
	assert.Equal(t, 111, h.proc.Signaled[0].PID)
	// Lock released so the next invocation is not wedged.
	assert.Equal(t, []string{h.lay.ProxyLock()}, h.flock.Released)
}

// TestAttachFailsWhenProxyNeverListens: the proxy stays alive but never opens the port
// within the readiness budget. Attach times out with a clear error (the FakeSleeper
// returns instantly, so the bounded poll does not pay wall-clock time).
func TestAttachFailsWhenProxyNeverListens(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	h.proc.AlivePIDs[111] = true // alive, but Listening[8080] stays false

	_, err := h.mgr.Attach(context.Background(), h.cfg(222))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not start listening")
	// The readiness poll exhausted its attempts (one Sleep per attempt).
	assert.Len(t, h.sleep.Sleeps, 100)
}

func TestAttachMkdirError(t *testing.T) {
	h := newHarness()
	h.fs.MkdirErrs[projectRoot] = assert.AnError

	_, err := h.mgr.Attach(context.Background(), h.cfg(222))
	require.Error(t, err)
	assert.Empty(t, h.proc.Spawned)
	assert.Empty(t, h.flock.Acquired, "lock not acquired when the state dir cannot be created")
}

func TestAttachAcquireError(t *testing.T) {
	h := newHarness()
	h.flock.AcquireErr = assert.AnError

	_, err := h.mgr.Attach(context.Background(), h.cfg(222))
	require.Error(t, err)
	assert.Empty(t, h.proc.Spawned, "nothing spawned when the lock cannot be taken")
}

func TestAttachSpawnError(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnErr = assert.AnError

	_, err := h.mgr.Attach(context.Background(), h.cfg(222))
	require.Error(t, err)
	// Lock is still released (defer) so the next invocation is not wedged.
	assert.Equal(t, []string{h.lay.ProxyLock()}, h.flock.Released)
}

func TestLockPathIsOutOfTree(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true

	_, err := h.mgr.Attach(context.Background(), h.cfg(222))
	require.NoError(t, err)

	require.Len(t, h.flock.Acquired, 1)
	locked := h.flock.Acquired[0]
	assert.Equal(t, h.lay.ProxyLock(), locked)
	assert.True(t, strings.HasPrefix(locked, "/cache/agent-creance/projects/"),
		"lock must live under the out-of-tree state dir (C4), got %q", locked)
	assert.NotContains(t, locked, h.lay.Canonical, "lock must not live in the project tree")
}
