package cli

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/sysdep"
	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

// setupCAPath is where the Installer resolves the mitmproxy CA cert, given runHome
// (defined in run_test.go). Pre-seeding it lets EnsureCA take its idempotent fast
// path so the tests don't depend on the mitmdump-generation poll loop.
const setupCAPath = runHome + "/.mitmproxy/mitmproxy-ca-cert.pem"

// setupFixture bundles an App wired from sysdep fakes plus the knobs a setup test
// asserts on. Like run_test.go, the keychain/curl-dependent steps can't be faked
// through cli.Main (it wires the real OS seams), so runSetup is exercised directly.
type setupFixture struct {
	app    *App
	fs     *sysdeptest.FakeFileSystem
	kc     *sysdeptest.FakeKeychain
	proc   *sysdeptest.FakeProcessManager
	prober *sysdeptest.FakeTLSProber
	out    *bytes.Buffer
}

// newSetupFixture wires the happy-path defaults: the CA PEM already present (so
// EnsureCA is a no-op), an empty keychain ready to record the trusted cert, and a
// TLS prober that reports the CA trusted.
func newSetupFixture() *setupFixture {
	fs := sysdeptest.NewFakeFileSystem()
	fs.Files[setupCAPath] = []byte("-----BEGIN CERTIFICATE-----")

	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = runHome

	proc := sysdeptest.NewFakeProcessManager()
	proc.SpawnPID = 7000
	ports := sysdeptest.NewFakePortAllocator()
	ports.AllocPort = 54321

	out := &bytes.Buffer{}
	app := &App{
		Stdout:         out,
		Stderr:         &bytes.Buffer{},
		FS:             fs,
		Paths:          paths,
		Keychain:       sysdeptest.NewFakeKeychain(),
		ProcessManager: proc,
		PortAllocator:  ports,
		TLSProber:      sysdeptest.NewFakeTLSProber(), // ProbeTrusted by default
		Sleeper:        &sysdeptest.FakeSleeper{},
	}
	return &setupFixture{
		app:    app,
		fs:     fs,
		kc:     app.Keychain.(*sysdeptest.FakeKeychain),
		proc:   proc,
		prober: app.TLSProber.(*sysdeptest.FakeTLSProber),
		out:    out,
	}
}

