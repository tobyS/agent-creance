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

// brokerCmd is the hidden subcommand this binary re-executes itself as to run the
// credential broker daemon (AC-0069b). Re-exec rather than a separate binary: the
// broker must ship and version with the CLI that speaks its protocol.
const brokerCmd = "broker"

// stateDirPerm is the mode the project state dir is held at once it hosts the
// broker socket. The socket itself is 0600 — that is the access control — but a
// 0700 directory means a same-uid process cannot even enumerate it.
const stateDirPerm = 0o700

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
	// BrokerPID is the credential-broker daemon (AC-0069b); 0 when the project
	// injects no credentials, or when the broker could not be started (in which
	// case the proxy still runs and the enforcer answers 472 for inject-hosts).
	// It shares the proxy's lifetime exactly: spawned on the same branch, killed
	// by the same last-out Detach. A lock written before this field existed
	// unmarshals with it zero — read as "no broker", so the next Attach starts one.
	BrokerPID int `json:"broker_pid"`
	// Port is the proxy's ephemeral listen port; 0 when none has been allocated.
	Port int `json:"port"`
	// PolicyHash is the compiled-policy hash the running proxy was started with.
	PolicyHash string `json:"policy_hash"`
	// Agents are the attached agent-creance invocations, each recorded as a PID plus
	// its process start time (the second identity factor, AC-0061): a recycled PID
	// alone can masquerade as a live attached agent and pin the proxy, so pruneDead
	// also checks the live process's start time against the recorded one. An old lock
	// whose agents were bare PIDs (a previous binary) fails to unmarshal into this
	// shape and is treated as a cold start by readLock (accepted; no migration).
	Agents []agentRef `json:"agents"`
	// CanonicalPath is the project directory this lock belongs to, recorded so
	// `status` (AC-0032) can show a readable path: the state-dir hash is one-way,
	// so the project directory cannot be recovered from it. Older locks written
	// before this field existed unmarshal with it empty; status falls back to the
	// hash.
	CanonicalPath string `json:"canonical_path"`
}

