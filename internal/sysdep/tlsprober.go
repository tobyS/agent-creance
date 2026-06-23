package sysdep

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// TLSProber makes an HTTPS request through an HTTP proxy and reports whether the
// server's certificate chain validated against the macOS system trust store.
// It is the load-bearing primitive of setup's post-install CA verification: after
// installing the mitmproxy CA, setup spawns a throwaway proxy, probes a public
// HTTPS host through it, and trusts the result — NOT the exit code of
// `security add-trusted-cert`, which returns 0 even when the user cancels the
// trust dialog (design.md "Post-install CA verification").
//
// Why route this through the seam (for someone coming from PHP/TS): the real
// probe shells out to curl and depends on a live proxy and the host keychain, so
// it can only run under the integration build tag. Packages take a TLSProber and
// call *that*; production wires OSTLSProber, tests wire the fake in sysdeptest.
type TLSProber interface {
	// ProbeViaProxy issues an HTTPS GET to targetURL through the HTTP proxy at
	// proxyURL (e.g. "http://127.0.0.1:54321"), validating the server certificate
	// against the system trust store ONLY — no extra CA bundle, because the whole
	// point is to prove the CA is trusted system-wide. The ProbeOutcome classifies
	// whether the chain validated. A non-nil error means the probe could not run at
	// all (e.g. curl is not installed), distinct from a clean untrusted verdict.
	ProbeViaProxy(ctx context.Context, proxyURL, targetURL string) (ProbeOutcome, error)
}

// ProbeOutcome is the verdict of a TLS-through-proxy probe.
type ProbeOutcome int

const (
	// ProbeTrusted means the chain validated against the system trust store
	// (curl exit 0) — the CA is trusted.
	ProbeTrusted ProbeOutcome = iota
	// ProbeUntrusted means the server cert could not be authenticated against the
	// system trust store (curl exit 60) — the CA is NOT trusted. This is the
	// silent-cancel / missing-trust failure mode setup must catch.
	ProbeUntrusted
	// ProbeError means curl failed for some other reason (handshake error,
	// unreachable proxy, DNS, timeout) — an environment problem, not a trust
	// verdict. Callers treat it as a genuine failure rather than "untrusted".
	ProbeError
)

// curl exit codes we classify (everything.curl.dev/cmdline/exitcode).
const (
	curlExitOK          = 0  // request completed, TLS validated
	curlExitUntrustedCA = 60 // peer certificate cannot be authenticated with known CA certificates
)

// ClassifyCurlExit maps a curl exit code to a ProbeOutcome. It is pure so the
// mapping is table-testable without invoking curl.
func ClassifyCurlExit(code int) ProbeOutcome {
	switch code {
	case curlExitOK:
		return ProbeTrusted
	case curlExitUntrustedCA:
		return ProbeUntrusted
	default:
		return ProbeError
	}
}

// OSTLSProber is the production TLSProber backed by the system curl.
type OSTLSProber struct{}

var _ TLSProber = (*OSTLSProber)(nil)

// curlProbeArgs builds the curl argv for the trust probe. It is a pure function so
// the load-bearing soundness property — that the probe validates the re-signed leaf
// against the system trust store ONLY — can be asserted by a unit test without
// running curl: the argv deliberately carries no -k/--insecure (would accept any
// cert), no --cacert (would trust an extra bundle instead of the system store), and
// no --proxy-insecure. If any of those crept in, the whole CA verification (and the
// same check in doctor) would pass spuriously even when the CA is not trusted
// (AC-0059 F10).
//
// -sS is quiet but still reports errors; -o /dev/null discards the body; --proxy
// routes the HTTPS request through the throwaway proxy (curl CONNECT-tunnels and
// validates the re-signed leaf against the system trust store). --retry-connrefused
// absorbs the proxy's startup race in the curl subprocess, so the caller needs no
// readiness sleep.
func curlProbeArgs(proxyURL, targetURL string) []string {
	return []string{
		"-sS", "-o", "/dev/null",
		"--proxy", proxyURL,
		"--retry", "5", "--retry-connrefused", "--retry-delay", "1",
		targetURL,
	}
}

func (OSTLSProber) ProbeViaProxy(ctx context.Context, proxyURL, targetURL string) (ProbeOutcome, error) {
	cmd := exec.CommandContext(ctx, "curl", curlProbeArgs(proxyURL, targetURL)...)
	err := cmd.Run()
	if err == nil {
		return ProbeTrusted, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ClassifyCurlExit(exitErr.ExitCode()), nil
	}
	// curl could not be started at all (e.g. not installed).
	return ProbeError, fmt.Errorf("sysdep: tls probe: %w", err)
}