func TestSetupAlreadyTrusted(t *testing.T) {
	f := newSetupFixture() // default prober is trusted → verify-first skips install

	if err := runSetup(context.Background(), f.app, false, false, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	// Verify-first passed, so the keychain dialog is skipped entirely.
	if len(f.kc.AddedCerts) != 0 {
		t.Errorf("AddedCerts = %v, want none when the CA is already trusted", f.kc.AddedCerts)
	}
	// Only the pre-install verification probe ran.
	if len(f.prober.Calls) != 1 || f.prober.Calls[0].TargetURL != "https://example.com" {
		t.Errorf("prober Calls = %+v, want one verify probe to https://example.com", f.prober.Calls)
	}
	// Skill written to the canonical path.
	if _, ok := f.fs.Files[skillPath]; !ok {
		t.Errorf("skill not written to %q; files: %v", skillPath, keys(f.fs.Files))
	}
	got := f.out.String()
	// Already-trusted notice + discoverability note + skill success.
	if !strings.Contains(got, "already trusted") || !strings.Contains(got, "Skill installed") {
		t.Errorf("stdout = %q, want the already-trusted + skill success lines", got)
	}
	if !strings.Contains(got, "mitmproxy") || !strings.Contains(got, "login keychain") ||
		!strings.Contains(got, "Keychain Access") {
		t.Errorf("stdout = %q, want the keychain discoverability note", got)
	}
	// The pre-prompt explanation must NOT appear when no dialog is shown.
	if strings.Contains(got, "authorization dialog") {
		t.Errorf("stdout = %q, want no pre-prompt on the already-trusted path", got)
	}
}

func TestSetupFreshInstall(t *testing.T) {
	f := newSetupFixture()
	// Untrusted pre-install, trusted post-install → the install path.
	f.prober.Outcomes = []sysdep.ProbeOutcome{sysdep.ProbeUntrusted, sysdep.ProbeTrusted}

	if err := runSetup(context.Background(), f.app, false, false, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	// CA installed (Bootstrap → InstallCA) and verified twice (pre + post).
	if len(f.kc.AddedCerts) != 1 || f.kc.AddedCerts[0] != setupCAPath {
		t.Errorf("AddedCerts = %v, want one entry %q", f.kc.AddedCerts, setupCAPath)
	}
	if len(f.prober.Calls) != 2 {
		t.Errorf("prober Calls = %d, want 2 (verify-first then post-install)", len(f.prober.Calls))
	}
	if _, ok := f.fs.Files[skillPath]; !ok {
		t.Errorf("skill not written to %q; files: %v", skillPath, keys(f.fs.Files))
	}
	got := f.out.String()
	// Pre-prompt before the dialog, then install success + discoverability + skill.
	if !strings.Contains(got, "authorization dialog") {
		t.Errorf("stdout = %q, want the pre-prompt explanation before install", got)
	}
	if !strings.Contains(got, "CA installed and verified") || !strings.Contains(got, "Skill installed") {
		t.Errorf("stdout = %q, want CA + skill success lines", got)
	}
	if !strings.Contains(got, "mitmproxy") || !strings.Contains(got, "login keychain") {
		t.Errorf("stdout = %q, want the keychain discoverability note", got)
	}
}

func TestSetupNoSkill(t *testing.T) {
	f := newSetupFixture()
	// Drive a real install so "CA still installed" stays meaningful.
	f.prober.Outcomes = []sysdep.ProbeOutcome{sysdep.ProbeUntrusted, sysdep.ProbeTrusted}

	if err := runSetup(context.Background(), f.app, true /*noSkill*/, false, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	// CA still installed.
	if len(f.kc.AddedCerts) != 1 {
		t.Errorf("AddedCerts = %v, want the CA installed", f.kc.AddedCerts)
	}
	// Skill NOT written.
	if _, ok := f.fs.Files[skillPath]; ok {
		t.Errorf("skill written to %q despite --no-skill", skillPath)
	}
	if got := f.out.String(); !strings.Contains(got, "Skipping skill install") {
		t.Errorf("stdout = %q, want the skill-skipped notice", got)
	}
}

func TestSetupNoCAInstall(t *testing.T) {
	f := newSetupFixture()

	if err := runSetup(context.Background(), f.app, false, true /*noCAInstall*/, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	// No system-trust change and no live verify under --no-ca-install.
	if len(f.kc.AddedCerts) != 0 {
		t.Errorf("AddedCerts = %v, want none under --no-ca-install", f.kc.AddedCerts)
	}
	if len(f.prober.Calls) != 0 {
		t.Errorf("prober Calls = %+v, want no verify under --no-ca-install", f.prober.Calls)
	}
	// Caveat printed, naming the env vars and the Go-based `gh` gap.
	got := f.out.String()
	if !strings.Contains(got, "env") ||
		!strings.Contains(got, "Go-based") || !strings.Contains(got, "`gh`") {
		t.Errorf("stdout = %q, want the env-var-only coverage caveat", got)
	}
	// No keychain discoverability note when nothing was installed.
	if strings.Contains(got, "login keychain") {
		t.Errorf("stdout = %q, want no keychain note under --no-ca-install", got)
	}
	// Skill still installed.
	if _, ok := f.fs.Files[skillPath]; !ok {
		t.Errorf("skill not written under --no-ca-install (skill should still install)")
	}
}

func TestSetupVerifyFailure(t *testing.T) {
	f := newSetupFixture()
	f.prober.Outcome = sysdep.ProbeUntrusted // CA in keychain but not trusted

	err := runSetup(context.Background(), f.app, false, false, false)
	if err == nil {
		t.Fatal("runSetup succeeded, want a verification failure")
	}
	if !strings.Contains(err.Error(), "CA verification failed") {
		t.Errorf("err = %q, want the actionable untrusted message", err)
	}
	// Verify-first is untrusted, so InstallCA runs; the post-install verify still
	// fails, but the cert was added before the failure.
	if len(f.kc.AddedCerts) != 1 {
		t.Errorf("AddedCerts = %v, want the cert added before the failing verify", f.kc.AddedCerts)
	}
	// Setup aborts before the skill step on a CA failure.
	if _, ok := f.fs.Files[skillPath]; ok {
		t.Errorf("skill written despite CA verification failure")
	}
}

func TestSetupBothOptOuts(t *testing.T) {
	f := newSetupFixture()

	if err := runSetup(context.Background(), f.app, true /*noSkill*/, true /*noCAInstall*/, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	// Near no-op: only the caveat, nothing trusted, verified, or installed.
	if len(f.kc.AddedCerts) != 0 {
		t.Errorf("AddedCerts = %v, want none", f.kc.AddedCerts)
	}
	if len(f.prober.Calls) != 0 {
		t.Errorf("prober Calls = %+v, want none", f.prober.Calls)
	}
	if _, ok := f.fs.Files[skillPath]; ok {
		t.Errorf("skill written despite --no-skill")
	}
	got := f.out.String()
	if !strings.Contains(got, "Skipping system trust install") {
		t.Errorf("stdout = %q, want the caveat", got)
	}
	if strings.Contains(got, "login keychain") {
		t.Errorf("stdout = %q, want no keychain note when nothing was installed", got)
	}
}

// globalConfigPath is where scaffoldGlobalConfig resolves the global config,
// given runHome — the same path config.Loader.GlobalPath returns.
const globalConfigPath = runHome + "/.config/agent-creance.yaml"

func TestSetupScaffoldsGlobalConfig(t *testing.T) {
	f := newSetupFixture()

	if err := runSetup(context.Background(), f.app, false, false, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	data, ok := f.fs.Files[globalConfigPath]
	if !ok {
		t.Fatalf("global config not written to %q; files: %v", globalConfigPath, keys(f.fs.Files))
	}
	// The template must survive the strict parser, which also runs validation
	// (known keys only; passthrough rules without paths/methods).
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatalf("scaffolded global config does not parse: %v", err)
	}
	byHost := map[string]*config.Rule{}
	for i := range cfg.Network.Egress.Allow {
		r := &cfg.Network.Egress.Allow[i]
		byHost[r.Host] = r
	}

	anthropic := byHost["api.anthropic.com"]
	if anthropic == nil {
		t.Fatalf("no api.anthropic.com rule; allow = %+v", cfg.Network.Egress.Allow)
	}
	if anthropic.Mode != config.ModePassthrough {
		t.Errorf("api.anthropic.com mode = %q, want passthrough", anthropic.Mode)
	}

	// AC-0048: Anthropic docs hosts. code.claude.com is GET-only and path-scoped
	// to /docs; the legacy redirector hosts are host-wide GET. All intercept, so
	// the path/method scoping is enforceable (passthrough would reject it).
	docs := byHost["code.claude.com"]
	if docs == nil {
		t.Fatalf("no code.claude.com rule; allow = %+v", cfg.Network.Egress.Allow)
	}
	if docs.Mode != config.ModeIntercept {
		t.Errorf("code.claude.com mode = %q, want intercept", docs.Mode)
	}
	if docs.Paths == nil || !slices.Contains(*docs.Paths, "/docs/") {
		t.Errorf("code.claude.com paths = %v, want to contain %q", docs.Paths, "/docs/")
	}
	if docs.Methods == nil || !slices.Contains(*docs.Methods, "GET") {
		t.Errorf("code.claude.com methods = %v, want to contain GET", docs.Methods)
	}
	for _, host := range []string{"docs.anthropic.com", "docs.claude.com"} {
		r := byHost[host]
		if r == nil {
			t.Fatalf("no %s rule; allow = %+v", host, cfg.Network.Egress.Allow)
		}
		if r.Mode != config.ModeIntercept {
			t.Errorf("%s mode = %q, want intercept", host, r.Mode)
		}
		if r.Methods == nil || !slices.Contains(*r.Methods, "GET") {
			t.Errorf("%s methods = %v, want to contain GET", host, r.Methods)
		}
	}

	if got := f.out.String(); !strings.Contains(got, "✓ Wrote "+globalConfigPath) {
		t.Errorf("stdout = %q, want the global-config success line", got)
	}
}

func TestSetupGlobalConfigExistsUntouched(t *testing.T) {
	f := newSetupFixture()
	custom := []byte("# my own config\n")
	f.fs.Files[globalConfigPath] = custom

	if err := runSetup(context.Background(), f.app, false, false, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	if got := f.fs.Files[globalConfigPath]; !bytes.Equal(got, custom) {
		t.Errorf("existing global config rewritten:\n%s", got)
	}
	if got := f.out.String(); !strings.Contains(got, "already exists — left untouched") {
		t.Errorf("stdout = %q, want the left-untouched notice", got)
	}
}

func TestSetupNoGlobalConfig(t *testing.T) {
	f := newSetupFixture()

	if err := runSetup(context.Background(), f.app, false, false, true /*noGlobalConfig*/); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	if _, ok := f.fs.Files[globalConfigPath]; ok {
		t.Errorf("global config written despite --no-global-config")
	}
	if got := f.out.String(); !strings.Contains(got, "Skipping global config baseline") {
		t.Errorf("stdout = %q, want the global-config skip notice", got)
	}
}

// TestSetupSeedsGlobalConfigFromClaude pins that, on a fresh machine, setup seeds
// the global baseline from the user's global Claude Code config: a WebFetch domain
// (GET-only intercept) and a remote MCP server (passthrough).
func TestSetupSeedsGlobalConfigFromClaude(t *testing.T) {
	f := newSetupFixture()
	f.fs.Files[runHome+"/.claude/settings.json"] =
		[]byte(`{"permissions":{"allow":["WebFetch(domain:docs.mylib.dev)"]}}`)
	f.fs.Files[runHome+"/.claude.json"] =
		[]byte(`{"mcpServers":{"corp":{"type":"http","url":"https://mcp.corp.example"}}}`)

	if err := runSetup(context.Background(), f.app, false, false, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	data, ok := f.fs.Files[globalConfigPath]
	if !ok {
		t.Fatalf("global config not written; files: %v", keys(f.fs.Files))
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatalf("seeded global config does not parse: %v\n%s", err, data)
	}
	web := findHost(t, cfg.Network.Egress.Allow, "docs.mylib.dev")
	if web.Mode != config.ModeIntercept || web.Methods == nil {
		t.Errorf("docs.mylib.dev = %+v, want GET-only intercept", web)
	}
	mcp := findHost(t, cfg.Network.Egress.Allow, "mcp.corp.example")
	if mcp.Mode != config.ModePassthrough {
		t.Errorf("mcp.corp.example mode = %q, want passthrough", mcp.Mode)
	}
	// Baseline hosts still present (seeded, not replaced).
	findHost(t, cfg.Network.Egress.Allow, "api.anthropic.com")
	if got := f.out.String(); !strings.Contains(got, "seeded from your global Claude Code config") {
		t.Errorf("stdout = %q, want the seeded notice", got)
	}
}

// TestSetupNoSkillStillScaffoldsGlobalConfig pins that the --no-skill branch no
// longer early-returns: the baseline step runs regardless of the skill opt-out.
func TestSetupNoSkillStillScaffoldsGlobalConfig(t *testing.T) {
	f := newSetupFixture()

	if err := runSetup(context.Background(), f.app, true /*noSkill*/, false, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	if _, ok := f.fs.Files[globalConfigPath]; !ok {
		t.Errorf("global config not written under --no-skill; files: %v", keys(f.fs.Files))
	}
}

// TestSetupCommandPrintsNextStep pins the orientation pointer added in the setup
// command's RunE closure (S5): a successful `setup` ends by pointing at `init`.
// The line lives in the closure, not runSetup, so init's inline setup gate never
// prints it — hence this exercises newSetupCmd rather than calling runSetup.
func TestSetupCommandPrintsNextStep(t *testing.T) {
	f := newSetupFixture() // already-trusted happy path

	cmd := newSetupCmd(f.app)
	cmd.SetArgs(nil)
	cmd.SetOut(f.out)
	cmd.SetErr(f.out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup command: %v\nstdout: %s", err, f.out)
	}

	if got := f.out.String(); !strings.Contains(got, "Next: run `agent-creance init` in your project.") {
		t.Errorf("stdout = %q, want the next-step pointer to init", got)
	}
}

// keys returns the map keys, for readable assertion failure messages.
func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
