package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const setAuthSrc = `# top comment
network:
  egress:
    allow:
      # graphql needs a scoped token
      - host: api.github.com
        paths: ["/graphql"]
        methods: [POST]
      - host: example.com
`

// TestSetRuleAuthUpdatesExisting: binding an already-allowed host/path updates that
// rule's auth axis in place rather than being a no-op, and leaves comments intact.
func TestSetRuleAuthUpdatesExisting(t *testing.T) {
	rule := Rule{Host: "api.github.com", Paths: strs("/graphql"), Methods: strs("POST"), Inject: "github"}
	out, changed, err := SetRuleAuth([]byte(setAuthSrc), AllowList, rule)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(out), "# graphql needs a scoped token", "leading comment preserved")
	require.Contains(t, string(out), "# top comment", "top comment preserved")
	require.Contains(t, string(out), "inject: github")

	cfg, err := Parse(out)
	require.NoError(t, err)
	require.Len(t, cfg.Network.Egress.Allow, 2, "no rule added, existing rule updated")
	require.Equal(t, "github", cfg.Network.Egress.Allow[0].Inject)
}

// TestSetRuleAuthAppendsWhenAbsent: binding a not-yet-allowed host appends a new rule
// carrying the auth axis.
func TestSetRuleAuthAppendsWhenAbsent(t *testing.T) {
	rule := Rule{Host: "api.new.example", Paths: strs("/graphql"), Inject: "github"}
	out, changed, err := SetRuleAuth([]byte(setAuthSrc), AllowList, rule)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(out), "inject: github")

	cfg, err := Parse(out)
	require.NoError(t, err)
	require.Len(t, cfg.Network.Egress.Allow, 3, "new rule appended")
}

// TestSetRuleAuthInCage: --in-cage writes in_cage: true.
func TestSetRuleAuthInCage(t *testing.T) {
	rule := Rule{Host: "example.com", InCage: true}
	out, changed, err := SetRuleAuth([]byte(setAuthSrc), AllowList, rule)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(out), "in_cage: true")

	cfg, err := Parse(out)
	require.NoError(t, err)
	require.True(t, cfg.Network.Egress.Allow[1].InCage)
}

// TestSetRuleAuthNoOp: re-binding the same auth axis is a no-op (changed=false, bytes
// unchanged) so there is no needless recompile.
func TestSetRuleAuthNoOp(t *testing.T) {
	rule := Rule{Host: "example.com", InCage: true}
	once, _, err := SetRuleAuth([]byte(setAuthSrc), AllowList, rule)
	require.NoError(t, err)
	twice, changed, err := SetRuleAuth(once, AllowList, rule)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, string(once), string(twice))
}
