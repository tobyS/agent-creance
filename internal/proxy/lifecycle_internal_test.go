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

func TestAddRemoveRef(t *testing.T) {
	refs := []agentRef{{PID: 1, StartTime: 11}, {PID: 2, StartTime: 22}, {PID: 3, StartTime: 33}}

	// addRef is idempotent on PID (a re-attach of the same PID keeps one entry).
	if got := addRef(refs, agentRef{PID: 2, StartTime: 999}); len(got) != 3 {
		t.Errorf("addRef existing changed length: %v", got)
	}
	if got := addRef(refs, agentRef{PID: 4, StartTime: 44}); len(got) != 4 || got[3].PID != 4 {
		t.Errorf("addRef new = %v, want [...,{4 44}]", got)
	}
	if got := removeRef(refs, 2); len(got) != 2 || got[0].PID != 1 || got[1].PID != 3 {
		t.Errorf("removeRef = %v, want PIDs [1 3]", got)
	}
	if got := removeRef(refs, 9); len(got) != 3 {
		t.Errorf("removeRef absent should keep all, got %v", got)
	}
	if got := pids(refs); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("pids = %v, want [1 2 3]", got)
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
