package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

type cleanFixture struct {
	app   *App
	fs    *sysdeptest.FakeFileSystem
	flock *sysdeptest.FakeFlock
	proc  *sysdeptest.FakeProcessManager
	ports *sysdeptest.FakePortAllocator
	out   *bytes.Buffer
	lay   state.Layout
}

func newCleanFixture(t *testing.T) *cleanFixture {
	t.Helper()
	fs := sysdeptest.NewFakeFileSystem()
	flock := sysdeptest.NewFakeFlock()
	proc := sysdeptest.NewFakeProcessManager()
	ports := sysdeptest.NewFakePortAllocator()

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = runHome
	paths.Cwd = runProj // so "." resolves to the project dir

	lay, err := state.New(paths).Resolve(runProj)
	if err != nil {
		t.Fatalf("resolve layout: %v", err)
	}

	out := &bytes.Buffer{}
	app := &App{
		Stdout:         out,
		Stderr:         &bytes.Buffer{},
		FS:             fs,
		Paths:          paths,
		Flock:          flock,
		ProcessManager: proc,
		PortAllocator:  ports,
	}
	return &cleanFixture{app: app, fs: fs, flock: flock, proc: proc, ports: ports, out: out, lay: lay}
}

func (f *cleanFixture) seedLock(t *testing.T, ls doctorLockJSON) {
	t.Helper()
	data, err := json.Marshal(ls)
	if err != nil {
		t.Fatal(err)
	}
	f.flock.Contents[f.lay.ProxyLock()] = data
}

func TestCleanStopsProxyAndIsIdempotent(t *testing.T) {
	f := newCleanFixture(t)
	// Running proxy with a dead agent (so clean does not refuse).
	f.seedLock(t, doctorLockJSON{ProxyPID: 111, Port: 8080, Agents: []int{999}, CanonicalPath: runProj})
	f.proc.AlivePIDs[111] = true
	f.ports.Listening[8080] = true
	f.fs.Files[f.lay.SessionOverlay()] = []byte("once: rules")

	if err := runClean(f.app, false); err != nil {
		t.Fatalf("runClean: %v\n%s", err, f.out)
	}
	if got := f.out.String(); !strings.Contains(got, "stopped proxy (pid 111)") {
		t.Errorf("clean should report the stopped proxy, got %q", got)
	}
	if _, ok := f.fs.Files[f.lay.SessionOverlay()]; ok {
		t.Error("session overlay should be purged")
	}

	// Second run: nothing left to clean.
	f.out.Reset()
	if err := runClean(f.app, false); err != nil {
		t.Fatalf("second runClean: %v", err)
	}
	if got := f.out.String(); !strings.Contains(got, "nothing to clean") {
		t.Errorf("idempotent second clean should be a no-op, got %q", got)
	}
}

func TestCleanNoLockIsNoOp(t *testing.T) {
	f := newCleanFixture(t)
	if err := runClean(f.app, false); err != nil {
		t.Fatalf("runClean with no lock: %v", err)
	}
	if got := f.out.String(); !strings.Contains(got, "nothing to clean") {
		t.Errorf("clean with no lock should be a no-op, got %q", got)
	}
}

func TestCleanRefusesWithLiveAgents(t *testing.T) {
	f := newCleanFixture(t)
	f.seedLock(t, doctorLockJSON{ProxyPID: 111, Port: 8080, Agents: []int{222, 333}, CanonicalPath: runProj})
	f.proc.AlivePIDs[111] = true
	f.proc.AlivePIDs[222] = true
	f.proc.AlivePIDs[333] = true
	f.ports.Listening[8080] = true

	err := runClean(f.app, false)
	if err == nil {
		t.Fatalf("clean should refuse (non-zero) with live agents; stdout: %s", f.out)
	}
	out := f.out.String()
	if !strings.Contains(out, "222, 333") || !strings.Contains(out, "--force") {
		t.Errorf("refusal should name the live PIDs and mention --force, got %q", out)
	}
	if len(f.proc.Signaled) != 0 {
		t.Errorf("a refused clean must not signal the proxy: %+v", f.proc.Signaled)
	}
}

func TestCleanForceOverridesLiveAgents(t *testing.T) {
	f := newCleanFixture(t)
	f.seedLock(t, doctorLockJSON{ProxyPID: 111, Port: 8080, Agents: []int{222}, CanonicalPath: runProj})
	f.proc.AlivePIDs[111] = true
	f.proc.AlivePIDs[222] = true
	f.ports.Listening[8080] = true

	if err := runClean(f.app, true); err != nil {
		t.Fatalf("runClean --force: %v\n%s", err, f.out)
	}
	if got := f.out.String(); !strings.Contains(got, "stopped proxy (pid 111)") {
		t.Errorf("--force clean should tear down the proxy, got %q", got)
	}
}
