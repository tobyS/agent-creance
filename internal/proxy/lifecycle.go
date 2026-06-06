package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"syscall"

	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// The lifecycle manager is the flock-guarded refcount that lets multiple
// agent-creance invocations in one project share a single mitmproxy. It is the
// riskiest non-security logic in the system, so it consumes only hermetically
// testable seams: Flock (atomic read-modify-write of proxy.lock on one fd),
// ProcessManager (spawn the proxy daemon, prune dead PIDs, kill by PID on
// last-out), PortAllocator (ephemeral :0 allocation + best-effort reclaim + a
// liveness probe), and FileSystem (purge the session-overlay on last-out). It
// does NOT compile policy.json/network.sb (AC-0013/0014) or exec Safehouse /
// forward signals to the agent group (AC-0023/0024) — those are its callers.
//
// See docs/design.md, "Multi-agent lifecycle" and "Crash recovery and the port".

// proxyBin is the mitmproxy binary the manager launches. mitmdump is the
// headless, scriptable flavour (no TUI), which is what a daemon wants.
const proxyBin = "mitmdump"

// lockState is the on-disk proxy.lock contents: the single source of truth for a
// project's shared proxy. It is written in place on the locked descriptor (never
// temp+rename, which would swap the inode out from under the flock). It lives
// out-of-tree under the project state dir (cross-cutting C4), so the caged agent
// — which has ./ writable but not the cache — cannot corrupt the refcount.
type lockState struct {
	// ProxyPID is the mitmproxy process; 0 when no proxy is running.
	ProxyPID int `json:"proxy_pid"`
	// Port is the proxy's ephemeral listen port; 0 when none has been allocated.
	Port int `json:"port"`
	// PolicyHash is the compiled-policy hash the running proxy was started with.
	PolicyHash string `json:"policy_hash"`
	// Agents are the PIDs of attached agent-creance invocations.
	Agents []int `json:"agents"`
}

// Manager owns the flock-guarded proxy lifecycle for projects. Construct it with
// NewManager; one instance serves any number of projects (the project is named by
// the StartConfig.Layout passed to each call).
type Manager struct {
	fs    sysdep.FileSystem
	lock  sysdep.Flock
	proc  sysdep.ProcessManager
	ports sysdep.PortAllocator
	warn  io.Writer
}

// NewManager wires a Manager from the OS seams. warn receives the warn-never-kill
// message (the caller passes app.Stderr); the others are the injected seams.
func NewManager(fs sysdep.FileSystem, lock sysdep.Flock, proc sysdep.ProcessManager, ports sysdep.PortAllocator, warn io.Writer) *Manager {
	return &Manager{fs: fs, lock: lock, proc: proc, ports: ports, warn: warn}
}

// StartConfig is everything Attach needs to identify or launch a project's proxy.
type StartConfig struct {
	// Layout is the project's resolved state-dir layout (ProxyLock, SessionOverlay,
	// PolicyJSON, EgressJSONL, Root).
	Layout state.Layout
	// EnforcerPy is the extracted addon entrypoint (from proxy.Extractor.Extract).
	EnforcerPy string
	// PolicyHash is the current compiled-policy hash, recorded in the lock.
	PolicyHash string
	// SelfPID is this invocation's PID (os.Getpid()), added to the agents array.
	SelfPID int
}

// Attachment is what Attach reports back to its caller.
type Attachment struct {
	// Port is the live proxy port the caller must point the cage at.
	Port int
	// ProxyPID is the live mitmproxy process.
	ProxyPID int
	// PortChanged is true when a crash restart could not reclaim the recorded port
	// (the warn-never-kill condition); the caller's own network.sb is unaffected,
	// but already-attached agents were warned.
	PortChanged bool
}

