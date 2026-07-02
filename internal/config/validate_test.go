package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "regenerate golden files")

// TestGoldenErrors pins the exact rendered error for each invalid fixture. The
// messages are golden-tested (not asserted inline) because their human-readable
// wording is the deliverable — an operator must be able to fix the config from the
// message alone. Regenerate with `make golden` or
// `go test ./internal/config -run TestGoldenErrors -update`.
func TestGoldenErrors(t *testing.T) {
	cases := []string{
		"passthrough_with_paths",
		"passthrough_with_methods",
		"bad_mode",
		"empty_host",
		"bad_host_service_port",
		"unknown_top_key",
		"unknown_nested_key",
		"inject_and_in_cage",
		"passthrough_with_inject",
		"credential_bad_source",
		"credential_bad_template",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name+".yaml"))
			require.NoError(t, err)

			_, perr := Parse(data)
			require.Error(t, perr, "expected Parse to reject %s.yaml", name)
			got := perr.Error()

			golden := filepath.Join("testdata", name+".golden")
			if *update {
				require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err, "missing golden file; run with -update to create it")
			require.Equal(t, string(want), got)
		})
	}
}

// TestValidate covers the pass/fail validation logic without golden coupling, so
// `go test ./internal/config -run TestValidate` is a meaningful check (ticket
// verification step 3).
func TestValidate(t *testing.T) {
	cases := []struct {
		name         string
		yaml         string
		wantErr      bool
		wantContains string
	}{
		{
			name:    "intercept default is valid",
			yaml:    "network:\n  egress:\n    allow:\n      - host: react.dev\n",
			wantErr: false,
		},
		{
			name:    "intercept with paths and methods is valid",
			yaml:    "network:\n  egress:\n    allow:\n      - host: api.github.com\n        paths: [\"/x\"]\n        methods: [GET]\n",
			wantErr: false,
		},
		{
			name:    "passthrough without paths or methods is valid",
			yaml:    "network:\n  egress:\n    allow:\n      - host: api.anthropic.com\n        mode: passthrough\n",
			wantErr: false,
		},
		{
			name:         "passthrough with paths is rejected",
			yaml:         "network:\n  egress:\n    allow:\n      - host: api.anthropic.com\n        mode: passthrough\n        paths: [\"/x\"]\n",
			wantErr:      true,
			wantContains: "use mode: intercept to filter by path",
		},
		{
			name:         "passthrough with methods is rejected",
			yaml:         "network:\n  egress:\n    allow:\n      - host: api.anthropic.com\n        mode: passthrough\n        methods: [GET]\n",
			wantErr:      true,
			wantContains: "use mode: intercept to filter by method",
		},
		{
			name:         "passthrough with empty paths list is still rejected",
			yaml:         "network:\n  egress:\n    allow:\n      - host: api.anthropic.com\n        mode: passthrough\n        paths: []\n",
			wantErr:      true,
			wantContains: "cannot carry paths",
		},
		{
			name:         "unknown mode is rejected",
			yaml:         "network:\n  egress:\n    allow:\n      - host: react.dev\n        mode: observe\n",
			wantErr:      true,
			wantContains: `unknown mode "observe"`,
		},
		{
			name:         "missing host is rejected",
			yaml:         "network:\n  egress:\n    deny_always:\n      - reason: nope\n",
			wantErr:      true,
			wantContains: "missing a host",
		},
		{
			name:         "host_services port out of range is rejected",
			yaml:         "network:\n  host_services:\n    - mysql:0\n",
			wantErr:      true,
			wantContains: "out of range 1-65535",
		},
		{
			name:         "host_services without a port is rejected",
			yaml:         "network:\n  host_services:\n    - mysql\n",
			wantErr:      true,
			wantContains: "not in label:port form",
		},
		{
			name:         "host_services label with a newline is rejected",
			yaml:         "network:\n  host_services:\n    - \"x\\n(allow network*):3306\"\n",
			wantErr:      true,
			wantContains: "control character in its label",
		},
		{
			name:         "lowercase method is rejected",
			yaml:         "network:\n  egress:\n    allow:\n      - host: api.github.com\n        methods: [get]\n",
			wantErr:      true,
			wantContains: "non-uppercase method",
		},
		{
			name:         "unknown method is rejected",
			yaml:         "network:\n  egress:\n    allow:\n      - host: api.github.com\n        methods: [FOOBAR]\n",
			wantErr:      true,
			wantContains: `unknown method "FOOBAR"`,
		},
		{
			name:         "host with a scheme is rejected",
			yaml:         "network:\n  egress:\n    allow:\n      - host: http://x/y\n",
			wantErr:      true,
			wantContains: "valid form: a bare hostname like example.com",
		},
		{
			name:         "host that is only whitespace is rejected",
			yaml:         "network:\n  egress:\n    allow:\n      - host: \" \"\n",
			wantErr:      true,
			wantContains: "invalid host",
		},
		{
			name:    "wildcard suffix host is valid",
			yaml:    "network:\n  egress:\n    allow:\n      - host: \"*.example.com\"\n        methods: [GET, POST]\n",
			wantErr: false,
		},
		{
			name:    "star host is valid",
			yaml:    "network:\n  egress:\n    allow:\n      - host: \"*\"\n",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantContains != "" {
					require.Contains(t, err.Error(), tc.wantContains)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestValidate_ReportsAllIssues confirms validation accumulates problems rather than
// stopping at the first, matching the prereq error-as-data convention.
func TestValidate_ReportsAllIssues(t *testing.T) {
	_, err := Parse([]byte(
		"network:\n" +
			"  egress:\n" +
			"    allow:\n" +
			"      - host: api.anthropic.com\n" +
			"        mode: passthrough\n" +
			"        paths: [\"/x\"]\n" +
			"        methods: [GET]\n",
	))
	require.Error(t, err)
	msg := err.Error()
	require.Equal(t, 2, strings.Count(msg, "\n  - "), "expected two bulleted issues in:\n%s", msg)
}