// agentRef identifies one attached agent in the lock: its PID and the PID's process
// start time (unix micros, from ProcessManager.StartTime). The start time is the
// second identity factor — when the PID is recycled into a different process the
// start time differs, so the stale entry is pruned instead of pinning the proxy.
type agentRef struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"start"`
}

// Manager owns the flock-guarded proxy lifecycle for projects. Construct it with
// NewManager; one instance serves any number of projects (the project is named by
// the StartConfig.Layout passed to each call).
type Manager struct {
	fs    sysdep.FileSystem
	lock  sysdep.Flock
	proc  sysdep.ProcessManager
	ports sysdep.PortAllocator
	sock  sysdep.UnixSocket
	sleep sysdep.Sleeper
	warn  io.Writer
}

// NewManager wires a Manager from the OS seams. warn receives the warn-never-kill
// message (the caller passes app.Stderr); sleep bounds the post-spawn readiness poll;
// sock probes the broker's socket the way ports probes the proxy's port; the others
// are the injected seams.
func NewManager(fs sysdep.FileSystem, lock sysdep.Flock, proc sysdep.ProcessManager, ports sysdep.PortAllocator, sock sysdep.UnixSocket, sleep sysdep.Sleeper, warn io.Writer) *Manager {
	return &Manager{fs: fs, lock: lock, proc: proc, ports: ports, sock: sock, sleep: sleep, warn: warn}
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
	// SelfExe is the agent-creance binary (os.Executable()), re-executed as the
	// broker daemon (`agent-creance broker --socket …`). Empty disables the broker,
	// and therefore injection.
	SelfExe string
	// Secrets, when non-nil, resolves the credential-injection payload host-side —
	// the JSON {credential-name: raw-token} handed to the *broker* over fd
	// sysdep.SecretFD (AC-0069b; before that it went to the addon directly). It is
	// invoked ONLY when this Attach actually spawns the proxy (never on reuse),
	// because resolving an op:// reference can prompt for Touch ID and must not
	// re-prompt every time an agent attaches to an already-running proxy. A
	// best-effort resolver: an individual unresolvable credential is omitted (the
	// addon then answers 472 for requests needing it), not a hard error.
	Secrets func(ctx context.Context) ([]byte, error)
}

// Attachment is what Attach reports back to its caller.
type Attachment struct {
	// Port is the live proxy port the caller must point the cage at.
	Port int
	// ProxyPID is the live mitmproxy process.
	ProxyPID int
	// BrokerPID is the live credential-broker daemon, or 0 when the project injects
	// nothing (or the broker could not be started — injection then 472s, but the
	// cage runs).
	BrokerPID int
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
		att.BrokerPID = cur.BrokerPID
	} else {
		port, changed, err := m.choosePort(cur.Port)
		if err != nil {
			return Attachment{}, err
		}
		if changed && len(alive) > 0 {
			// Warn-never-kill: a restart on a new port strands agents whose frozen
			// Seatbelt profile only allows the old port. Warn, do NOT signal them.
			m.warnPortChanged(port, cur.Port, pids(alive))
		}
		// Resolve the injection payload only now, on the spawn path — never on the
		// reuse branch above — so op:// resolution can't re-prompt for Touch ID each
		// time an agent attaches to a running proxy. A resolver error is non-fatal:
		// spawn without a broker, and the addon fails closed (472) for inject-hosts.
		var secret []byte
		if cfg.Secrets != nil {
			s, serr := cfg.Secrets(ctx)
			if serr != nil {
				fmt.Fprintf(m.warn, "warning: resolving injection credentials: %v\n", serr)
			} else {
				secret = s
			}
		}
		// The broker custodies the secrets and serves them to the addon over its
		// socket; it must be listening before the proxy starts taking requests.
		brokerPID, sock := m.startBroker(ctx, cfg, secret)

		pid, err := m.proc.Spawn(ctx, proxyBin, mitmArgs(port, cfg, sock)...)
		if err != nil {
			m.stopBroker(brokerPID, cfg.Layout)
			return Attachment{}, fmt.Errorf("proxy: start mitmproxy: %w (try `agent-creance doctor`)", err)
		}
		if err := m.waitProxyReady(ctx, pid, port); err != nil {
			// Don't leave a half-started proxy (or its broker) orphaned: the lock has
			// not been written yet, so neither PID is recorded. Best-effort SIGTERM by
			// PID (a process that already exited is a no-op).
			_ = m.proc.Signal(pid, syscall.SIGTERM)
			m.stopBroker(brokerPID, cfg.Layout)
			return Attachment{}, err
		}
		att.Port = port
		att.ProxyPID = pid
		att.BrokerPID = brokerPID
		att.PortChanged = changed
	}

	// Record ourselves with our start time read from the same oracle pruneDead uses,
	// so a later prune compares like with like. A process can always read its own
	// kinfo_proc; a failure here means a broken environment, so fail the attach
	// rather than store a bogus identity that would prune us on the next run.
	selfStart, err := m.proc.StartTime(cfg.SelfPID)
	if err != nil {
		return Attachment{}, fmt.Errorf("proxy: read own start time (pid %d): %w", cfg.SelfPID, err)
	}
	agents := addRef(alive, agentRef{PID: cfg.SelfPID, StartTime: selfStart})
	next := lockState{ProxyPID: att.ProxyPID, BrokerPID: att.BrokerPID, Port: att.Port, PolicyHash: cfg.PolicyHash, Agents: agents, CanonicalPath: cfg.Layout.Canonical}
	if err := writeLock(lf, next); err != nil {
		return Attachment{}, err
	}
	return att, nil
}

// startBroker spawns the credential broker for a project that injects something,
// handing it the resolved secrets over the inherited descriptor, and waits until
// it is listening. It returns the broker's PID and the socket path the addon must
// be told about — both zero/empty when no broker runs.
//
// Every failure here is non-fatal by design, and the reason is the same one that
// makes injection failures a per-request 472 rather than a failed spawn: a cage
// that will not start is worse than a cage whose injected hosts refuse. So a
// missing binary, an over-long socket path (sun_path is 104 bytes — a long $HOME
// or XDG_CACHE_HOME really can overflow it), or a broker that never comes up all
// warn and return 0. The proxy then starts without a broker socket, and the addon
// answers 472 for exactly the hosts that needed a credential.
func (m *Manager) startBroker(ctx context.Context, cfg StartConfig, secret []byte) (pid int, sock string) {
	if len(secret) == 0 || cfg.SelfExe == "" {
		return 0, "" // nothing to inject
	}

	sock = cfg.Layout.BrokerSock()
	if len(sock) > sysdep.MaxSocketPathLen {
		fmt.Fprintf(m.warn, "warning: credential broker socket path is too long (%d bytes, limit %d): %s\n"+
			"  injected hosts will answer 472; set XDG_CACHE_HOME to a shorter path\n",
			len(sock), sysdep.MaxSocketPathLen, sock)
		return 0, ""
	}

	// The socket's mode is the access control, but tighten the directory too: it may
	// predate this binary (MkdirAll only applies perm to dirs it creates).
	if err := m.fs.Chmod(cfg.Layout.Root, stateDirPerm); err != nil {
		fmt.Fprintf(m.warn, "warning: tighten state dir %q: %v\n", cfg.Layout.Root, err)
	}

	pid, err := m.proc.SpawnWithSecret(ctx, secret, cfg.SelfExe, brokerArgs(sock)...)
	if err != nil {
		fmt.Fprintf(m.warn, "warning: start credential broker: %v\n  injected hosts will answer 472\n", err)
		return 0, ""
	}
	if err := m.waitBrokerReady(ctx, pid, sock); err != nil {
		fmt.Fprintf(m.warn, "warning: %v\n  injected hosts will answer 472\n", err)
		m.stopBroker(pid, cfg.Layout)
		return 0, ""
	}
	return pid, sock
}

// waitBrokerReady blocks until the freshly-spawned broker is accepting connections
// on its socket, the process has exited, or the bounded poll elapses — the same
// shape (and the same bounds) as waitProxyReady, for the same reason: a PID from
// Spawn is not evidence that the daemon ever came up.
func (m *Manager) waitBrokerReady(ctx context.Context, pid int, sock string) error {
	for attempt := 0; attempt < readyMaxAttempts; attempt++ {
		if m.sock.Probe(sock) {
			return nil
		}
		if !m.proc.Alive(pid) {
			return fmt.Errorf("proxy: credential broker (pid %d) exited during startup", pid)
		}
		if err := m.sleep.Sleep(ctx, readyPollInterval); err != nil {
			return fmt.Errorf("proxy: wait for credential broker on %s: %w", sock, err)
		}
	}
	return fmt.Errorf("proxy: credential broker did not start listening on %s within %s",
		sock, time.Duration(readyMaxAttempts)*readyPollInterval)
}

// stopBroker SIGTERMs the broker (which wipes its secrets on the way out) and
// removes the socket file. Both steps are best-effort: a broker that already exited
// is a no-op, and a leftover socket file would only be cleaned by the next Listen.
func (m *Manager) stopBroker(pid int, layout state.Layout) {
	if pid != 0 {
		_ = m.proc.Signal(pid, syscall.SIGTERM)
	}
	_, _ = sysdep.RemoveIfPresent(m.fs, layout.BrokerSock())
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
	return fmt.Errorf("proxy: mitmproxy did not start listening on port %d within %s (try `agent-creance doctor`)", port, time.Duration(readyMaxAttempts)*readyPollInterval)
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
	agents := removeRef(cur.Agents, selfPID)

	if len(agents) == 0 {
		// Last agent out: tear the proxy down and purge the session overlay.
		if cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) {
			if err := m.proc.Signal(cur.ProxyPID, syscall.SIGTERM); err != nil {
				return fmt.Errorf("proxy: stop mitmproxy: %w", err)
			}
		}
		// The broker dies with the proxy it served — on SIGTERM it wipes the tokens
		// it custodied, which is the point of holding them in Go at all.
		m.stopBroker(cur.BrokerPID, layout)
		if _, err := sysdep.RemoveIfPresent(m.fs, layout.SessionOverlay()); err != nil {
			return fmt.Errorf("proxy: purge session overlay: %w", err)
		}
		// Keep the lock file (it is the flock target) but clear the proxy state so
		// the next Attach cold-starts.
		return writeLock(lf, lockState{PolicyHash: cur.PolicyHash})
	}

	// Others remain: drop our PID, leave the proxy (and its broker) untouched.
	return writeLock(lf, lockState{ProxyPID: cur.ProxyPID, BrokerPID: cur.BrokerPID, Port: cur.Port, PolicyHash: cur.PolicyHash, Agents: agents, CanonicalPath: cur.CanonicalPath})
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
	// BrokerPID is the recorded credential broker (0 when the project injects
	// nothing, or the broker failed to start).
	BrokerPID int
	// BrokerUp is the broker's "genuinely alive" composite, mirroring ProxyUp: PID
	// liveness AND a socket probe.
	BrokerUp bool
	// BrokerDown is true when the proxy is up but its recorded broker is not. The
	// cage still runs and non-injected hosts still work, but every request needing
	// an injected credential answers 472 — a degraded state worth surfacing, since
	// a 472 alone cannot tell the user a dead broker from a locked secret store.
	BrokerDown bool
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
	brokerUp := cur.BrokerPID != 0 && m.proc.Alive(cur.BrokerPID) && m.sock.Probe(layout.BrokerSock())
	return Diagnosis{
		LockPresent:   true,
		ProxyPID:      cur.ProxyPID,
		Port:          cur.Port,
		BrokerPID:     cur.BrokerPID,
		CanonicalPath: cur.CanonicalPath,
		ProxyUp:       up,
		BrokerUp:      brokerUp,
		// Only a project that *has* a broker can have a dead one: BrokerPID is 0
		// when nothing is injected, which is not a degradation.
		BrokerDown: up && cur.BrokerPID != 0 && !brokerUp,
		LiveAgents: pids(live),
		Orphan:     up && len(live) == 0,
		Stranded:   !up && len(live) > 0,
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
	m.stopBroker(cur.BrokerPID, layout)
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
		return CleanResult{Refused: true, LiveAgents: pids(live)}, nil
	}

	var res CleanResult
	if cur.ProxyPID != 0 && m.proc.Alive(cur.ProxyPID) {
		if err := m.proc.Signal(cur.ProxyPID, syscall.SIGTERM); err != nil {
			return CleanResult{}, fmt.Errorf("proxy: stop mitmproxy: %w", err)
		}
		res.Cleaned = true
		res.ProxyPID = cur.ProxyPID
	}
	m.stopBroker(cur.BrokerPID, layout)
	if _, err := sysdep.RemoveIfPresent(m.fs, layout.SessionOverlay()); err != nil {
		return CleanResult{}, fmt.Errorf("proxy: purge session overlay: %w", err)
	}
	if err := writeLock(lf, lockState{}); err != nil {
		return CleanResult{}, err
	}
	return res, nil
}

// pruneDead returns the subset of refs that are still the original attached agent,
// preserving order. An entry survives only when its PID is alive AND the live
// process's start time matches the one recorded at attach: a recycled PID (alive,
// but a different process now) or an unreadable start time (the process is gone)
// is treated as dead, so it can no longer pin the proxy or block clean (AC-0061).
func (m *Manager) pruneDead(refs []agentRef) []agentRef {
	var alive []agentRef
	for _, ref := range refs {
		if !m.proc.Alive(ref.PID) {
			continue
		}
		st, err := m.proc.StartTime(ref.PID)
		if err != nil || st != ref.StartTime {
			continue
		}
		alive = append(alive, ref)
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
// (creance_policy, creance_audit_log, creance_broker_sock) are the ones enforcer.py
// declares. A non-empty sock adds creance_broker_sock, telling the addon where to
// fetch injected credentials; it is set only when a broker was actually started.
//
// The socket path is not a secret — the socket's mode and its unreachability from
// the cage are the access control (AC-0069b) — so unlike the raw token it once
// carried, it is safe on argv, where `ps` can see it.
func mitmArgs(port int, cfg StartConfig, sock string) []string {
	args := []string{
		"--listen-host", "127.0.0.1",
		"--listen-port", strconv.Itoa(port),
		"-s", cfg.EnforcerPy,
		"--set", "creance_policy=" + cfg.Layout.PolicyJSON(),
		"--set", "creance_audit_log=" + cfg.Layout.EgressJSONL(),
	}
	if sock != "" {
		args = append(args, "--set", "creance_broker_sock="+sock)
	}
	return append(args, "-q")
}

// brokerArgs builds the argv for re-executing this binary as the broker daemon.
// The secrets reach it over fd sysdep.SecretFD, never here.
func brokerArgs(sock string) []string {
	return []string{brokerCmd, "--socket", sock}
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

// addRef appends ref unless an entry with the same PID is already present,
// preserving order (idempotent on PID, like the agents array it maintains).
func addRef(refs []agentRef, ref agentRef) []agentRef {
	for _, r := range refs {
		if r.PID == ref.PID {
			return refs
		}
	}
	return append(refs, ref)
}

// removeRef returns refs without any entry for pid, preserving order.
func removeRef(refs []agentRef, pid int) []agentRef {
	var out []agentRef
	for _, r := range refs {
		if r.PID != pid {
			out = append(out, r)
		}
	}
	return out
}

// pids projects an agentRef slice to its PIDs, for display (Diagnosis/CleanResult)
// and the warn-never-kill message — the lock's identity factor is internal.
func pids(refs []agentRef) []int {
	out := make([]int, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.PID)
	}
	return out
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
