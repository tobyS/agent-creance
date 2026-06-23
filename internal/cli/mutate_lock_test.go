package cli

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// These tests cover AC-0059 F9: config mutations take an advisory lock around the
// read-modify-write so two concurrent runs cannot lose a rule, and a failed lock
// acquire aborts before any write.

func TestMutateAcquiresConfigLockForTheTargetFile(t *testing.T) {
	f := newMutateFixture(t)
	fl := f.app.Flock.(*sysdeptest.FakeFlock)

	require.NoError(t, runAllow(context.Background(), f.app, mutProjDir, "api.github.com", false, false))

	wantLock, err := state.New(f.paths).ConfigLock(mutProjDir + "/.agent-creance.yaml")
	require.NoError(t, err)
	require.Equal(t, []string{wantLock}, fl.Acquired, "config lock acquired once for the project file")
	require.Equal(t, []string{wantLock}, fl.Released, "config lock released after the write")
}

func TestMutateLockAcquireFailureAbortsBeforeWrite(t *testing.T) {
	f := newMutateFixture(t)
	fl := f.app.Flock.(*sysdeptest.FakeFlock)
	fl.AcquireErr = errors.New("flock busy")

	before := append([]byte(nil), f.projectConfig(t)...)
	err := runAllow(context.Background(), f.app, mutProjDir, "api.github.com", false, false)
	require.Error(t, err)
	// The config is untouched and no policy was compiled: the lock failure stopped the
	// read-modify-write before it could write anything.
	require.Equal(t, before, f.projectConfig(t))
	require.NotContains(t, f.fs.Files, f.layout.PolicyJSON())
}

func TestConcurrentAllowAndDenyBothLand(t *testing.T) {
	f := newMutateFixture(t)

	// Run an allow and a deny concurrently against the same project file. The
	// FakeFlock's per-path mutex genuinely serialises the two critical sections, so
	// the second writer reads the first writer's result and neither rule is lost —
	// crucially not the deny_always (the dangerous direction the review flagged).
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = runAllow(context.Background(), f.app, mutProjDir, "allow.example", false, false)
	}()
	go func() {
		defer wg.Done()
		_ = runDeny(context.Background(), f.app, mutProjDir, "deny.example", "blocked")
	}()
	wg.Wait()

	cfg, err := config.Parse(f.projectConfig(t))
	require.NoError(t, err)
	findHost(t, cfg.Network.Egress.Allow, "allow.example")
	denied := findHost(t, cfg.Network.Egress.DenyAlways, "deny.example")
	require.Equal(t, "blocked", denied.Reason)
}