// Attach ensures a live proxy for the project and records SelfPID as attached,
// all under the proxy.lock flock. It prunes dead agent PIDs, verifies the proxy
// is genuinely alive (PID liveness AND a port probe — a recycled PID alone is not
// trusted), and starts mitmproxy if none is running or it crashed. On a crash
// restart it best-effort reclaims the recorded port so already-attached agents
// recover transparently; if reclaim fails while agents remain attached it emits
// the documented warning naming them and never signals them.
func (m *Manager) Attach(ctx context.Context, cfg StartConfig) (Attachment, error) {
	if err := m.fs.MkdirAll(cfg.Layout.Root, dirPerm); err != nil {
		return Attachment{}, fmt.Errorf("proxy: create state dir %q: %w", cfg.Layout.Root, err)
	}
	lf, err := m.lock.Acquire(cfg.Layout.ProxyLock())
	if err != nil {
		return Attachment{}, fmt.Errorf("proxy: acquire lock: %w", err)
	}
	defer func() { _ = lf.Release() }()

	cur, err := readLock(lf)
	if err != nil {
		return Attachment{}, err
	}

	// Prune dead agents from the previous run before any decision.
	alive := m.pruneDead(cur.Agents)

	// The proxy counts as up only if its PID is alive AND it is actually listening
	// — a bare PID may have been recycled into an unrelated process.
	proxyUp := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) && m.ports.Probe(cur.Port)

	att := Attachment{}
	if proxyUp {
		att.Port = cur.Port
		att.ProxyPID = cur.ProxyPID
	} else {
		port, changed, err := m.choosePort(cur.Port)
		if err != nil {
			return Attachment{}, err
		}
		if changed && len(alive) > 0 {
			// Warn-never-kill: a restart on a new port strands agents whose frozen
			// Seatbelt profile only allows the old port. Warn, do NOT signal them.
			m.warnPortChanged(port, cur.Port, alive)
		}
		pid, err := m.proc.Spawn(ctx, proxyBin, mitmArgs(port, cfg)...)
		if err != nil {
			return Attachment{}, fmt.Errorf("proxy: start mitmproxy: %w", err)
		}
		att.Port = port
		att.ProxyPID = pid
		att.PortChanged = changed
	}

	agents := addPID(alive, cfg.SelfPID)
	next := lockState{ProxyPID: att.ProxyPID, Port: att.Port, PolicyHash: cfg.PolicyHash, Agents: agents}
	if err := writeLock(lf, next); err != nil {
		return Attachment{}, err
	}
	return att, nil
}

// Detach removes selfPID from the project's lock under the flock. If it was the
// last agent, it kills the proxy (by PID — this invocation may not be the one that
// started it) and purges the session-overlay; a non-final exit does neither.
func (m *Manager) Detach(layout state.Layout, selfPID int) error {
	lf, err := m.lock.Acquire(layout.ProxyLock())
	if err != nil {
		return fmt.Errorf("proxy: acquire lock: %w", err)
	}
	defer func() { _ = lf.Release() }()

	cur, err := readLock(lf)
	if err != nil {
		return err
	}
	agents := removePID(cur.Agents, selfPID)

	if len(agents) == 0 {
		// Last agent out: tear the proxy down and purge the session overlay.
		if cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) {
			if err := m.proc.Signal(cur.ProxyPID, syscall.SIGTERM); err != nil {
				return fmt.Errorf("proxy: stop mitmproxy: %w", err)
			}
		}
		if _, err := sysdep.RemoveIfPresent(m.fs, layout.SessionOverlay()); err != nil {
			return fmt.Errorf("proxy: purge session overlay: %w", err)
		}
		// Keep the lock file (it is the flock target) but clear the proxy state so
		// the next Attach cold-starts.
		return writeLock(lf, lockState{PolicyHash: cur.PolicyHash})
	}

	// Others remain: drop our PID, leave the proxy untouched.
	return writeLock(lf, lockState{ProxyPID: cur.ProxyPID, Port: cur.Port, PolicyHash: cur.PolicyHash, Agents: agents})
}

