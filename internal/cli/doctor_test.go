package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/buildinfo"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// doctorCAPath is where the Installer resolves the mitmproxy CA cert given runHome.
const doctorCAPath = runHome + "/.mitmproxy/mitmproxy-ca-cert.pem"

// doctorLockJSON mirrors proxy's unexported lockState wire format for seeding.
type doctorLockJSON struct {
	ProxyPID      int            `json:"proxy_pid"`
	Port          int            `json:"port"`
	PolicyHash    string         `json:"policy_hash"`
	Agents        []lockAgentRef `json:"agents"`
	CanonicalPath string         `json:"canonical_path"`
}

// lockAgentRef mirrors proxy's unexported agentRef (PID + start-time identity).
type lockAgentRef struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"start"`
}

// agentStart is the deterministic start time used when seeding an attached agent in
// a cli-level lock; tests that also mark the PID alive must set the same value in
// FakeProcessManager.StartTimes so pruneDead's identity check keeps the agent.
func agentStart(pid int) int64 { return int64(pid)*1000 + 1 }

// agentEntries builds lock agent records for pids, each with its deterministic start.
func agentEntries(pids ...int) []lockAgentRef {
	out := make([]lockAgentRef, 0, len(pids))
	for _, p := range pids {
		out = append(out, lockAgentRef{PID: p, StartTime: agentStart(p)})
	}
	return out
}

type doctorFixture struct {
	app    *App
	fs     *sysdeptest.FakeFileSystem
	proc   *sysdeptest.FakeProcessManager
	ports  *sysdeptest.FakePortAllocator
	prober *sysdeptest.FakeTLSProber
	flock  *sysdeptest.FakeFlock
	listen *sysdeptest.FakeListenerScanner
	out    *bytes.Buffer
	lay    state.Layout
}

// newDoctorFixture wires a healthy host: both prerequisites at the tested versions,
// a generated + trusted CA, no project proxy, no exposed listeners, local FS.
func newDoctorFixture(t *testing.T) *doctorFixture {
	t.Helper()

	fs := sysdeptest.NewFakeFileSystem()
	fs.Files[doctorCAPath] = []byte("-----BEGIN CERTIFICATE-----") // CA generated

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = runHome
	paths.Cwd = runProj

	resolver := state.New(paths)
	lay, err := resolver.Resolve(runProj)
	if err != nil {
		t.Fatalf("resolve layout: %v", err)
	}

	cmd := sysdeptest.NewFakeCommander().
		WithTool("agent-safehouse", "/usr/local/bin/agent-safehouse",
			"agent-safehouse "+buildinfo.TestedVersions["agent-safehouse"]).
		WithTool("mitmproxy", "/usr/local/bin/mitmproxy",
			"Mitmproxy: "+buildinfo.TestedVersions["mitmproxy"])

	proc := sysdeptest.NewFakeProcessManager()
	ports := sysdeptest.NewFakePortAllocator()
	prober := sysdeptest.NewFakeTLSProber() // ProbeTrusted by default
	flock := sysdeptest.NewFakeFlock()
	listen := &sysdeptest.FakeListenerScanner{}

	out := &bytes.Buffer{}
	app := &App{
		Commander:      cmd,
		Stdout:         out,
		Stderr:         &bytes.Buffer{},
		Tested:         buildinfo.TestedVersions,
		FS:             fs,
		Paths:          paths,
		Keychain:       sysdeptest.NewFakeKeychain(),
		Flock:          flock,
		ProcessManager: proc,
		PortAllocator:  ports,
		TLSProber:      prober,
		Sleeper:        &sysdeptest.FakeSleeper{},
		FSType:         sysdeptest.NewFakeFilesystemTyper(),
		Listeners:      listen,
	}
	return &doctorFixture{
		app: app, fs: fs, proc: proc, ports: ports, prober: prober,
		flock: flock, listen: listen, out: out, lay: lay,
	}
}

func (f *doctorFixture) seedLock(t *testing.T, ls doctorLockJSON) {
	t.Helper()
	data, err := json.Marshal(ls)
	if err != nil {
		t.Fatal(err)
	}
	f.flock.Contents[f.lay.ProxyLock()] = data
}

func TestDoctorHealthyExitsZero(t *testing.T) {
	f := newDoctorFixture(t)

	if err := runDoctor(context.Background(), f.app, false, false); err != nil {
		t.Fatalf("runDoctor: %v\nstdout: %s", err, f.out)
	}
	out := f.out.String()
	for _, want := range []string{"Version compatibility:", "CA trust:", "✓ trusted", "Proxy (this project):", "no proxy state"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\n%s", want, out)
		}
	}
}

func TestDoctorJSONHealthy(t *testing.T) {
	f := newDoctorFixture(t)

	if err := runDoctor(context.Background(), f.app, false, true /*json*/); err != nil {
		t.Fatalf("runDoctor --json: %v\nstdout: %s", err, f.out)
	}
	var rep struct {
		CA         struct{ State string } `json:"ca"`
		Proxy      struct{ State string } `json:"proxy"`
		Actionable []string               `json:"actionable"`
	}
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
		t.Fatalf("doctor --json is not valid JSON: %v\n%s", err, f.out)
	}
	if rep.CA.State != "ok" || rep.Proxy.State != "none" {
		t.Errorf("ca=%q proxy=%q, want ok/none", rep.CA.State, rep.Proxy.State)
	}
	if len(rep.Actionable) != 0 {
		t.Errorf("actionable = %v, want empty on a healthy host", rep.Actionable)
	}
}

// TestDoctorJSONPreservesExitCode pins S7: --json must still exit non-zero (and
// emit JSON) when an actionable problem remains.
func TestDoctorJSONPreservesExitCode(t *testing.T) {
	f := newDoctorFixture(t)
	f.prober.Outcome = sysdep.ProbeUntrusted // actionable: untrusted CA

	err := runDoctor(context.Background(), f.app, false, true /*json*/)
	if err == nil {
		t.Fatalf("want non-zero exit for untrusted CA under --json\nstdout: %s", f.out)
	}
	if !strings.Contains(err.Error(), "untrusted CA") {
		t.Errorf("error = %q, want it to mention untrusted CA", err)
	}
	// JSON was still emitted to stdout (machine-readable even on failure).
	var rep struct {
		CA         struct{ State string } `json:"ca"`
		Actionable []string               `json:"actionable"`
	}
	if jerr := json.Unmarshal(f.out.Bytes(), &rep); jerr != nil {
		t.Fatalf("doctor --json emitted no valid JSON on the failure path: %v\n%s", jerr, f.out)
	}
	if rep.CA.State != "problem" {
		t.Errorf("ca state = %q, want problem", rep.CA.State)
	}
}

func TestDoctorUntrustedCAExitsNonZero(t *testing.T) {
	f := newDoctorFixture(t)
	f.prober.Outcome = sysdep.ProbeUntrusted

	err := runDoctor(context.Background(), f.app, false, false)
	if err == nil {
		t.Fatalf("want non-zero exit for untrusted CA, got nil\nstdout: %s", f.out)
	}
	if !strings.Contains(err.Error(), "untrusted CA") {
		t.Errorf("error = %q, want it to mention untrusted CA", err)
	}
}

func TestDoctorOrphanActionableThenFixed(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedLock(t, doctorLockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: agentEntries(999)})
	f.proc.AlivePIDs[111] = true   // proxy alive
	f.ports.Listening[8080] = true // listening; 999 dead ⇒ orphan

	// Without --fix: orphan makes doctor exit non-zero.
	if err := runDoctor(context.Background(), f.app, false, false); err == nil {
		t.Fatalf("want non-zero exit for un-fixed orphan, got nil\nstdout: %s", f.out)
	} else if !strings.Contains(err.Error(), "orphan proxy") {
		t.Errorf("error = %q, want it to mention orphan proxy", err)
	}

	// With --fix: orphan cleaned, exit zero.
	f.out.Reset()
	if err := runDoctor(context.Background(), f.app, true, false); err != nil {
		t.Fatalf("doctor --fix should clean the orphan and exit 0, got %v\nstdout: %s", err, f.out)
	}
	if !strings.Contains(f.out.String(), "cleaned orphan proxy (pid 111)") {
		t.Errorf("stdout should report the cleaned orphan\n%s", f.out)
	}
}

func TestDoctorExposedServiceIsWarningExitsZero(t *testing.T) {
	f := newDoctorFixture(t)
	f.listen.List = []sysdep.Listener{{Command: "node", PID: 501, Address: "*:8080"}}

	if err := runDoctor(context.Background(), f.app, false, false); err != nil {
		t.Fatalf("exposed service is a warning, doctor must exit 0, got %v", err)
	}
	if !strings.Contains(f.out.String(), "node (pid 501) listening on *:8080") {
		t.Errorf("stdout should list the exposed service\n%s", f.out)
	}
}
