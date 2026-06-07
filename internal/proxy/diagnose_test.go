package proxy_test

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Inspect and CleanOrphan are doctor's (AC-0031) read-only diagnosis and --fix
// cleanup. They reuse the harness/seedLock helpers from lifecycle_test.go.

func TestInspectNoLock(t *testing.T) {
	h := newHarness() // no lock seeded

	diag, err := h.mgr.Inspect(h.lay)
	require.NoError(t, err)
	assert.False(t, diag.LockPresent)
	assert.False(t, diag.Orphan)
	assert.False(t, diag.Stranded)
}

func TestInspectHealthyWithAgents(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{222}})
	h.proc.AlivePIDs[111] = true // proxy alive
	h.proc.AlivePIDs[222] = true // agent alive
	h.ports.Listening[8080] = true

	diag, err := h.mgr.Inspect(h.lay)
	require.NoError(t, err)
	assert.True(t, diag.LockPresent)
	assert.True(t, diag.ProxyUp)
	assert.Equal(t, []int{222}, diag.LiveAgents)
	assert.False(t, diag.Orphan)
	assert.False(t, diag.Stranded)
}

func TestInspectOrphan(t *testing.T) {
	h := newHarness()
	// Proxy up and listening, but the only attached agent (999) is dead.
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{999}})
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true // 999 absent ⇒ dead

	diag, err := h.mgr.Inspect(h.lay)
	require.NoError(t, err)
	assert.True(t, diag.ProxyUp)
	assert.Empty(t, diag.LiveAgents)
	assert.True(t, diag.Orphan)
	assert.False(t, diag.Stranded)
}

func TestInspectStranded(t *testing.T) {
	h := newHarness()
	// A live agent is attached, but the proxy is not listening on the recorded port
	// (port changed / proxy gone) — the stranded condition.
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{222}})
	h.proc.AlivePIDs[111] = true    // PID alive...
	h.ports.Listening[8080] = false // ...but not listening
	h.proc.AlivePIDs[222] = true

	diag, err := h.mgr.Inspect(h.lay)
	require.NoError(t, err)
	assert.False(t, diag.ProxyUp)
	assert.Equal(t, []int{222}, diag.LiveAgents)
	assert.False(t, diag.Orphan)
	assert.True(t, diag.Stranded)
}

func TestCleanOrphanTearsDown(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{999}})
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true // 999 dead ⇒ orphan
	h.fs.Files[h.lay.SessionOverlay()] = []byte("once: rules")

	res, err := h.mgr.CleanOrphan(h.lay)
	require.NoError(t, err)
	assert.True(t, res.Cleaned)
	assert.Equal(t, 111, res.ProxyPID)

	// Proxy SIGTERM'd, overlay purged, lock proxy-state cleared (PolicyHash kept).
	require.Len(t, h.proc.Signaled, 1)
	assert.Equal(t, 111, h.proc.Signaled[0].PID)
	assert.Equal(t, syscall.SIGTERM, h.proc.Signaled[0].Sig)
	_, ok := h.fs.Files[h.lay.SessionOverlay()]
	assert.False(t, ok, "session overlay must be purged")

	ls := h.readLock(t)
	assert.Equal(t, 0, ls.ProxyPID)
	assert.Equal(t, 0, ls.Port)
	assert.Empty(t, ls.Agents)
	assert.Equal(t, "h", ls.PolicyHash)
}

func TestCleanOrphanNoOpWithLiveAgents(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{222}})
	h.proc.AlivePIDs[111] = true
	h.proc.AlivePIDs[222] = true // live agent ⇒ not an orphan
	h.ports.Listening[8080] = true

	res, err := h.mgr.CleanOrphan(h.lay)
	require.NoError(t, err)
	assert.False(t, res.Cleaned)
	assert.Empty(t, h.proc.Signaled, "a proxy with live agents must never be signalled")
}

func TestCleanOrphanNoOpWhenProxyDown(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{999}})
	// proxy PID not alive ⇒ not "up" ⇒ nothing to tear down.
	h.ports.Listening[8080] = false

	res, err := h.mgr.CleanOrphan(h.lay)
	require.NoError(t, err)
	assert.False(t, res.Cleaned)
	assert.Empty(t, h.proc.Signaled)
}
