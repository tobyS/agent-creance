package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"syscall"
	"time"

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

// readiness poll bounds for a freshly-spawned proxy: Attach waits up to
// readyMaxAttempts * readyPollInterval for the proxy to start listening, so a proxy
// that exits during startup (e.g. the enforcer refusing a corrupt initial policy.json)
// is surfaced as a hard error rather than reported "ready" the moment Spawn returns a
// PID (AC-0058 / B2).
const (
	readyMaxAttempts  = 100
	readyPollInterval = 50 * time.Millisecond
)

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
	// CanonicalPath is the project directory this lock belongs to, recorded so
	// `status` (AC-0032) can show a readable path: the state-dir hash is one-way,
	// so the project directory cannot be recovered from it. Older locks written
	// before this field existed unmarshal with it empty; status falls back to the
	// hash.
	CanonicalPath string `json:"canonical_path"`
}

// Manager owns the flock-guarded proxy lifecycle for projects. Construct it with
// NewManager; one instance serves any number of projects (the project is named by
// the StartConfig.Layout passed to each call).
type Manager struct {
	fs    sysdep.FileSystem
	lock  sysdep.Flock
	proc  sysdep.ProcessManager
	ports sysdep.PortAllocator
	sleep sysdep.Sleeper
	warn  io.Writer
}

// NewManager wires a Manager from the OS seams. warn receives the warn-never-kill
// message (the caller passes app.Stderr); sleep bounds the post-spawn readiness poll;
// the others are the injected seams.
func NewManager(fs sysdep.FileSystem, lock sysdep.Flock, proc sysdep.ProcessManager, ports sysdep.PortAllocator, sleep sysdep.Sleeper, warn io.Writer) *Manager {
	return &Manager{fs: fs, lock: lock, proc: proc, ports: ports, sleep: sleep, warn: warn}
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
		if err := m.waitProxyReady(ctx, pid, port); err != nil {
			// Don't leave a half-started proxy orphaned: the lock has not been written
			// yet, so this PID is not yet recorded. Best-effort SIGTERM by PID (a proxy
			// that already exited is a no-op).
			_ = m.proc.Signal(pid, syscall.SIGTERM)
			return Attachment{}, err
		}
		att.Port = port
		att.ProxyPID = pid
		att.PortChanged = changed
	}

	agents := addPID(alive, cfg.SelfPID)
	next := lockState{ProxyPID: att.ProxyPID, Port: att.Port, PolicyHash: cfg.PolicyHash, Agents: agents, CanonicalPath: cfg.Layout.Canonical}
	if err := writeLock(lf, next); err != nil {
		return Attachment{}, err
	}
	return att, nil
}

// waitProxyReady blocks until the freshly-spawned proxy is accepting connections
// (ready), the process has exited (a hard startup failure — e.g. the enforcer refused
// to run on a missing or corrupt initial policy.json and exited non-zero), or the
// bounded poll elapses. Returning an error turns what used to be a silent fail-open
// start — the proxy reported "ready" the instant Spawn returned a PID, even if it never
// came up on an empty ruleset — into a visible failure to the launcher (AC-0058 / B2).
func (m *Manager) waitProxyReady(ctx context.Context, pid, port int) error {
	for attempt := 0; attempt < readyMaxAttempts; attempt++ {
		if m.ports.Probe(port) {
			return nil
		}
		if !m.proc.Alive(pid) {
			return fmt.Errorf("proxy: mitmproxy (pid %d) exited during startup; check the compiled policy and CA setup (try `agent-creance doctor`)", pid)
		}
		if err := m.sleep.Sleep(ctx, readyPollInterval); err != nil {
			return fmt.Errorf("proxy: wait for mitmproxy to listen on port %d: %w", port, err)
		}
	}
	return fmt.Errorf("proxy: mitmproxy did not start listening on port %d within %s", port, time.Duration(readyMaxAttempts)*readyPollInterval)
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
	return writeLock(lf, lockState{ProxyPID: cur.ProxyPID, Port: cur.Port, PolicyHash: cur.PolicyHash, Agents: agents, CanonicalPath: cur.CanonicalPath})
}

// Diagnosis is doctor's read-only view of a project's proxy lifecycle (AC-0031),
// computed from proxy.lock plus live PID/port probes. It mutates nothing.
type Diagnosis struct {
	// LockPresent is false when there is no recorded proxy state (missing, empty, or
	// corrupt lock) — nothing to diagnose.
	LockPresent bool
	// ProxyPID and Port are the recorded proxy identity (may be stale).
	ProxyPID int
	Port     int
	// CanonicalPath is the project directory recorded in the lock (empty for locks
	// written before AC-0032). `status` shows it, falling back to the state-dir hash.
	CanonicalPath string
	// ProxyUp is the lifecycle "is the proxy genuinely alive" composite: PID liveness
	// AND a port probe (a bare PID may have been recycled). Mirrors Attach.
	ProxyUp bool
	// LiveAgents are the attached agent PIDs still alive (dead ones pruned).
	LiveAgents []int
	// Orphan is true when the proxy is up but no attached agent is alive — a stranded
	// listening mitmproxy that doctor --fix can clean.
	Orphan bool
	// Stranded is true when live agents are attached but the proxy is not reachable on
	// the recorded port. This is the persistent, detectable manifestation of the
	// "port changed under attached agents" condition (AC-0020 surfaces it only
	// transiently and persists no old port): the agents' next request hits a dead
	// port and gets connection-refused (design.md "Multi-agent lifecycle"). doctor
	// warns and never kills them (warn-never-kill).
	Stranded bool
}

