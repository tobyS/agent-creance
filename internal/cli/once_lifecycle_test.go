package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/proxy"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// lockJSON mirrors the proxy manager's on-disk lock wire format so this test can seed
// proxy.lock with a single attached agent for the teardown to remove.
type lockJSON struct {
	ProxyPID   int    `json:"proxy_pid"`
	Port       int    `json:"port"`
	PolicyHash string `json:"policy_hash"`
	Agents     []int  `json:"agents"`
}

// TestOnceRulePurgedOnLastExit ties AC-0030's overlay writer to AC-0020's purge end
// to end (the ticket's Verification step 4): an `allow --once` rule is live in the
// compiled policy while the session runs, and is removed from the overlay — and so
// drops out of a recompiled policy — when the last agent detaches. The whole flow
// runs over one in-memory filesystem shared by the command and the proxy manager.
func TestOnceRulePurgedOnLastExit(t *testing.T) {
	f := newMutateFixture(t)
	ctx := context.Background()

	require.NoError(t, runAllow(ctx, f.app, mutProjDir, "ephemeral.example", true /*once*/, false))

	overlay := f.layout.SessionOverlay()
	require.Contains(t, f.fs.Files, overlay, "--once rule should write the session overlay")
	require.Contains(t, string(f.policyJSON(t)), "ephemeral.example",
		"the once rule should be live in the compiled policy")

	// Simulate AC-0020's last-agent-exit teardown over the SAME filesystem: seed the
	// lock so selfPID is the only attached agent, then Detach it.
	const proxyPID, selfPID = 4242, 777
	flock := sysdeptest.NewFakeFlock()
	lock, err := json.Marshal(lockJSON{ProxyPID: proxyPID, Port: 9000, PolicyHash: "h", Agents: []int{selfPID}})
	require.NoError(t, err)
	flock.Contents[f.layout.ProxyLock()] = lock
	proc := sysdeptest.NewFakeProcessManager()
	proc.AlivePIDs[proxyPID] = true // so the last-out Detach can SIGTERM the proxy

	mgr := proxy.NewManager(f.fs, flock, proc, sysdeptest.NewFakePortAllocator(), f.app.Stderr)
	require.NoError(t, mgr.Detach(f.layout, selfPID))

	// Overlay purged, and a recompile drops the once rule from the policy.
	require.NotContains(t, f.fs.Files, overlay, "overlay should be purged on last-agent-exit")
	require.NoError(t, recompile(ctx, f.app, mutProjDir))
	require.NotContains(t, string(f.policyJSON(t)), "ephemeral.example",
		"the purged once rule should be gone after a recompile")
}
