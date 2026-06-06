package sysdep

import (
	"errors"
	"testing"
)

// The real OSKeychain shells out to /usr/bin/security, so its end-to-end
// behaviour is exercised only by the integration test (build tag `integration`).
// The risky part — mapping the tool's exit code / timeout to a Keychain
// sentinel — is pure, so we table-test it here without invoking the tool.

func TestInterpretSecurityErr(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
		timedOut bool
		want     error
	}{
		{name: "item not found", exitCode: secItemNotFound, want: ErrItemNotFound},
		{name: "locked keychain times out", timedOut: true, want: ErrKeychainLocked},
		{name: "timeout wins over exit code", exitCode: secItemNotFound, timedOut: true, want: ErrKeychainLocked},
		{name: "other exit code is unexpected", exitCode: 1, want: errUnexpectedSecurity},
		{name: "zero exit code is unexpected", exitCode: 0, want: errUnexpectedSecurity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := interpretSecurityErr(tc.exitCode, tc.timedOut)
			if !errors.Is(got, tc.want) {
				t.Errorf("interpretSecurityErr(%d, %v) = %v, want errors.Is(%v)",
					tc.exitCode, tc.timedOut, got, tc.want)
			}
		})
	}
}
