package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// statusFixture wires an App with the seams `status` needs and a resolved
// projects/ root to seed fixture project locks under.
type statusFixture struct {
	app      *App
	fs       *sysdeptest.FakeFileSystem
	flock    *sysdeptest.FakeFlock
	proc     *sysdeptest.FakeProcessManager
	ports    *sysdeptest.FakePortAllocator
	out      *bytes.Buffer
	projects string
}

func newStatusFixture(t *testing.T) *statusFixture {
	t.Helper()
	fs := sysdeptest.NewFakeFileSystem()
	flock := sysdeptest.NewFakeFlock()
	proc := sysdeptest.NewFakeProcessManager()
	ports := sysdeptest.NewFakePortAllocator()

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = runHome
	paths.Cwd = runProj

	projects, err := state.New(paths).ProjectsRoot()
	if err != nil {
		t.Fatalf("projects root: %v", err)
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
	return &statusFixture{app: app, fs: fs, flock: flock, proc: proc, ports: ports, out: out, projects: projects}
}

func (f *statusFixture) seed(t *testing.T, hash string, ls doctorLockJSON) {
	t.Helper()
	root := filepath.Join(f.projects, hash)
	if err := f.fs.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ls)
	if err != nil {
		t.Fatal(err)
	}
	f.flock.Contents[filepath.Join(root, "proxy.lock")] = data
}

func TestStatusNoCages(t *testing.T) {
	f := newStatusFixture(t)
	if err := runStatus(f.app, false); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	if got := f.out.String(); !strings.Contains(got, "No active cages.") {
		t.Errorf("status with no projects should say so, got %q", got)
	}
}

func TestStatusJSON(t *testing.T) {
	f := newStatusFixture(t)
	f.seed(t, "1111111111111111", doctorLockJSON{ProxyPID: 11, Port: 8080, Agents: agentEntries(12), CanonicalPath: "/code/alpha"})
	f.proc.AlivePIDs[11] = true
	f.proc.AlivePIDs[12] = true
	f.proc.StartTimes[12] = agentStart(12)
	f.ports.Listening[8080] = true

	if err := runStatus(f.app, true /*json*/); err != nil {
		t.Fatalf("runStatus --json: %v", err)
	}
	var rep struct {
		Projects []struct {
			Project string `json:"project"`
			State   string `json:"state"`
			Port    int    `json:"port"`
			Agents  int    `json:"agents"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(f.out.Bytes(), &rep); err != nil {
		t.Fatalf("status --json is not valid JSON: %v\n%s", err, f.out)
	}
	if len(rep.Projects) != 1 {
		t.Fatalf("projects = %d, want 1\n%s", len(rep.Projects), f.out)
	}
	p := rep.Projects[0]
	if p.Project != "/code/alpha" || p.State != "running" || p.Port != 8080 || p.Agents != 1 {
		t.Errorf("project = %+v, want /code/alpha running 8080 1 agent", p)
	}
}

// TestStatusJSONEmpty pins that no cages serializes as an empty array (not null),
// so scripts can iterate unconditionally.
func TestStatusJSONEmpty(t *testing.T) {
	f := newStatusFixture(t)
	if err := runStatus(f.app, true /*json*/); err != nil {
		t.Fatalf("runStatus --json: %v", err)
	}
	if got := strings.TrimSpace(f.out.String()); got != "{\n  \"projects\": []\n}" {
		t.Errorf("empty status --json = %q, want an empty projects array", got)
	}
}

func TestStatusListsProjects(t *testing.T) {
	f := newStatusFixture(t)

	// A running cage.
	f.seed(t, "1111111111111111", doctorLockJSON{ProxyPID: 11, Port: 8080, Agents: agentEntries(12), CanonicalPath: "/code/alpha"})
	f.proc.AlivePIDs[11] = true
	f.proc.AlivePIDs[12] = true
	f.proc.StartTimes[12] = agentStart(12)
	f.ports.Listening[8080] = true

	// An orphan (proxy up, no live agents).
	f.seed(t, "2222222222222222", doctorLockJSON{ProxyPID: 21, Port: 8081, Agents: agentEntries(99), CanonicalPath: "/code/beta"})
	f.proc.AlivePIDs[21] = true
	f.ports.Listening[8081] = true

	if err := runStatus(f.app, false); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	out := f.out.String()
	for _, want := range []string{
		"PROJECT", "STATE", "PORT", "AGENTS",
		"/code/alpha", "running", "8080",
		"/code/beta", "orphan",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q\n%s", want, out)
		}
	}
}
