package sysdep

import "testing"

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
