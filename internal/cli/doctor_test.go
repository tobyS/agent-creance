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
	ProxyPID   int    `json:"proxy_pid"`
	Port       int    `json:"port"`
	PolicyHash string `json:"policy_hash"`
	Agents     []int  `json:"agents"`
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

	if err := runDoctor(context.Background(), f.app, false); err != nil {
		t.Fatalf("runDoctor: %v\nstdout: %s", err, f.out)
	}
	out := f.out.String()
	for _, want := range []string{"Version compatibility:", "CA trust:", "✓ trusted", "Proxy (this project):", "no proxy state"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\n%s", want, out)
		}
	}
}

func TestDoctorUntrustedCAExitsNonZero(t *testing.T) {
	f := newDoctorFixture(t)
	f.prober.Outcome = sysdep.ProbeUntrusted

	err := runDoctor(context.Background(), f.app, false)
	if err == nil {
		t.Fatalf("want non-zero exit for untrusted CA, got nil\nstdout: %s", f.out)
	}
	if !strings.Contains(err.Error(), "untrusted CA") {
		t.Errorf("error = %q, want it to mention untrusted CA", err)
	}
}

func TestDoctorOrphanActionableThenFixed(t *testing.T) {
	f := newDoctorFixture(t)
	f.seedLock(t, doctorLockJSON{ProxyPID: 111, Port: 8080, PolicyHash: "h", Agents: []int{999}})
	f.proc.AlivePIDs[111] = true   // proxy alive
	f.ports.Listening[8080] = true // listening; 999 dead ⇒ orphan

	// Without --fix: orphan makes doctor exit non-zero.
	if err := runDoctor(context.Background(), f.app, false); err == nil {
		t.Fatalf("want non-zero exit for un-fixed orphan, got nil\nstdout: %s", f.out)
	} else if !strings.Contains(err.Error(), "orphan proxy") {
		t.Errorf("error = %q, want it to mention orphan proxy", err)
	}

	// With --fix: orphan cleaned, exit zero.
	f.out.Reset()
	if err := runDoctor(context.Background(), f.app, true); err != nil {
		t.Fatalf("doctor --fix should clean the orphan and exit 0, got %v\nstdout: %s", err, f.out)
	}
	if !strings.Contains(f.out.String(), "cleaned orphan proxy (pid 111)") {
		t.Errorf("stdout should report the cleaned orphan\n%s", f.out)
	}
}

func TestDoctorExposedServiceIsWarningExitsZero(t *testing.T) {
	f := newDoctorFixture(t)
	f.listen.List = []sysdep.Listener{{Command: "node", PID: 501, Address: "*:8080"}}

	if err := runDoctor(context.Background(), f.app, false); err != nil {
		t.Fatalf("exposed service is a warning, doctor must exit 0, got %v", err)
	}
	if !strings.Contains(f.out.String(), "node (pid 501) listening on *:8080") {
		t.Errorf("stdout should list the exposed service\n%s", f.out)
	}
}
