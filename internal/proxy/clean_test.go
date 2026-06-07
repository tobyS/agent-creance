package proxy_test

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Clean is the `agent-creance clean` primitive (AC-0032): unconditional,
// idempotent teardown of this project's proxy, refusing on live agents unless
// force is set. It reuses the harness/seedLock helpers from lifecycle_test.go.

func TestCleanTearsDownRunningProxyWithNoLiveAgents(t *testing.T) {
	h := newHarness()
	// Proxy up and listening; the only recorded agent (999) is dead.
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{999}, CanonicalPath: "/home/toby/proj"})
	h.proc.AlivePIDs[111] = true
	h.ports.Listening[8080] = true
	h.fs.Files[h.lay.SessionOverlay()] = []byte("once: rules")

	res, err := h.mgr.Clean(h.lay, false)
	require.NoError(t, err)
	assert.True(t, res.Cleaned)
	assert.Equal(t, 111, res.ProxyPID)
	assert.False(t, res.Refused)

	// Proxy SIGTERM'd, overlay purged, lock fully cleared.
	require.Len(t, h.proc.Signaled, 1)
	assert.Equal(t, 111, h.proc.Signaled[0].PID)
	assert.Equal(t, syscall.SIGTERM, h.proc.Signaled[0].Sig)
	_, ok := h.fs.Files[h.lay.SessionOverlay()]
	assert.False(t, ok, "session overlay must be purged")

	ls := h.readLock(t)
	assert.Zero(t, ls.ProxyPID)
	assert.Zero(t, ls.Port)
	assert.Empty(t, ls.Agents)
	assert.Empty(t, ls.PolicyHash)
	assert.Empty(t, ls.CanonicalPath)
}

func TestCleanIsIdempotentNoOpWhenNothingRunning(t *testing.T) {
	h := newHarness() // no lock seeded

	res, err := h.mgr.Clean(h.lay, false)
	require.NoError(t, err)
	assert.False(t, res.Cleaned)
	assert.False(t, res.Refused)
	assert.Empty(t, h.proc.Signaled, "nothing to signal")

	// A second run is equally a clean no-op.
	res2, err := h.mgr.Clean(h.lay, false)
	require.NoError(t, err)
	assert.False(t, res2.Cleaned)
	assert.False(t, res2.Refused)
}

func TestCleanRefusesWithLiveAgentsWithoutForce(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{222, 333}, CanonicalPath: "/home/toby/proj"})
	h.proc.AlivePIDs[111] = true
	h.proc.AlivePIDs[222] = true
	h.proc.AlivePIDs[333] = true
	h.ports.Listening[8080] = true
	h.fs.Files[h.lay.SessionOverlay()] = []byte("once: rules")

	res, err := h.mgr.Clean(h.lay, false)
	require.NoError(t, err)
	assert.True(t, res.Refused)
	assert.Equal(t, []int{222, 333}, res.LiveAgents)
	assert.False(t, res.Cleaned)

	// Nothing mutated: proxy not signalled, overlay intact, lock unchanged.
	assert.Empty(t, h.proc.Signaled, "a refused clean must never signal the proxy")
	_, ok := h.fs.Files[h.lay.SessionOverlay()]
	assert.True(t, ok, "a refused clean must not purge the overlay")
	ls := h.readLock(t)
	assert.Equal(t, 111, ls.ProxyPID)
	assert.Equal(t, []int{222, 333}, ls.Agents)
}

func TestCleanForceTearsDownDespiteLiveAgents(t *testing.T) {
	h := newHarness()
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{222}, CanonicalPath: "/home/toby/proj"})
	h.proc.AlivePIDs[111] = true
	h.proc.AlivePIDs[222] = true
	h.ports.Listening[8080] = true
	h.fs.Files[h.lay.SessionOverlay()] = []byte("once: rules")

	res, err := h.mgr.Clean(h.lay, true)
	require.NoError(t, err)
	assert.True(t, res.Cleaned)
	assert.Equal(t, 111, res.ProxyPID)
	assert.False(t, res.Refused)

	require.Len(t, h.proc.Signaled, 1)
	assert.Equal(t, 111, h.proc.Signaled[0].PID)
	_, ok := h.fs.Files[h.lay.SessionOverlay()]
	assert.False(t, ok, "force clean purges the overlay")
	ls := h.readLock(t)
	assert.Empty(t, ls.Agents)
}

func TestCleanPurgesOverlayWhenProxyAlreadyDead(t *testing.T) {
	h := newHarness()
	// A recorded-but-dead proxy and no live agents: nothing to signal, but the
	// stale overlay must still be purged and the lock cleared.
	h.seedLock(lockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", CanonicalPath: "/home/toby/proj"})
	h.ports.Listening[8080] = false // proxy not alive
	h.fs.Files[h.lay.SessionOverlay()] = []byte("once: rules")

	res, err := h.mgr.Clean(h.lay, false)
	require.NoError(t, err)
	assert.False(t, res.Cleaned, "no live proxy was stopped")
	assert.Empty(t, h.proc.Signaled)

	_, ok := h.fs.Files[h.lay.SessionOverlay()]
	assert.False(t, ok, "stale overlay must be purged")
	ls := h.readLock(t)
	assert.Zero(t, ls.ProxyPID)
	assert.Empty(t, ls.CanonicalPath)
}