// pruneDead returns the subset of pids that are still alive, preserving order.
func (m *Manager) pruneDead(pids []int) []int {
	var alive []int
	for _, pid := range pids {
		if m.proc.Alive(pid) {
			alive = append(alive, pid)
		}
	}
	return alive
}

// choosePort decides the port for a (re)started proxy. recorded is the port from
// the lock (0 on a cold start). On a crash restart (recorded != 0) it best-effort
// reclaims that exact port so attached agents recover; if it cannot, it allocates
// a fresh one and reports changed=true.
func (m *Manager) choosePort(recorded int) (port int, changed bool, err error) {
	if recorded != 0 {
		ok, err := m.ports.TryReclaim(recorded)
		if err != nil {
			return 0, false, fmt.Errorf("proxy: reclaim port %d: %w", recorded, err)
		}
		if ok {
			return recorded, false, nil
		}
	}
	allocated, err := m.ports.Allocate()
	if err != nil {
		return 0, false, fmt.Errorf("proxy: allocate port: %w", err)
	}
	// changed only matters when there was a recorded port to lose.
	return allocated, recorded != 0, nil
}

// warnPortChanged emits the documented warn-never-kill message naming the agents
// that will see egress failures (their profile only allows the old port).
func (m *Manager) warnPortChanged(newPort, oldPort int, agents []int) {
	if m.warn == nil {
		return
	}
	fmt.Fprintf(m.warn,
		"⚠ proxy restarted on port %d (was %d); attached agents %s will see egress failures and should be relaunched\n",
		newPort, oldPort, formatPIDs(agents))
}

// mitmArgs builds the mitmdump command for the enforcer addon. The option names
// (creance_policy, creance_audit_log) are the ones enforcer.py declares.
func mitmArgs(port int, cfg StartConfig) []string {
	return []string{
		"--listen-host", "127.0.0.1",
		"--listen-port", strconv.Itoa(port),
		"-s", cfg.EnforcerPy,
		"--set", "creance_policy=" + cfg.Layout.PolicyJSON(),
		"--set", "creance_audit_log=" + cfg.Layout.EgressJSONL(),
		"-q",
	}
}

// readLock reads and unmarshals the lock contents from the held descriptor. An
// empty file is the zero lockState (first run); a corrupt file is treated as
// empty so the lock self-heals rather than wedging the project.
func readLock(lf sysdep.LockedFile) (lockState, error) {
	data, err := lf.ReadAll()
	if err != nil {
		return lockState{}, fmt.Errorf("proxy: read lock: %w", err)
	}
	if len(data) == 0 {
		return lockState{}, nil
	}
	var ls lockState
	if err := json.Unmarshal(data, &ls); err != nil {
		return lockState{}, nil // corrupt → treat as absent, cold-start
	}
	return ls, nil
}

// writeLock marshals and writes the lock contents in place on the held descriptor.
func writeLock(lf sysdep.LockedFile, ls lockState) error {
	data, err := json.MarshalIndent(ls, "", "  ")
	if err != nil {
		return fmt.Errorf("proxy: marshal lock: %w", err)
	}
	data = append(data, '\n')
	if err := lf.Write(data); err != nil {
		return fmt.Errorf("proxy: write lock: %w", err)
	}
	return nil
}

// addPID appends pid unless already present, preserving order.
func addPID(pids []int, pid int) []int {
	if containsPID(pids, pid) {
		return pids
	}
	return append(pids, pid)
}

// removePID returns pids without pid, preserving order.
func removePID(pids []int, pid int) []int {
	var out []int
	for _, p := range pids {
		if p != pid {
			out = append(out, p)
		}
	}
	return out
}

func containsPID(pids []int, pid int) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}

// formatPIDs renders a PID slice as a comma-separated list for the warning.
func formatPIDs(pids []int) string {
	out := ""
	for i, p := range pids {
		if i > 0 {
			out += ", "
		}
		out += strconv.Itoa(p)
	}
	return out
}
