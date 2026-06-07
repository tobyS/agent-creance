package setup

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep/sysdeptest"
)

const testHome = "/home/toby"

// caPath is the CA cert path EnsureCA resolves, given testHome.
var caPath = filepath.Join(testHome, ".mitmproxy", "mitmproxy-ca-cert.pem")

// fakes bundles the sysdep fakes an Installer is built from, so tests can drive
// inputs and assert recorded calls.
type fakes struct {
	fs      *sysdeptest.FakeFileSystem
	kc      *sysdeptest.FakeKeychain
	proc    *sysdeptest.FakeProcessManager
	ports   *sysdeptest.FakePortAllocator
	prober  *sysdeptest.FakeTLSProber
	sleeper *sysdeptest.FakeSleeper
	paths   *sysdeptest.FakePathResolver
}

func newFakes() *fakes {
	paths := sysdeptest.NewFakePathResolver()
	paths.HomeDir = testHome
	return &fakes{
		fs:      sysdeptest.NewFakeFileSystem(),
		kc:      sysdeptest.NewFakeKeychain(),
		proc:    sysdeptest.NewFakeProcessManager(),
		ports:   sysdeptest.NewFakePortAllocator(),
		prober:  sysdeptest.NewFakeTLSProber(),
		sleeper: &sysdeptest.FakeSleeper{},
		paths:   paths,
	}
}

func (f *fakes) installer() *Installer {
	return NewInstaller(f.fs, f.kc, f.proc, f.ports, f.prober, f.sleeper, f.paths)
}

// appearingFS wraps FakeFileSystem so the CA cert is reported absent for the
// first appearAfter Stat calls and present afterwards — modelling mitmdump
// writing the file shortly after spawn.
type appearingFS struct {
	*sysdeptest.FakeFileSystem
	path        string
	appearAfter int
	statCount   int
}

func (a *appearingFS) Stat(name string) (fs.FileInfo, error) {
	if name == a.path {
		a.statCount++
		if a.statCount > a.appearAfter {
			a.FakeFileSystem.Files[name] = []byte("-----BEGIN CERTIFICATE-----")
		}
	}
	return a.FakeFileSystem.Stat(name)
}

func TestEnsureCAIdempotentWhenPresent(t *testing.T) {
	f := newFakes()
	f.fs.Files[caPath] = []byte("-----BEGIN CERTIFICATE-----")

	got, err := f.installer().EnsureCA(context.Background())
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if got != caPath {
		t.Errorf("certPath = %q, want %q", got, caPath)
	}
	// An existing CA must not be regenerated: no proxy spawned.
	if len(f.proc.Spawned) != 0 {
		t.Errorf("Spawned = %+v, want no generation", f.proc.Spawned)
	}
}

func TestEnsureCAGeneratesWhenAbsent(t *testing.T) {
	f := newFakes()
	f.ports.AllocPort = 54321
	f.proc.SpawnPID = 4242
	gfs := &appearingFS{FakeFileSystem: f.fs, path: caPath, appearAfter: 2}
	inst := NewInstaller(gfs, f.kc, f.proc, f.ports, f.prober, f.sleeper, f.paths)

	got, err := inst.EnsureCA(context.Background())
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	if got != caPath {
		t.Errorf("certPath = %q, want %q", got, caPath)
	}

	// A bare mitmdump is spawned on the allocated port.
	if len(f.proc.Spawned) != 1 {
		t.Fatalf("Spawned = %+v, want one mitmdump spawn", f.proc.Spawned)
	}
	cmd := f.proc.Spawned[0]
	if cmd.Name != mitmdumpBin {
		t.Errorf("spawned %q, want %q", cmd.Name, mitmdumpBin)
	}
	if !argsContain(cmd.Args, "--listen-port", "54321") {
		t.Errorf("args = %v, want --listen-port 54321", cmd.Args)
	}
	// It polled (slept at least once) while waiting for the file.
	if len(f.sleeper.Sleeps) == 0 {
		t.Error("expected at least one poll sleep while waiting for the CA")
	}
	// The throwaway proxy is torn down with SIGTERM.
	if len(f.proc.Signaled) != 1 || f.proc.Signaled[0].PID != 4242 || f.proc.Signaled[0].Sig != syscall.SIGTERM {
		t.Errorf("Signaled = %+v, want SIGTERM to pid 4242", f.proc.Signaled)
	}
}

func TestEnsureCAGenerationTimeoutTearsDown(t *testing.T) {
	f := newFakes()
	f.proc.SpawnPID = 99
	// CA never appears (plain FakeFileSystem, file absent), FakeSleeper is instant.

	_, err := f.installer().EnsureCA(context.Background())
	if err == nil {
		t.Fatal("EnsureCA = nil, want a generation-timeout error")
	}
	// Even on timeout the proxy must be killed.
	if len(f.proc.Signaled) != 1 || f.proc.Signaled[0].Sig != syscall.SIGTERM {
		t.Errorf("Signaled = %+v, want SIGTERM teardown on timeout", f.proc.Signaled)
	}
}

func TestEnsureCAAllocateErrorDoesNotSpawn(t *testing.T) {
	f := newFakes()
	f.ports.AllocErr = errors.New("no ports")

	if _, err := f.installer().EnsureCA(context.Background()); err == nil {
		t.Fatal("EnsureCA = nil, want allocate error")
	}
	if len(f.proc.Spawned) != 0 {
		t.Errorf("Spawned = %+v, want no spawn after allocate failure", f.proc.Spawned)
	}
}

func TestEnsureCASpawnError(t *testing.T) {
	f := newFakes()
	f.proc.SpawnErr = errors.New("exec failed")

	if _, err := f.installer().EnsureCA(context.Background()); err == nil {
		t.Fatal("EnsureCA = nil, want spawn error")
	}
}

func TestEnsureCAHomeDirError(t *testing.T) {
	f := newFakes()
	boom := errors.New("no home")
	f.paths.HomeErr = boom

	if _, err := f.installer().EnsureCA(context.Background()); !errors.Is(err, boom) {
		t.Errorf("EnsureCA error = %v, want it to wrap %v", err, boom)
	}
}

func TestEnsureCAStatErrorSurfaced(t *testing.T) {
	f := newFakes()
	boom := errors.New("permission denied")
	f.fs.StatErrs[caPath] = boom

	if _, err := f.installer().EnsureCA(context.Background()); !errors.Is(err, boom) {
		t.Errorf("EnsureCA error = %v, want it to wrap %v", err, boom)
	}
}

func TestInstallCACallsKeychain(t *testing.T) {
	f := newFakes()
	if err := f.installer().InstallCA(caPath); err != nil {
		t.Fatalf("InstallCA: %v", err)
	}
	if len(f.kc.AddedCerts) != 1 || f.kc.AddedCerts[0] != caPath {
		t.Errorf("AddedCerts = %+v, want [%s]", f.kc.AddedCerts, caPath)
	}
}

func TestInstallCAPropagatesKeychainError(t *testing.T) {
	f := newFakes()
	boom := errors.New("add-trusted-cert failed")
	f.kc.AddCertErr = boom

	if err := f.installer().InstallCA(caPath); !errors.Is(err, boom) {
		t.Errorf("InstallCA error = %v, want it to wrap %v", err, boom)
	}
}

// argsContain reports whether want... appears as a contiguous subsequence of args.
func argsContain(args []string, want ...string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j, w := range want {
			if args[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
