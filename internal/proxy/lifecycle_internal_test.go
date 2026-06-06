package proxy

import (
	"testing"

	"github.com/tobyS/agent-creance/internal/state"
)

func TestMitmArgsShape(t *testing.T) {
	cfg := StartConfig{
		Layout:     state.Layout{Root: "/cache/agent-creance/projects/abc"},
		EnforcerPy: "/enforcer/enforcer.py",
	}
	args := mitmArgs(7777, cfg)

	// Must carry the listen port, the addon script, and both --set options.
	assertContainsPair(t, args, "--listen-port", "7777")
	assertContainsPair(t, args, "-s", "/enforcer/enforcer.py")
	assertContains(t, args, "creance_policy="+cfg.Layout.PolicyJSON())
	assertContains(t, args, "creance_audit_log="+cfg.Layout.EgressJSONL())
	assertContains(t, args, "--listen-host")
}

func TestAddRemoveContainsPID(t *testing.T) {
	pids := []int{1, 2, 3}

	if got := addPID(pids, 2); len(got) != 3 {
		t.Errorf("addPID existing changed length: %v", got)
	}
	if got := addPID(pids, 4); len(got) != 4 || got[3] != 4 {
		t.Errorf("addPID new = %v, want [...,4]", got)
	}
	if got := removePID(pids, 2); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("removePID = %v, want [1 3]", got)
	}
	if removePID(pids, 9) == nil || len(removePID(pids, 9)) != 3 {
		t.Errorf("removePID absent should keep all")
	}
	if !containsPID(pids, 3) || containsPID(pids, 9) {
		t.Errorf("containsPID wrong")
	}
}

func TestFormatPIDs(t *testing.T) {
	if got := formatPIDs([]int{10, 20, 30}); got != "10, 20, 30" {
		t.Errorf("formatPIDs = %q, want \"10, 20, 30\"", got)
	}
	if got := formatPIDs([]int{42}); got != "42" {
		t.Errorf("formatPIDs single = %q, want \"42\"", got)
	}
}

func assertContains(t *testing.T, args []string, want string) {
	t.Helper()
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Errorf("args %v missing %q", args, want)
}

func assertContainsPair(t *testing.T, args []string, flag, val string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return
		}
	}
	t.Errorf("args %v missing pair %q %q", args, flag, val)
}

// Guard against an accidental change to the lock filename the manager relies on.
func TestProxyLockFilename(t *testing.T) {
	lay := state.Layout{Root: "/r"}
	if lay.ProxyLock() != "/r/proxy.lock" {
		t.Errorf("ProxyLock = %q, want /r/proxy.lock", lay.ProxyLock())
	}
}
