package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tobyS/agent-creance/internal/buildinfo"
	"github.com/tobyS/agent-creance/internal/cred"
	"github.com/tobyS/agent-creance/internal/setupcheck"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// runFixture bundles an App wired entirely from sysdep fakes plus the knobs a
// run test needs to assert on. The CLI's keychain-dependent steps can't be faked
// through cli.Main (it wires the real OSKeychain), so the run body is exercised
// directly against the fakes here — the project's stated testability model.
type runFixture struct {
	app   *App
	fs    *sysdeptest.FakeFileSystem
	paths *sysdeptest.FakePathResolver
	kc    *sysdeptest.FakeKeychain
	cmd   *sysdeptest.FakeCommander
	flock *sysdeptest.FakeFlock
	proc  *sysdeptest.FakeProcessManager
	ports *sysdeptest.FakePortAllocator
	pg    *sysdeptest.FakeProcessGroup
	out   *bytes.Buffer
	err   *bytes.Buffer
	lay   state.Layout
}

const (
	runHome    = "/home/toby"
	runProj    = "/home/toby/proj"
	runUser    = "toby"
	allocPort  = 48080
	proxyPID   = 4242
	skillPath  = runHome + "/.claude/skills/agent-creance/SKILL.md"
	projConfig = runProj + "/.agent-creance.yaml"
)

// newRunFixture wires the happy-path defaults: both prerequisites installed at the
// tested versions, a trusted CA + skill + Keychain credential, a minimal project
// config (no generators, so compile stays offline), and process fakes that let the
// proxy "start" and the cage "run" to a clean exit without touching the OS.
func newRunFixture(t *testing.T) *runFixture {
	t.Helper()

	fs := sysdeptest.NewFakeFileSystem()
	fs.Files[skillPath] = []byte("# skill")
	fs.Files[projConfig] = []byte("agent:\n  command: [\"claude\"]\n" +
		"network:\n  egress:\n    allow:\n      - host: api.anthropic.com\n        mode: passthrough\n")

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = runHome
	paths.Env["USER"] = runUser
	paths.Cwd = runProj // so "." resolves to the project dir

	resolver := state.New(paths)
	lay, err := resolver.Resolve(runProj)
	if err != nil {
		t.Fatalf("resolve layout: %v", err)
	}

	kc := sysdeptest.NewFakeKeychain().
		WithCertificate(setupcheck.CACommonName, "-----BEGIN CERTIFICATE-----").
		WithItem(cred.KeychainService, runUser, `{"claudeAiOauth":{}}`)

	cmd := sysdeptest.NewFakeCommander().
		WithTool("safehouse", "/opt/homebrew/bin/safehouse",
			"Agent Safehouse "+buildinfo.TestedVersions[buildinfo.ToolSafehouse]).
		WithTool("mitmproxy", "/usr/local/bin/mitmproxy",
			"Mitmproxy: "+buildinfo.TestedVersions[buildinfo.ToolMitmproxy])

	flock := sysdeptest.NewFakeFlock()
	proc := sysdeptest.NewFakeProcessManager()
	proc.SpawnPID = proxyPID
	proc.AlivePIDs[proxyPID] = true // so last-out Detach can SIGTERM it
	ports := sysdeptest.NewFakePortAllocator()
	ports.AllocPort = allocPort
	pg := sysdeptest.NewFakeProcessGroup() // Start returns a process that Waits clean

	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	app := &App{
		Commander:      cmd,
		Stdout:         out,
		Stderr:         errb,
		Tested:         buildinfo.TestedVersions,
		FS:             fs,
		Paths:          paths,
		Clock:          sysdeptest.NewFakeClock(time.Unix(0, 0)),
		HTTP:           sysdeptest.NewFakeHTTPGetter(),
		Keychain:       kc,
		ProcessGroup:   pg,
		Flock:          flock,
		ProcessManager: proc,
		PortAllocator:  ports,
	}
	return &runFixture{
		app: app, fs: fs, paths: paths, kc: kc, cmd: cmd, flock: flock,
		proc: proc, ports: ports, pg: pg, out: out, err: errb, lay: lay,
	}
}

func TestRunHappyPath(t *testing.T) {
	f := newRunFixture(t)

	if err := runRun(context.Background(), f.app, "."); err != nil {
		t.Fatalf("runRun: %v\nstdout: %s\nstderr: %s", err, f.out, f.err)
	}

	// The proxy stub was launched: mitmdump spawned with the allocated port.
	if len(f.proc.Spawned) != 1 {
		t.Fatalf("mitmdump spawns = %d, want 1 (%+v)", len(f.proc.Spawned), f.proc.Spawned)
	}
	if got := f.proc.Spawned[0].Name; got != "mitmdump" {
		t.Errorf("spawned %q, want mitmdump", got)
	}
	if !argsContain(f.proc.Spawned[0].Args, "--listen-port", "48080") {
		t.Errorf("mitmdump args missing --listen-port 48080: %v", f.proc.Spawned[0].Args)
	}

	// The cage stub was launched: safehouse with both append-profiles and proxy env.
	started := f.pg.Started()
	if len(started) != 1 {
		t.Fatalf("cage starts = %d, want 1", len(started))
	}
	if started[0].Name != "safehouse" {
		t.Errorf("cage exec %q, want safehouse", started[0].Name)
	}
	if !argsContain(started[0].Args, "--append-profile", f.lay.NetworkSB()) {
		t.Errorf("cage args missing network.sb append-profile: %v", started[0].Args)
	}
	if !argsContain(started[0].Args, "--append-profile", f.lay.ProxyProfileSB()) {
		t.Errorf("cage args missing proxy.sb append-profile: %v", started[0].Args)
	}
	if !envHasPrefix(started[0].Env, "HTTPS_PROXY=http://127.0.0.1:48080") {
		t.Errorf("cage env missing HTTPS_PROXY to the proxy port: %v", started[0].Env)
	}

	// Lock file shows attach then last-out detach: the proxy was SIGTERM'd and the
	// final lock has no attached agents.
	if !signaled(f.proc, proxyPID, syscall.SIGTERM) {
		t.Errorf("proxy was not SIGTERM'd on last-out detach: %+v", f.proc.Signaled)
	}
	if agents := lockAgents(t, f.flock, f.lay.ProxyLock()); len(agents) != 0 {
		t.Errorf("final lock agents = %v, want empty (last out)", agents)
	}
}

