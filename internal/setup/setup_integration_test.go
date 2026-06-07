//go:build integration

// These tests exercise the real CA bootstrap against live tools (mitmdump, curl,
// /usr/bin/security) on a configured macOS host. They run only under
// `make test-integration` (the integration build tag), never in the hermetic unit
// suite, and are gated on spike S1 (AC-0001), which proved interception validates
// against the trusted mitmproxy CA.
//
// TestVerifyLive is non-destructive: it generates the CA if absent (a write under
// ~/.mitmproxy only) and verifies trust, skipping when the CA is not yet trusted.
// TestBootstrapLive performs the full install — it invokes
// `security add-trusted-cert`, which prompts for the account password and mutates
// the developer's trust settings — so it is opt-in via CREANCE_LIVE_CA_INSTALL=1
// and never runs silently in CI.
package setup_test

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/setup"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

// liveInstaller wires the real OS seams, skipping when the live tools needed for
// generation/verification are not installed.
func liveInstaller(t *testing.T) *setup.Installer {
	t.Helper()
	for _, tool := range []string{"mitmdump", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed; skipping live CA test", tool)
		}
	}
	return setup.NewInstaller(
		sysdep.OSFileSystem{},
		sysdep.OSKeychain{},
		sysdep.OSProcessManager{},
		sysdep.OSPortAllocator{},
		sysdep.OSTLSProber{},
		sysdep.OSSleeper{},
		sysdep.OSPathResolver{},
	)
}

func TestVerifyLive(t *testing.T) {
	inst := liveInstaller(t)
	ctx := context.Background()

	// Generating the CA (if absent) only writes ~/.mitmproxy — non-destructive.
	if _, err := inst.EnsureCA(ctx); err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}

	res, err := inst.Verify(ctx)
	require.NoError(t, err, "verification probe should run cleanly with mitmdump+curl present")
	if !res.OK() {
		t.Skipf("mitmproxy CA is not trusted on this host (%s); "+
			"run TestBootstrapLive with CREANCE_LIVE_CA_INSTALL=1 to install it", res.Message())
	}
	require.Equal(t, setup.StatusTrusted, res.Status)
}

func TestBootstrapLive(t *testing.T) {
	if os.Getenv("CREANCE_LIVE_CA_INSTALL") != "1" {
		t.Skip("set CREANCE_LIVE_CA_INSTALL=1 to run the destructive full bootstrap " +
			"(invokes security add-trusted-cert and changes your keychain trust settings)")
	}
	inst := liveInstaller(t)

	err := inst.Bootstrap(context.Background())
	require.NoError(t, err, "full generate+install+verify should report the CA trusted end to end")
}