// Inspect reads the project's proxy.lock under the flock and reports its health
// without mutating anything. A missing/empty/corrupt lock yields
// Diagnosis{LockPresent: false}. It uses the same liveness checks as Attach so the
// orphan/stranded verdicts match the lifecycle's own notion of "up".
func (m *Manager) Inspect(layout state.Layout) (Diagnosis, error) {
	lf, err := m.lock.Acquire(layout.ProxyLock())
	if err != nil {
		return Diagnosis{}, fmt.Errorf("proxy: acquire lock: %w", err)
	}
	defer func() { _ = lf.Release() }()

	cur, err := readLock(lf)
	if err != nil {
		return Diagnosis{}, err
	}
	if cur.ProxyPID == 0 && len(cur.Agents) == 0 {
		return Diagnosis{LockPresent: false}, nil
	}

	live := m.pruneDead(cur.Agents)
	up := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) && m.ports.Probe(cur.Port)
	return Diagnosis{
		LockPresent:   true,
		ProxyPID:      cur.ProxyPID,
		Port:          cur.Port,
		CanonicalPath: cur.CanonicalPath,
		ProxyUp:       up,
		LiveAgents:    live,
		Orphan:        up && len(live) == 0,
		Stranded:      !up && len(live) > 0,
	}, nil
}

// CleanResult reports what CleanOrphan / Clean changed.
type CleanResult struct {
	// Cleaned is true iff a live proxy was found and torn down.
	Cleaned bool
	// ProxyPID is the proxy that was signalled (0 when nothing was cleaned).
	ProxyPID int
	// Refused is true when Clean declined because live agents are attached and
	// force was not set (warn-never-kill). LiveAgents names them. Nothing was
	// mutated. CleanOrphan never sets this (it is always a safe no-op instead).
	Refused bool
	// LiveAgents are the still-attached agent PIDs when Refused is true.
	LiveAgents []int
}

// CleanOrphan is the doctor --fix primitive (AC-0031). It re-checks under the flock
// and, ONLY if the project's proxy is a true orphan (up, zero live agents), tears it
// down exactly like last-out Detach: SIGTERM the proxy by PID, purge the session
// overlay, and clear the lock's proxy state (keeping the lock file as the flock
// target). It never touches a proxy with live attached agents (warn-never-kill), so
// a non-orphan is a safe no-op returning Cleaned=false.
func (m *Manager) CleanOrphan(layout state.Layout) (CleanResult, error) {
	lf, err := m.lock.Acquire(layout.ProxyLock())
	if err != nil {
		return CleanResult{}, fmt.Errorf("proxy: acquire lock: %w", err)
	}
	defer func() { _ = lf.Release() }()

	cur, err := readLock(lf)
	if err != nil {
		return CleanResult{}, err
	}
	live := m.pruneDead(cur.Agents)
	up := cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) && m.ports.Probe(cur.Port)
	if !up || len(live) > 0 {
		return CleanResult{}, nil // not an orphan — safe no-op
	}

	if err := m.proc.Signal(cur.ProxyPID, syscall.SIGTERM); err != nil {
		return CleanResult{}, fmt.Errorf("proxy: stop orphan mitmproxy: %w", err)
	}
	if _, err := sysdep.RemoveIfPresent(m.fs, layout.SessionOverlay()); err != nil {
		return CleanResult{}, fmt.Errorf("proxy: purge session overlay: %w", err)
	}
	if err := writeLock(lf, lockState{PolicyHash: cur.PolicyHash}); err != nil {
		return CleanResult{}, err
	}
	return CleanResult{Cleaned: true, ProxyPID: cur.ProxyPID}, nil
}

// Clean is the `agent-creance clean` primitive (AC-0032): an unconditional,
// idempotent teardown of this project's proxy. Under the flock it prunes dead
// agents and, unless force is set, REFUSES when live agents remain
// (warn-never-kill) — returning Refused with their PIDs and mutating nothing, so
// an operator does not strand running cages by accident. Otherwise it SIGTERMs the
// recorded proxy if alive, purges the session overlay, and clears the lock state
// (keeping the lock file as the flock target, like Detach/CleanOrphan). It is safe
// to run repeatedly and when nothing is running (Cleaned=false, no error).
func (m *Manager) Clean(layout state.Layout, force bool) (CleanResult, error) {
	// Ensure the state dir exists so acquiring the flock (which opens/creates the
	// lock file) does not fail when the project has never run — clean is a no-op in
	// that case, not an error.
	if err := m.fs.MkdirAll(layout.Root, dirPerm); err != nil {
		return CleanResult{}, fmt.Errorf("proxy: create state dir %q: %w", layout.Root, err)
	}
	lf, err := m.lock.Acquire(layout.ProxyLock())
	if err != nil {
		return CleanResult{}, fmt.Errorf("proxy: acquire lock: %w", err)
	}
	defer func() { _ = lf.Release() }()

	cur, err := readLock(lf)
	if err != nil {
		return CleanResult{}, err
	}
	live := m.pruneDead(cur.Agents)
	if len(live) > 0 && !force {
		return CleanResult{Refused: true, LiveAgents: live}, nil
	}

	var res CleanResult
	if cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) {
		if err := m.proc.Signal(cur.ProxyPID, syscall.SIGTERM); err != nil {
			return CleanResult{}, fmt.Errorf("proxy: stop mitmproxy: %w", err)
		}
		res.Cleaned = true
		res.ProxyPID = cur.ProxyPID
	}
	if _, err := sysdep.RemoveIfPresent(m.fs, layout.SessionOverlay()); err != nil {
		return CleanResult{}, fmt.Errorf("proxy: purge session overlay: %w", err)
	}
	if err := writeLock(lf, lockState{}); err != nil {
		return CleanResult{}, err
	}
	return res, nil
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
