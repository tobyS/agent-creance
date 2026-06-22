package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	watch *sysdeptest.FakeFileWatcherFactory
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
	ports.Listening[allocPort] = true      // the spawned proxy listens → Attach readiness wait passes
	pg := sysdeptest.NewFakeProcessGroup() // Start returns a process that Waits clean
	watch := sysdeptest.NewFakeFileWatcherFactory()

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
		Terminal:       &sysdeptest.FakeTerminal{}, // non-tty stderr → append-only progress lines
		Keychain:       kc,
		ProcessGroup:   pg,
		Flock:          flock,
		ProcessManager: proc,
		PortAllocator:  ports,
		Sleeper:        &sysdeptest.FakeSleeper{},
		WatcherFactory: watch,
	}
	return &runFixture{
		app: app, fs: fs, paths: paths, kc: kc, cmd: cmd, flock: flock,
		proc: proc, ports: ports, pg: pg, watch: watch, out: out, err: errb, lay: lay,
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
	// The claude command gets the launch-time cage briefing (AC-0047).
	briefing := false
	for i, a := range started[0].Args {
		if a == "--append-system-prompt" && i+1 < len(started[0].Args) &&
			strings.Contains(started[0].Args[i+1], "470") {
			briefing = true
		}
	}
	if !briefing {
		t.Errorf("cage args missing the --append-system-prompt briefing: %v", started[0].Args)
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

// TestRunProgressOutput asserts the step announcements on stderr (AC-0041):
// every major step is announced with a duration, a first compile reports its
// rule counts, and a cached re-run reports "up to date". The fixture's stderr
// is a non-tty (FakeTerminal zero value), so the output is plain appended
// lines; the in-place \r rendering is byte-tested in internal/progress.
func TestRunProgressOutput(t *testing.T) {
	f := newRunFixture(t)

	if err := runRun(context.Background(), f.app, "."); err != nil {
		t.Fatalf("runRun: %v\nstdout: %s\nstderr: %s", err, f.out, f.err)
	}
	want := "Compiling egress policy…\n" +
		"✓ Egress policy compiled: 1 allow, 0 deny (0s)\n" +
		"Compiling sandbox profile…\n" +
		"✓ Sandbox profile compiled (0s)\n" +
		"Starting egress proxy…\n" +
		"✓ Egress proxy ready on port 48080 (0s)\n" +
		"Launching agent…\n"
	if got := f.err.String(); got != want {
		t.Errorf("first-run stderr:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// A second run hits the input-hash cache: same steps, compact cached line.
	f.err.Reset()
	if err := runRun(context.Background(), f.app, "."); err != nil {
		t.Fatalf("runRun #2: %v\nstderr: %s", err, f.err)
	}
	if !strings.Contains(f.err.String(), "✓ Egress policy up to date (cached) (0s)\n") {
		t.Errorf("cached-run stderr = %q, want the up-to-date line", f.err)
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

// TestRunGrantsLocalPluginMarketplaceDirs asserts a local ("directory") plugin
// marketplace outside the cage is mounted read-only, while one inside the project
// (already mounted) is filtered out (AC-0056).
func TestRunGrantsLocalPluginMarketplaceDirs(t *testing.T) {
	f := newRunFixture(t)
	const outside = "/work/toby-plugins"
	inside := runProj + "/embedded-mkt"
	f.fs.Files[runHome+"/.claude/plugins/known_marketplaces.json"] = []byte(`{
		"toby-plugins": {"source": {"source": "directory", "path": "` + outside + `"}},
		"git":          {"source": {"source": "github", "repo": "x/y"}},
		"embedded":     {"source": {"source": "directory", "path": "` + inside + `"}}
	}`)
	f.fs.Dirs[outside] = true
	f.fs.Dirs[inside] = true

	if err := runRun(context.Background(), f.app, "."); err != nil {
		t.Fatalf("runRun: %v\nstderr: %s", err, f.err)
	}
	started := f.pg.Started()
	if len(started) != 1 {
		t.Fatalf("cage starts = %d, want 1", len(started))
	}
	// Exactly the outside dir is granted (so the --add-dirs-ro value equals it);
	// the inside-project dir was filtered, else the value would be colon-joined.
	if !argsContain(started[0].Args, "--add-dirs-ro", outside) {
		t.Errorf("cage args missing --add-dirs-ro %s: %v", outside, started[0].Args)
	}
	if !strings.Contains(f.err.String(), outside) {
		t.Errorf("stderr missing read-only grant notice for %s: %s", outside, f.err)
	}
}

// TestRunMalformedMarketplaceRegistryWarns asserts a broken known_marketplaces.json
// is a non-fatal warning: the launch still proceeds and no extra RO mount is added.
func TestRunMalformedMarketplaceRegistryWarns(t *testing.T) {
	f := newRunFixture(t)
	f.fs.Files[runHome+"/.claude/plugins/known_marketplaces.json"] = []byte(`{not json`)

	if err := runRun(context.Background(), f.app, "."); err != nil {
		t.Fatalf("runRun should not fail on a malformed registry: %v", err)
	}
	if !strings.Contains(f.err.String(), "plugin marketplace") {
		t.Errorf("stderr missing plugin-marketplace warning: %s", f.err)
	}
	started := f.pg.Started()
	if len(started) != 1 {
		t.Fatalf("cage starts = %d, want 1", len(started))
	}
	for i, a := range started[0].Args {
		if a == "--add-dirs-ro" {
			t.Errorf("unexpected --add-dirs-ro from a malformed registry: %v", started[0].Args[i:])
		}
	}
}

// TestRunStartsAndStopsConfigWatcher asserts the run session watches the project
// config dir for hot-reload (AC-0053) and tears the watcher down cleanly when the
// agent exits.
func TestRunStartsAndStopsConfigWatcher(t *testing.T) {
	f := newRunFixture(t)

	if err := runRun(context.Background(), f.app, "."); err != nil {
		t.Fatalf("runRun: %v\nstderr: %s", err, f.err)
	}

	if added := f.watch.Watcher.Added(); len(added) != 1 || added[0] != runProj {
		t.Errorf("watched dirs = %v, want [%s] (the project config dir)", added, runProj)
	}
	if !f.watch.Watcher.Closed() {
		t.Error("config watcher was not closed on run exit")
	}
}

// TestRunConfigWatcherFailureIsAdvisory asserts that a watcher that cannot be
// created warns but does not fail the run (hot-reload is best-effort).
func TestRunConfigWatcherFailureIsAdvisory(t *testing.T) {
	f := newRunFixture(t)
	f.watch.NewErr = errFakeWatcher

	if err := runRun(context.Background(), f.app, "."); err != nil {
		t.Fatalf("runRun should not fail when the watcher can't start: %v", err)
	}
	if !strings.Contains(f.err.String(), "config hot-reload unavailable") {
		t.Errorf("stderr missing the advisory warning: %s", f.err)
	}
	if len(f.pg.Started()) != 1 {
		t.Errorf("agent was not launched despite the watcher failure")
	}
}

// --- helpers ---

var errFakeWatcher = errors.New("fake watcher unavailable")

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
