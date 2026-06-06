//go:build integration

// This test exercises the real OSProcessGroup end to end on the host: it starts a
// child that spawns a long-lived descendant in the same (new) process group, then
// forwards SIGTERM via kill(-pgid, sig) and asserts BOTH the leader and the
// descendant are gone. It needs no external tool (no safehouse), so it runs on any
// macOS host under `make test-integration` and directly validates AC-0024's
// acceptance criteria at the syscall layer. The through-safehouse composition is
// covered separately in internal/cage. Liveness is probed with kill(pid, 0) (the
// OSProcessManager.Alive seam) rather than ps/pgrep, which keeps it robust on
// hosts that restrict process enumeration.
package sysdep_test

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestOSProcessGroupTearsDownChildTree(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("agent-creance is macOS-only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pidFile := t.TempDir() + "/sleep.pid"
	// sh leads the new group; `sleep 300 &` (non-interactive sh = no job control)
	// stays in the SAME group. We record the backgrounded sleep's PID so we can
	// probe it directly, and `wait` keeps the leader alive until teardown.
	script := "sleep 300 & echo $! > " + pidFile + "; wait"
	proc, err := sysdep.OSProcessGroup{}.Start(ctx, nil, "/bin/sh", "-c", script)
	require.NoError(t, err)
	pgid := proc.Pgid() // == leader (sh) PID
	require.Positive(t, pgid)

	var alive sysdep.OSProcessManager

	// Wait until sh has forked sleep and recorded its PID, and confirm the leader is
	// running. The descendant being killed by a GROUP signal is what proves it is in
	// our process group (if it weren't, kill(-pgid) would miss it and it would survive).
	var sleepPID int
	require.Eventually(t, func() bool {
		sleepPID = readPID(pidFile)
		return sleepPID > 0 && alive.Alive(sleepPID)
	}, 5*time.Second, 20*time.Millisecond, "child tree never came up in the new group")
	require.True(t, alive.Alive(pgid), "leader should be running before teardown")

	// Forward SIGTERM to the whole group, then reap the leader.
	require.NoError(t, proc.Signal(syscall.SIGTERM))
	require.Error(t, proc.Wait(), "a SIGTERM'd group should not exit 0")

	// Neither the leader nor the backgrounded descendant may survive the group signal.
	require.Eventually(t, func() bool { return !alive.Alive(sleepPID) && !alive.Alive(pgid) },
		5*time.Second, 20*time.Millisecond, "leader or descendant survived the group SIGTERM")
}

// readPID reads a PID written to path, or 0 if the file is absent/empty/unparseable.
func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}
