package sysdep

import (
	"slices"
	"strings"
	"testing"
)

// The real OSTLSProber shells out to curl, so its end-to-end behaviour is
// exercised only by the integration test. The risky part — mapping curl's exit
// code to a trust verdict — is pure, so we table-test it here without curl.

func TestClassifyCurlExit(t *testing.T) {
	cases := []struct {
		name string
		code int
		want ProbeOutcome
	}{
		{name: "exit 0 is trusted", code: 0, want: ProbeTrusted},
		{name: "exit 60 is untrusted CA", code: 60, want: ProbeUntrusted},
		{name: "exit 35 (tls connect) is an error", code: 35, want: ProbeError},
		{name: "exit 7 (connect refused) is an error", code: 7, want: ProbeError},
		{name: "exit 6 (resolve host) is an error", code: 6, want: ProbeError},
		{name: "unknown exit is an error", code: 99, want: ProbeError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyCurlExit(tc.code); got != tc.want {
				t.Errorf("ClassifyCurlExit(%d) = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

// TestCurlProbeArgsValidatesAgainstSystemStoreOnly is the F10 soundness assertion:
// the probe's argv must never weaken certificate validation, or the CA check passes
// spuriously even when the CA is untrusted. We assert the dangerous flags are absent
// and the proxy/target are wired correctly, without running curl.
func TestCurlProbeArgsValidatesAgainstSystemStoreOnly(t *testing.T) {
	const proxyURL = "http://127.0.0.1:54321"
	const targetURL = "https://example.com"
	args := curlProbeArgs(proxyURL, targetURL)

	// Anything that would accept a cert not anchored in the system trust store is
	// forbidden. --cacert/--capath would also take an argument, so a substring check
	// catches both the flag and any "=value" form.
	forbidden := []string{"-k", "--insecure", "--cacert", "--capath", "--proxy-insecure"}
	for _, a := range args {
		for _, bad := range forbidden {
			if a == bad || strings.HasPrefix(a, bad+"=") {
				t.Errorf("curlProbeArgs contains forbidden flag %q (weakens trust validation): %v", a, args)
			}
		}
	}

	// The probe must actually go through the proxy and hit the target.
	i := slices.Index(args, "--proxy")
	if i < 0 || i+1 >= len(args) || args[i+1] != proxyURL {
		t.Errorf("curlProbeArgs missing `--proxy %s`: %v", proxyURL, args)
	}
	if len(args) == 0 || args[len(args)-1] != targetURL {
		t.Errorf("curlProbeArgs should end with the target URL %q: %v", targetURL, args)
	}
}
