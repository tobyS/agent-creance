package setup

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

// update regenerates the golden files: go test ./internal/setup -update
var update = flag.Bool("update", false, "regenerate golden files")

func TestVerifyTrusted(t *testing.T) {
	f := newFakes()
	f.ports.AllocPort = 54321
	f.proc.SpawnPID = 7000
	// FakeTLSProber defaults to ProbeTrusted.

	res, err := f.installer().Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !res.OK() || res.Status != StatusTrusted {
		t.Errorf("Result = %+v, want trusted", res)
	}
	if res.Message() != "" {
		t.Errorf("Message() = %q, want empty when trusted", res.Message())
	}

	// A bare mitmdump is spawned on the allocated port and torn down.
	if len(f.proc.Spawned) != 1 || f.proc.Spawned[0].Name != mitmdumpBin ||
		!argsContain(f.proc.Spawned[0].Args, "--listen-port", "54321") {
		t.Errorf("Spawned = %+v, want one mitmdump on port 54321", f.proc.Spawned)
	}
	if len(f.proc.Signaled) != 1 || f.proc.Signaled[0].PID != 7000 || f.proc.Signaled[0].Sig != syscall.SIGTERM {
		t.Errorf("Signaled = %+v, want SIGTERM to pid 7000", f.proc.Signaled)
	}
	// The probe goes through the allocated proxy to the public target.
	if len(f.prober.Calls) != 1 ||
		f.prober.Calls[0].ProxyURL != "http://127.0.0.1:54321" ||
		f.prober.Calls[0].TargetURL != verifyTargetURL {
		t.Errorf("prober Calls = %+v, want one probe via 127.0.0.1:54321 to %s", f.prober.Calls, verifyTargetURL)
	}
}

func TestVerifyUntrusted(t *testing.T) {
	f := newFakes()
	f.prober.Outcome = sysdep.ProbeUntrusted

	res, err := f.installer().Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.OK() || res.Status != StatusUntrusted {
		t.Errorf("Result = %+v, want untrusted", res)
	}

	// The actionable message is pinned in a golden file.
	golden := filepath.Join("testdata", "verify_untrusted.golden")
	got := res.Message()
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden file; run with -update to create it: %v", err)
	}
	if string(want) != got {
		t.Errorf("Message() = %q, want golden %q", got, string(want))
	}

	// Even on an untrusted verdict the throwaway proxy is torn down.
	if len(f.proc.Signaled) != 1 || f.proc.Signaled[0].Sig != syscall.SIGTERM {
		t.Errorf("Signaled = %+v, want SIGTERM teardown", f.proc.Signaled)
	}
}

func TestVerifyProbeErrorIsFailure(t *testing.T) {
	f := newFakes()
	boom := errors.New("curl not installed")
	f.prober.Err = boom

	if _, err := f.installer().Verify(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Verify error = %v, want it to wrap %v", err, boom)
	}
}

func TestVerifyEnvironmentErrorOutcomeIsFailure(t *testing.T) {
	f := newFakes()
	// Probe ran but curl returned a non-trust error code (e.g. exit 35) -> ProbeError.
	f.prober.Outcome = sysdep.ProbeError

	if _, err := f.installer().Verify(context.Background()); err == nil {
		t.Error("Verify = nil, want an environment-error failure for ProbeError outcome")
	}
}

func TestVerifyAllocateErrorDoesNotSpawn(t *testing.T) {
	f := newFakes()
	f.ports.AllocErr = errors.New("no ports")

	if _, err := f.installer().Verify(context.Background()); err == nil {
		t.Fatal("Verify = nil, want allocate error")
	}
	if len(f.proc.Spawned) != 0 {
		t.Errorf("Spawned = %+v, want no spawn after allocate failure", f.proc.Spawned)
	}
}

func TestVerifySpawnError(t *testing.T) {
	f := newFakes()
	f.proc.SpawnErr = errors.New("exec failed")

	if _, err := f.installer().Verify(context.Background()); err == nil {
		t.Fatal("Verify = nil, want spawn error")
	}
}

func TestBootstrapHappyPath(t *testing.T) {
	f := newFakes()
	f.fs.Files[caPath] = []byte("-----BEGIN CERTIFICATE-----") // CA already generated
	// Default prober outcome is trusted.

	if err := f.installer().Bootstrap(context.Background()); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// CA installed into the keychain.
	if len(f.kc.AddedCerts) != 1 || f.kc.AddedCerts[0] != caPath {
		t.Errorf("AddedCerts = %+v, want [%s]", f.kc.AddedCerts, caPath)
	}
}

func TestBootstrapUntrustedReturnsActionableError(t *testing.T) {
	f := newFakes()
	f.fs.Files[caPath] = []byte("-----BEGIN CERTIFICATE-----")
	f.prober.Outcome = sysdep.ProbeUntrusted

	err := f.installer().Bootstrap(context.Background())
	if err == nil {
		t.Fatal("Bootstrap = nil, want an error on failed verification")
	}
	if err.Error() != msgUntrusted {
		t.Errorf("Bootstrap error = %q, want the actionable message %q", err.Error(), msgUntrusted)
	}
}
