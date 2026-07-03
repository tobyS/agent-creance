package proxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// lockJSON mirrors the manager's unexported lockState wire format so blackbox
// tests can seed and inspect proxy.lock contents.
type lockJSON struct {
	ProxyPID      int            `json:"proxy_pid"`
	Port          int            `json:"port"`
	PolicyHash    string         `json:"policy_hash"`
	Agents        []agentRefJSON `json:"agents"`
	CanonicalPath string         `json:"canonical_path"`
}

// agentRefJSON mirrors the manager's unexported agentRef (PID + start-time identity).
type agentRefJSON struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"start"`
}

// startFor is a deterministic start time per PID so a seeded lock entry and the
// FakeProcessManager.StartTimes oracle agree; pruneDead keeps the entry only when
// both match. A recycled-PID test deliberately seeds a different oracle value.
func startFor(pid int) int64 { return int64(pid) * 1000 }

// agentRefs builds lock entries for pids, each with its deterministic start time.
func agentRefs(pids ...int) []agentRefJSON {
	refs := make([]agentRefJSON, 0, len(pids))
	for _, p := range pids {
		refs = append(refs, agentRefJSON{PID: p, StartTime: startFor(p)})
	}
	return refs
}

// agentPIDs projects seeded/read lock entries back to PIDs for assertions.
func agentPIDs(refs []agentRefJSON) []int {
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.PID)
	}
	return out
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

// cfg builds a StartConfig and registers selfPID's start time in the oracle, since
// Attach reads ProcessManager.StartTime(SelfPID) to record its own identity.
func (h *harness) cfg(selfPID int) proxy.StartConfig {
	h.proc.StartTimes[selfPID] = startFor(selfPID)
	return proxy.StartConfig{
		Layout:     h.lay,
		EnforcerPy: "/cache/agent-creance/enforcer/enforcer.py",
		PolicyHash: "hash-v1",
		SelfPID:    selfPID,
	}
}

// agentAlive marks pid as a live attached agent whose live start time matches its
// recorded lock entry, so pruneDead keeps it.
func (h *harness) agentAlive(pid int) {
	h.proc.AlivePIDs[pid] = true
	h.proc.StartTimes[pid] = startFor(pid)
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

	// Lock records the proxy + the self PID with its start time.
	ls := h.readLock(t)
	assert.Equal(t, 111, ls.ProxyPID)
	assert.Equal(t, 8080, ls.Port)
	assert.Equal(t, "hash-v1", ls.PolicyHash)
	assert.Equal(t, []agentRefJSON{{PID: 222, StartTime: startFor(222)}}, ls.Agents)
	assert.Equal(t, h.lay.Canonical, ls.CanonicalPath, "Attach records the project path for status")

	// Lock was acquired and released around the work.
	assert.Equal(t, []string{h.lay.ProxyLock()}, h.flock.Acquired)
	assert.Equal(t, []string{h.lay.ProxyLock()}, h.flock.Released)
}

func TestSecondAttachDoesNotStartSecondProxy(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: agentRefs(555)})
	h.proc.AlivePIDs[111] = true // proxy alive
	h.agentAlive(555)            // existing agent alive (PID + matching start time)
	h.ports.Listening[8080] = true

	att, err := h.mgr.Attach(context.Background(), h.cfg(666))
	require.NoError(t, err)

	assert.Equal(t, 8080, att.Port)
	assert.Equal(t, 111, att.ProxyPID)
	assert.Empty(t, h.proc.Spawned, "no second proxy should start")

	ls := h.readLock(t)
	assert.Equal(t, []int{555, 666}, agentPIDs(ls.Agents))
}

func TestLastOutTearsDownAndPurgesOverlay(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: agentRefs(222)})
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
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: agentRefs(222, 333), CanonicalPath: h.lay.Canonical})
	h.proc.AlivePIDs[111] = true
	h.fs.Files[h.lay.SessionOverlay()] = []byte("once: rules")

	require.NoError(t, h.mgr.Detach(h.lay, 222))

	assert.Empty(t, h.proc.Signaled, "proxy must not be signalled on a non-final exit")
	_, ok := h.fs.Files[h.lay.SessionOverlay()]
	assert.True(t, ok, "overlay must survive a non-final exit")

	ls := h.readLock(t)
	assert.Equal(t, 111, ls.ProxyPID)
	assert.Equal(t, 8080, ls.Port)
	assert.Equal(t, []agentRefJSON{{PID: 333, StartTime: startFor(333)}}, ls.Agents,
		"the surviving agent keeps its full identity record across a non-final exit")
	assert.Equal(t, h.lay.Canonical, ls.CanonicalPath, "the project path survives a non-final exit")
}

func TestDeadAgentPidPruned(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: agentRefs(999, 222)})
	h.proc.AlivePIDs[111] = true
	h.agentAlive(222) // 999 is absent from AlivePIDs ⇒ dead
	h.ports.Listening[8080] = true

	_, err := h.mgr.Attach(context.Background(), h.cfg(333))
	require.NoError(t, err)

	assert.Empty(t, h.proc.Spawned, "live proxy should not be restarted")
	ls := h.readLock(t)
	assert.Equal(t, []int{222, 333}, agentPIDs(ls.Agents), "stale PID 999 should be pruned")
}

// TestRecycledAgentPidPruned: an attached agent's PID is still "alive" (kill -0
// succeeds) but now belongs to a different process — its live start time no longer
// matches the one recorded at attach. The stale entry must be pruned so it cannot
// pin the proxy or block clean (AC-0061 / F13).
func TestRecycledAgentPidPruned(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: agentRefs(999, 222)})
	h.proc.AlivePIDs[111] = true
	h.agentAlive(222)                          // genuine survivor: PID alive, start time matches
	h.proc.AlivePIDs[999] = true               // PID 999 is alive again...
	h.proc.StartTimes[999] = startFor(999) + 1 // ...but as a DIFFERENT process
	h.ports.Listening[8080] = true

	_, err := h.mgr.Attach(context.Background(), h.cfg(333))
	require.NoError(t, err)

	assert.Empty(t, h.proc.Spawned, "live proxy should not be restarted")
	ls := h.readLock(t)
	assert.Equal(t, []int{222, 333}, agentPIDs(ls.Agents),
		"recycled PID 999 (alive but a different process) should be pruned")
}

// TestAttachFailsWhenOwnStartTimeUnreadable: Attach records its own identity by
// reading ProcessManager.StartTime(SelfPID); if that read fails it must surface the
// error rather than store a bogus identity that would prune itself on the next run.
func TestAttachFailsWhenOwnStartTimeUnreadable(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true
	cfg := h.cfg(222)
	delete(h.proc.StartTimes, 222) // our own start time is unreadable

	_, err := h.mgr.Attach(context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "own start time")
}

func TestCrashRestartReclaimsPort(t *testing.T) {
	h := newHarness()
	// Proxy PID dead (absent), but an agent is still attached → crash.
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: agentRefs(222)})
	h.agentAlive(222)
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
	assert.Equal(t, []int{222, 444}, agentPIDs(ls.Agents))
	assert.Equal(t, 333, ls.ProxyPID)
}

func TestCrashRestartReclaimFailWarnsNeverKills(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: agentRefs(222, 777)})
	h.agentAlive(222)
	h.agentAlive(777)
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
	assert.Equal(t, []int{222}, agentPIDs(ls.Agents))
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
	// S6: the readiness-timeout path points at doctor (it did not before).
	assert.Contains(t, err.Error(), "agent-creance doctor")
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
	// S6: a spawn failure points at doctor.
	assert.Contains(t, err.Error(), "agent-creance doctor")
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

func TestAttachDeliversInjectionSecretOnSpawn(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true

	cfg := h.cfg(222)
	called := false
	cfg.Secrets = func(context.Context) ([]byte, error) {
		called = true
		return []byte(`{"gh":"tok3n"}`), nil
	}
	_, err := h.mgr.Attach(context.Background(), cfg)
	require.NoError(t, err)

	require.True(t, called, "Secrets must be resolved on the spawn path")
	require.Len(t, h.proc.Spawned, 1)
	require.Len(t, h.proc.Secrets, 1, "the proxy was spawned via SpawnWithSecret")
	assert.Equal(t, `{"gh":"tok3n"}`, string(h.proc.Secrets[0]))
	assert.Contains(t, h.proc.Spawned[0].Args, "creance_secret_fd="+strconv.Itoa(sysdep.SecretFD))
	// Hygiene: the payload never rides argv.
	for _, a := range h.proc.Spawned[0].Args {
		assert.NotContains(t, a, "tok3n", "secret must not appear in argv")
	}
}

func TestAttachReuseDoesNotResolveSecret(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "hash-v1", Agents: agentRefs(555)})
	h.proc.AlivePIDs[111] = true
	h.agentAlive(555)
	h.ports.Listening[8080] = true

	cfg := h.cfg(666)
	called := false
	cfg.Secrets = func(context.Context) ([]byte, error) {
		called = true
		return []byte(`{"gh":"tok3n"}`), nil
	}
	_, err := h.mgr.Attach(context.Background(), cfg)
	require.NoError(t, err)

	assert.False(t, called, "reuse path must not resolve secrets (no Touch ID re-prompt)")
	assert.Empty(t, h.proc.Spawned, "no proxy spawned on reuse")
	assert.Empty(t, h.proc.Secrets)
}

func TestAttachNoSecretsSpawnsPlain(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true

	cfg := h.cfg(222)
	cfg.Secrets = func(context.Context) ([]byte, error) { return nil, nil } // nothing to inject
	_, err := h.mgr.Attach(context.Background(), cfg)
	require.NoError(t, err)

	require.Len(t, h.proc.Spawned, 1)
	assert.Empty(t, h.proc.Secrets, "no secret delivered when the payload is empty")
	for _, a := range h.proc.Spawned[0].Args {
		assert.NotContains(t, a, "creance_secret_fd", "no fd option without a payload")
	}
}

func TestAttachSecretResolverErrorSpawnsPlain(t *testing.T) {
	h := newHarness()
	h.ports.AllocPort = 8080
	h.proc.SpawnPID = 111
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true

	cfg := h.cfg(222)
	cfg.Secrets = func(context.Context) ([]byte, error) { return nil, assert.AnError }
	_, err := h.mgr.Attach(context.Background(), cfg)
	require.NoError(t, err, "a resolver error must not fail the attach (fail-closed at request time)")

	require.Len(t, h.proc.Spawned, 1)
	assert.Empty(t, h.proc.Secrets)
	assert.Contains(t, h.warn.String(), "resolving injection credentials")
}
