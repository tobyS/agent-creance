package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// editGoldenCases pin the splice output for each structural shape AppendRule must
// handle: an existing populated list, the commented-out init stub (null egress), an
// egress mapping that has generators but no allow key, an empty file (build from
// scratch), a network section without egress, plus a deny-with-reason and a bare-host
// allow. Golden output proves comments and blank lines outside the insertion point
// survive. Regenerate with `make golden` or
// `go test ./internal/config -run TestAppendRuleGolden -update`.
var editGoldenCases = []struct {
	name string
	list RuleList
	rule Rule
}{
	{"existing_list", AllowList, Rule{Host: "example.com", Paths: strs("/api/")}},
	{"commented_stub", AllowList, Rule{Host: "example.com"}},
	{"with_generators", AllowList, Rule{Host: "api.github.com", Paths: strs("/repos/foo/")}},
	{"from_scratch", AllowList, Rule{Host: "example.com"}},
	{"network_only", AllowList, Rule{Host: "example.com"}},
	{"deny_reason", DenyList, Rule{Host: "tracker.example", Reason: "low quality source"}},
	{"bare_host", AllowList, Rule{Host: "*.example.com"}},
}

func TestAppendRuleGolden(t *testing.T) {
	for _, tc := range editGoldenCases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.name + ".in.yaml"
			// Reuse an input fixture for the variants that don't ship their own.
			switch tc.name {
			case "deny_reason", "bare_host":
				in = "existing_list.in.yaml"
			}
			src, err := os.ReadFile(filepath.Join("testdata", "edit", in))
			require.NoError(t, err)

			got, changed, err := AppendRule(src, tc.list, tc.rule)
			require.NoError(t, err)
			require.True(t, changed)

			// The result must parse and the appended rule must be present.
			cfg, err := Parse(got)
			require.NoError(t, err)
			require.True(t, containsRule(listRules(cfg, tc.list), tc.rule),
				"appended rule not found in parsed result")

			golden := filepath.Join("testdata", "edit", tc.name+".golden.yaml")
			if *update {
				require.NoError(t, os.WriteFile(golden, got, 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			require.NoError(t, err, "missing golden file; run with -update to create it")
			require.Equal(t, string(want), string(got))
		})
	}
}

// TestAppendRuleDuplicate: an identical rule (same host/paths/methods) is a no-op —
// changed is false and the bytes are returned unchanged, so no needless recompile or
// config churn. A differing reason does not make it a new rule.
func TestAppendRuleDuplicate(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "edit", "existing_list.in.yaml"))
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		list RuleList
		rule Rule
	}{
		{"allow same host+path", AllowList, Rule{Host: "api.github.com", Paths: strs("/repos/tobyS/this/")}},
		{"deny same host, different reason", DenyList, Rule{Host: "w3schools.com", Reason: "different wording"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := AppendRule(src, tc.list, tc.rule)
			require.NoError(t, err)
			require.False(t, changed, "expected a no-op for a duplicate rule")
			require.Equal(t, string(src), string(got))
		})
	}
}

// TestAppendRuleSemantics covers the host/path mapping without golden coupling: a bare
// host yields no paths; a host+path yields exactly that path; everything still parses.
func TestAppendRuleSemantics(t *testing.T) {
	src := []byte("network:\n  egress:\n    allow:\n      - host: seed.com\n")

	t.Run("bare host has no paths", func(t *testing.T) {
		got, changed, err := AppendRule(src, AllowList, Rule{Host: "bare.example"})
		require.NoError(t, err)
		require.True(t, changed)
		cfg, err := Parse(got)
		require.NoError(t, err)
		r := findHost(t, cfg.Network.Egress.Allow, "bare.example")
		require.Nil(t, r.Paths)
	})

	t.Run("host with path", func(t *testing.T) {
		got, _, err := AppendRule(src, AllowList, Rule{Host: "p.example", Paths: strs("/x/")})
		require.NoError(t, err)
		cfg, err := Parse(got)
		require.NoError(t, err)
		r := findHost(t, cfg.Network.Egress.Allow, "p.example")
		require.NotNil(t, r.Paths)
		require.Equal(t, []string{"/x/"}, *r.Paths)
	})
}

// TestAppendRuleRejectsBrokenInput: AppendRule refuses to edit a config that does not
// already parse, rather than appending to a broken file.
func TestAppendRuleRejectsBrokenInput(t *testing.T) {
	_, _, err := AppendRule([]byte("network:\n  egress:\n    allow: not-a-list\n"), AllowList, Rule{Host: "x.com"})
	require.Error(t, err)
}

func findHost(t *testing.T, rules []Rule, host string) Rule {
	t.Helper()
	for _, r := range rules {
		if r.Host == host {
			return r
		}
	}
	t.Fatalf("host %q not found in rules", host)
	return Rule{}
}
