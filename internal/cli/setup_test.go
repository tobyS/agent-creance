package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

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

func TestSetupDefault(t *testing.T) {
	f := newSetupFixture()

	if err := runSetup(context.Background(), f.app, false, false); err != nil {
		t.Fatalf("runSetup: %v\nstdout: %s", err, f.out)
	}

	// CA installed (Bootstrap → InstallCA) and verified (live probe ran).
	if len(f.kc.AddedCerts) != 1 || f.kc.AddedCerts[0] != setupCAPath {
		t.Errorf("AddedCerts = %v, want one entry %q", f.kc.AddedCerts, setupCAPath)
	}
	if len(f.prober.Calls) != 1 || f.prober.Calls[0].TargetURL != "https://example.com" {
		t.Errorf("prober Calls = %+v, want one probe to https://example.com", f.prober.Calls)
	}
	// Skill written to the canonical path.
	if _, ok := f.fs.Files[skillPath]; !ok {
		t.Errorf("skill not written to %q; files: %v", skillPath, keys(f.fs.Files))
	}
	if got := f.out.String(); !strings.Contains(got, "CA installed and verified") ||
		!strings.Contains(got, "Skill installed") {
		t.Errorf("stdout = %q, want CA + skill success lines", got)
	}
}

func TestSetupNoSkill(t *testing.T) {
	f := newSetupFixture()

	if err := runSetup(context.Background(), f.app, true /*noSkill*/, false); err != nil {
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

	if err := runSetup(context.Background(), f.app, false, true /*noCAInstall*/); err != nil {
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
	if got := f.out.String(); !strings.Contains(got, "env") ||
		!strings.Contains(got, "Go-based") || !strings.Contains(got, "`gh`") {
		t.Errorf("stdout = %q, want the env-var-only coverage caveat", got)
	}
	// Skill still installed.
	if _, ok := f.fs.Files[skillPath]; !ok {
		t.Errorf("skill not written under --no-ca-install (skill should still install)")
	}
}

func TestSetupVerifyFailure(t *testing.T) {
	f := newSetupFixture()
	f.prober.Outcome = sysdep.ProbeUntrusted // CA in keychain but not trusted

	err := runSetup(context.Background(), f.app, false, false)
	if err == nil {
		t.Fatal("runSetup succeeded, want a verification failure")
	}
	if !strings.Contains(err.Error(), "CA verification failed") {
		t.Errorf("err = %q, want the actionable untrusted message", err)
	}
	// InstallCA runs before Verify, so the cert was added before the failure.
	if len(f.kc.AddedCerts) != 1 {
		t.Errorf("AddedCerts = %v, want the cert added before verify", f.kc.AddedCerts)
	}
	// Setup aborts before the skill step on a CA failure.
	if _, ok := f.fs.Files[skillPath]; ok {
		t.Errorf("skill written despite CA verification failure")
	}
}

func TestSetupBothOptOuts(t *testing.T) {
	f := newSetupFixture()

	if err := runSetup(context.Background(), f.app, true /*noSkill*/, true /*noCAInstall*/); err != nil {
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
	if got := f.out.String(); !strings.Contains(got, "Skipping system trust install") {
		t.Errorf("stdout = %q, want the caveat", got)
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
