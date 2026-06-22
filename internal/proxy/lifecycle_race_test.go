package proxy_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// TestConcurrentAttachDetach exercises the flock-guarded read-modify-write under
// the race detector: N invocations each Attach then Detach against one Manager
// sharing one FakeFlock (whose per-path mutex models advisory exclusion). After
// all complete, the lock is empty and the proxy was torn down exactly once.
//
// Run with `go test -race`. The FakeFlock serialises the critical section, so the
// agents array never tears even though the goroutines race to enter it.
func TestConcurrentAttachDetach(t *testing.T) {
	fl := sysdeptest.NewFakeFlock()
	pm := sysdeptest.NewFakeProcessManager()
	pa := sysdeptest.NewFakePortAllocator()
	fs := sysdeptest.NewFakeFileSystem()

	// The first proxy spawned gets PID 1000 and is "alive" + listening, so later
	// Attach calls attach rather than re-spawn.
	pm.SpawnPID = 1000
	pm.AlivePIDs[1000] = true
	pa.AllocPort = 8080
	pa.Listening[8080] = true

	mgr := proxy.NewManager(fs, fl, pm, pa, &sysdeptest.FakeSleeper{}, nil)
	lay := testLayout()

	const n = 16
	// Mark every worker PID alive with a start time that matches what Attach records,
	// so none is pruned mid-flight (the identity check sees a consistent process).
	for i := 0; i < n; i++ {
		pm.AlivePIDs[1+i] = true
		pm.StartTimes[1+i] = int64(1+i) * 1000
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(pid int) {
			defer wg.Done()
			cfg := proxy.StartConfig{Layout: lay, EnforcerPy: "/e/enforcer.py", PolicyHash: "h", SelfPID: pid}
			_, err := mgr.Attach(context.Background(), cfg)
			assert.NoError(t, err)
			assert.NoError(t, mgr.Detach(lay, pid))
		}(1 + i)
	}
	wg.Wait()

	// The proxy was started at least once, and every started proxy generation was
	// cleanly torn down (a generation ends when its agents drain to empty). Because
	// the run ends with no agents, spawns and teardowns balance — the refcount never
	// leaked a live proxy nor double-killed one.
	assert.GreaterOrEqual(t, len(pm.Spawned), 1, "the shared proxy should start at least once")
	assert.Equal(t, len(pm.Spawned), len(pm.Signaled),
		"every spawned proxy generation should be torn down exactly once")

	// Every acquire was matched by a release — no leaked lock.
	assert.Equal(t, len(fl.Acquired), len(fl.Released))

	// Final lock state: no agents left.
	var ls lockJSON
	require.NoError(t, json.Unmarshal(fl.Contents[lay.ProxyLock()], &ls))
	assert.Empty(t, ls.Agents, "all agents should have detached")
}
