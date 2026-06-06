//go:build integration

// These tests drive the cage-built invocation through the real agent-safehouse
// (0.10.1) binary: they confirm safehouse accepts the constructed argv (both
// --append-profile fragments parse and compose), runs a trivial caged command to
// exit 0, that the injected env reaches inside the cage via --env-pass, and that
// the deny-all network baseline blocks direct egress. They run only under
// `make test-integration` and need macOS with `safehouse` on PATH (skip
// otherwise). The full isolation matrix is AC-0033's domain (M3 gate); this is the
// minimal "egress blocked except via proxy" smoke called for by AC-0023 step 4.
//
// safehouse applies a sandbox-exec policy, which a host that is itself already
// sandboxed cannot nest ("sandbox_apply: Operation not permitted"). When that is
// detected the tests skip rather than report a false pass/fail — run them on a
// host where safehouse can apply its policy.
package cage_test

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/cage"
	"github.com/tobyS/agent-creance/internal/config"
	"github.com/tobyS/agent-creance/internal/profile"
	"github.com/tobyS/agent-creance/internal/state"
	"github.com/tobyS/agent-creance/internal/sysdep"
)

func TestLiveSafehouseInvocation(t *testing.T) {
	requireSafehouse(t)
	proj, layout := setupLayout(t)
	b := cage.New(sysdep.OSFileSystem{}, sysdep.OSPathResolver{})

	cfg := &config.Config{
		Agent: config.Agent{
			Command: []string{"/bin/sh", "-c", `echo ok; echo "PROXY=$HTTPS_PROXY"; echo "CA=$SSL_CERT_FILE"`},
			Workdir: proj,
		},
		Safehouse: config.Safehouse{AddDirsRW: []string{proj}},
	}
	out, err := runCaged(t, b, cfg, layout, 18081) // no real proxy runs; testing argv + env wiring
	require.NoError(t, err, "safehouse run failed:\n%s", out)
	require.Contains(t, out, "ok", "caged command should run to completion")
	require.Contains(t, out, "PROXY=http://127.0.0.1:18081", "--env-pass must forward HTTPS_PROXY into the cage")
	require.Contains(t, out, ".mitmproxy/mitmproxy-ca-cert.pem", "SSL_CERT_FILE must point at the mitmproxy CA")
}

// TestLiveSafehouseEgressDenied confirms the deny-all baseline blocks a direct
// outbound connection from inside the cage. Skips if `nc` is absent.
func TestLiveSafehouseEgressDenied(t *testing.T) {
	requireSafehouse(t)
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc not on PATH")
	}
	proj, layout := setupLayout(t)
	b := cage.New(sysdep.OSFileSystem{}, sysdep.OSPathResolver{})

	// Raw connect to an external host:port (proxy env is irrelevant to raw nc); the
	// deny-all network baseline must refuse it. -G/-w cap the attempt at a few sec.
	cfg := &config.Config{
		Agent: config.Agent{
			Command: []string{"nc", "-G", "3", "-w", "3", "1.1.1.1", "443"},
			Workdir: proj,
		},
		Safehouse: config.Safehouse{AddDirsRW: []string{proj}},
	}
	out, err := runCaged(t, b, cfg, layout, 18081)
	require.Error(t, err, "direct egress should be denied by the deny-all baseline; got:\n%s", out)
	require.NotContains(t, out, "succeeded", "connection unexpectedly succeeded:\n%s", out)
}

func requireSafehouse(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("safehouse is macOS-only")
	}
	if _, err := exec.LookPath(cage.Binary); err != nil {
		t.Skipf("%s not on PATH", cage.Binary)
	}
}

// setupLayout resolves a per-project layout under a temp XDG_CACHE_HOME (so the
// test never touches the real ~/.cache), creates the state root, and stands in the
// deny-all network.sb baseline (normally the compile step's output).
func setupLayout(t *testing.T) (proj string, layout state.Layout) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	proj = t.TempDir()
	layout, err := state.New(sysdep.OSPathResolver{}).Resolve(proj)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(layout.Root, 0o700))
	require.NoError(t, os.WriteFile(layout.NetworkSB(), []byte(profile.RenderNetworkSB(nil)), 0o600))
	return proj, layout
}

// runCaged builds + prepares the cage invocation for cfg and runs it through real
// safehouse, returning combined output and the run error. It skips the test if the
// host cannot apply a nested sandbox-exec policy (so neither a false pass nor a
// false fail results from an unsandboxable environment).
func runCaged(t *testing.T, b *cage.Builder, cfg *config.Config, layout state.Layout, port int) (string, error) {
	t.Helper()
	in, err := b.Resolve(cfg, layout, port)
	require.NoError(t, err)
	require.NoError(t, b.Prepare(in))
	inv, err := cage.Build(in)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, inv.Path, inv.Args...)
	cmd.Env = append(os.Environ(), inv.Env...)
	out, runErr := cmd.CombinedOutput()
	if strings.Contains(string(out), "sandbox_apply: Operation not permitted") {
		t.Skip("host cannot apply a nested sandbox-exec policy; run on an unsandboxed macOS host")
	}
	return string(out), runErr
}