// TestRunLaunchesResolvedBinary asserts check/exec agreement (AC-0039): the
// cage execs exactly the safehouse binary name the prereq check resolved, for
// either install name, with the preferred name winning when both exist.
func TestRunLaunchesResolvedBinary(t *testing.T) {
	tests := []struct {
		name      string
		installed []string
		want      string
	}{
		{"preferred name only", []string{"safehouse"}, "safehouse"},
		{"fallback name only", []string{"agent-safehouse"}, "agent-safehouse"},
		{"both installed, preferred wins", []string{"safehouse", "agent-safehouse"}, "safehouse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRunFixture(t)
			delete(f.cmd.Paths, "safehouse") // replace the fixture's default install
			delete(f.cmd.Outputs, "safehouse")
			for _, name := range tt.installed {
				f.cmd.WithTool(name, "/opt/homebrew/bin/"+name,
					"Agent Safehouse "+buildinfo.TestedVersions[buildinfo.ToolSafehouse])
			}

			if err := runRun(context.Background(), f.app, "."); err != nil {
				t.Fatalf("runRun: %v\nstdout: %s\nstderr: %s", err, f.out, f.err)
			}
			started := f.pg.Started()
			if len(started) != 1 {
				t.Fatalf("cage starts = %d, want 1", len(started))
			}
			if started[0].Name != tt.want {
				t.Errorf("cage exec %q, want %q", started[0].Name, tt.want)
			}
		})
	}
}

func TestRunSetupMissing(t *testing.T) {
	f := newRunFixture(t)
	f.kc.Certs = map[string][]byte{} // no trusted CA → setup incomplete

	err := runRun(context.Background(), f.app, ".")
	if err == nil {
		t.Fatal("runRun succeeded, want a setup-incomplete refusal")
	}
	if !strings.Contains(f.out.String(), "agent-creance setup") {
		t.Errorf("stdout = %q, want a pointer to `agent-creance setup`", f.out)
	}
	if len(f.proc.Spawned) != 0 {
		t.Errorf("proxy was started despite missing setup: %+v", f.proc.Spawned)
	}
	if len(f.pg.Started()) != 0 {
		t.Errorf("cage was started despite missing setup")
	}
}

func TestRunCredentialMissing(t *testing.T) {
	f := newRunFixture(t)
	f.kc.Items = map[string][]byte{} // CA present, but no Keychain credential

	err := runRun(context.Background(), f.app, ".")
	if err == nil {
		t.Fatal("runRun succeeded, want a credential refusal")
	}
	if !strings.Contains(f.out.String(), "No Claude credential") {
		t.Errorf("stdout = %q, want the missing-credential message", f.out)
	}
	if len(f.proc.Spawned) != 0 {
		t.Errorf("proxy was started despite missing credential: %+v", f.proc.Spawned)
	}
}

func TestRunMissingPrerequisite(t *testing.T) {
	f := newRunFixture(t)
	f.app.Commander = sysdeptest.NewFakeCommander() // neither tool installed

	err := runRun(context.Background(), f.app, ".")
	if err == nil {
		t.Fatal("runRun succeeded, want a missing-prerequisite refusal")
	}
	if !strings.Contains(f.out.String(), "not installed") {
		t.Errorf("stdout = %q, want the install instructions", f.out)
	}
	if len(f.proc.Spawned) != 0 || len(f.pg.Started()) != 0 {
		t.Errorf("proxy/cage started despite missing prerequisite")
	}
}

// --- helpers ---

func argsContain(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func envHasPrefix(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

func signaled(p *sysdeptest.FakeProcessManager, pid int, sig syscall.Signal) bool {
	for _, s := range p.Signaled {
		if s.PID == pid && s.Sig == sig {
			return true
		}
	}
	return false
}

func lockAgents(t *testing.T, fl *sysdeptest.FakeFlock, path string) []int {
	t.Helper()
	data := fl.Contents[path]
	if len(data) == 0 {
		return nil
	}
	var ls struct {
		Agents []int `json:"agents"`
	}
	if err := json.Unmarshal(data, &ls); err != nil {
		t.Fatalf("unmarshal lock: %v", err)
	}
	return ls.Agents
}
