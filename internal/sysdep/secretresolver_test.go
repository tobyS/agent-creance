package sysdep

import (
	"errors"
	"testing"
)

// The branchy part of OSSecretResolver is the reference parsing; it is pure, so
// we table-test it here (white-box, no fakes — those would import sysdeptest and
// form a cycle). The scheme dispatch and the backend-error mapping are exercised
// against fakes in the external test file (package sysdep_test).

func TestParseKeychainRef(t *testing.T) {
	cases := []struct {
		name        string
		rest        string
		wantService string
		wantAccount string
		wantErr     bool
	}{
		{name: "service only", rest: "Claude Code-credentials", wantService: "Claude Code-credentials"},
		{name: "service and account", rest: "GitHub/octocat", wantService: "GitHub", wantAccount: "octocat"},
		{name: "trailing slash is empty account", rest: "GitHub/", wantService: "GitHub"},
		{name: "empty service is an error", rest: "", wantErr: true},
		{name: "leading slash is empty service", rest: "/octocat", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, account, err := parseKeychainRef(tc.rest)
			if tc.wantErr {
				if !errors.Is(err, ErrUnknownSecretScheme) {
					t.Fatalf("parseKeychainRef(%q) err = %v, want ErrUnknownSecretScheme", tc.rest, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseKeychainRef(%q) unexpected err: %v", tc.rest, err)
			}
			if service != tc.wantService || account != tc.wantAccount {
				t.Errorf("parseKeychainRef(%q) = (%q, %q), want (%q, %q)",
					tc.rest, service, account, tc.wantService, tc.wantAccount)
			}
		})
	}
}

func TestParseEnvRef(t *testing.T) {
	cases := []struct {
		name    string
		rest    string
		want    string
		wantErr bool
	}{
		{name: "name", rest: "GH_TOKEN", want: "GH_TOKEN"},
		{name: "empty is an error", rest: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, err := parseEnvRef(tc.rest)
			if tc.wantErr {
				if !errors.Is(err, ErrUnknownSecretScheme) {
					t.Fatalf("parseEnvRef(%q) err = %v, want ErrUnknownSecretScheme", tc.rest, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEnvRef(%q) unexpected err: %v", tc.rest, err)
			}
			if name != tc.want {
				t.Errorf("parseEnvRef(%q) = %q, want %q", tc.rest, name, tc.want)
			}
		})
	}
}
