// Package setup performs agent-creance's one-time CA bootstrap: it generates the
// mitmproxy CA if absent, installs it into the login keychain, and proves the CA
// is actually trusted with a live verification. It is the library the `setup`
// command (AC-0028) and `doctor` (AC-0031) wire up; this package ships no command.
//
// Why a live verification (design.md "Post-install CA verification"): on macOS
// `security add-trusted-cert` returns exit 0 even when the user cancels the auth
// dialog — the cert lands in the keychain but is not actually trusted, which
// surfaces later as confusing TLS errors on the first `agent-creance run`. So
// after installing, setup spawns a short-lived mitmdump on a random loopback
// port, curls https://example.com through it, and checks the chain validates
// against the system trust store. Trust is proven functionally, never inferred
// from the install command's exit code.
//
// This is the robust counterpart to internal/setupcheck's cheap keychain-presence
// probe: run stays on the cheap check (it must not pay this cost every launch),
// while setup/doctor use the live Verify here.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/tobyS/agent-creance/internal/sysdep"
)

const (
	// caDirRel is mitmproxy's default config directory, relative to $HOME. The
	// runtime proxy (internal/proxy) uses this same default confdir, so the CA we
	// generate and trust here is exactly the one the cage's proxy presents.
	caDirRel = ".mitmproxy"
	// caCertFile is the certificate-only PEM mitmproxy writes — the file to trust
	// (mitmproxy-ca.pem also holds the private key; we never touch that one).
	caCertFile = "mitmproxy-ca-cert.pem"
	// mitmdumpBin is the headless mitmproxy binary, the same one internal/proxy
	// spawns. A first run materialises the CA in caDirRel; there is no standalone
	// "generate CA" subcommand.
	mitmdumpBin = "mitmdump"
)

const (
	// genMaxAttempts × genPollInterval bounds how long EnsureCA waits for mitmdump
	// to write the CA file after spawn (~5s). The CA is written early in startup,
	// well before the proxy binds, so this is generous.
	genMaxAttempts  = 100
	genPollInterval = 50 * time.Millisecond
)

// Installer composes the sysdep seams the CA bootstrap needs. Construct it with
// NewInstaller; the `setup`/`doctor` commands will wire the real OS seams from the
// App, tests wire the sysdeptest fakes.
type Installer struct {
	fs      sysdep.FileSystem
	kc      sysdep.Keychain
	proc    sysdep.ProcessManager
	ports   sysdep.PortAllocator
	prober  sysdep.TLSProber
	sleeper sysdep.Sleeper
	paths   sysdep.PathResolver
}

// NewInstaller wires an Installer from its seams.
func NewInstaller(
	fsys sysdep.FileSystem,
	kc sysdep.Keychain,
	proc sysdep.ProcessManager,
	ports sysdep.PortAllocator,
	prober sysdep.TLSProber,
	sleeper sysdep.Sleeper,
	paths sysdep.PathResolver,
) *Installer {
	return &Installer{fs: fsys, kc: kc, proc: proc, ports: ports, prober: prober, sleeper: sleeper, paths: paths}
}

// EnsureCA returns the path to the mitmproxy CA certificate, generating it with a
// brief mitmdump run if it is not already present. It is idempotent: an existing
// CA is left untouched (so re-running setup never regenerates keys).
func (i *Installer) EnsureCA(ctx context.Context) (string, error) {
	certPath, err := i.caCertPath()
	if err != nil {
		return "", err
	}
	switch _, err := i.fs.Stat(certPath); {
	case err == nil:
		return certPath, nil // already generated — idempotent
	case errors.Is(err, fs.ErrNotExist):
		// fall through to generation
	default:
		return "", fmt.Errorf("setup: stat CA %s: %w", certPath, err)
	}
	if err := i.generateCA(ctx, certPath); err != nil {
		return "", err
	}
	return certPath, nil
}

// generateCA spawns a throwaway mitmdump (which writes the CA into ~/.mitmproxy on
// startup), waits for the cert file to appear, then tears the proxy down.
func (i *Installer) generateCA(ctx context.Context, certPath string) error {
	port, err := i.ports.Allocate()
	if err != nil {
		return fmt.Errorf("setup: allocate port for CA generation: %w", err)
	}
	pid, err := i.proc.Spawn(ctx, mitmdumpBin,
		"--listen-host", "127.0.0.1", "--listen-port", strconv.Itoa(port), "-q")
	if err != nil {
		return fmt.Errorf("setup: spawn mitmdump for CA generation: %w", err)
	}
	// The proxy is only needed to materialise the CA; always tear it down.
	defer func() { _ = i.proc.Signal(pid, syscall.SIGTERM) }()

	for attempt := 0; attempt < genMaxAttempts; attempt++ {
		if _, err := i.fs.Stat(certPath); err == nil {
			return nil
		}
		if err := i.sleeper.Sleep(ctx, genPollInterval); err != nil {
			return fmt.Errorf("setup: wait for CA generation: %w", err)
		}
	}
	return fmt.Errorf("setup: mitmproxy did not generate %s within %s",
		certPath, time.Duration(genMaxAttempts)*genPollInterval)
}

// InstallCA imports the CA certificate at certPath into the login keychain as a
// trusted SSL root. A nil error means the install command ran — NOT that trust
// was applied (security returns 0 even on a cancelled dialog); call Verify to
// confirm trust actually took effect.
func (i *Installer) InstallCA(certPath string) error {
	if err := i.kc.AddTrustedCert(certPath); err != nil {
		return fmt.Errorf("setup: install CA into login keychain: %w", err)
	}
	return nil
}

// caCertPath resolves $HOME/.mitmproxy/mitmproxy-ca-cert.pem.
func (i *Installer) caCertPath() (string, error) {
	home, err := i.paths.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("setup: resolve home dir: %w", err)
	}
	return filepath.Join(home, caDirRel, caCertFile), nil
}
