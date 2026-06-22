package policy

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/config"
)

func TestMatchHost(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{"exact hit", "api.github.com", "api.github.com", true},
		{"exact miss", "api.github.com", "github.com", false},
		{"exact case-insensitive", "API.GitHub.com", "api.github.com", true},
		{"star matches any", "*", "anything.example", true},
		{"suffix single label", "*.medium.com", "foo.medium.com", true},
		{"suffix multi label", "*.medium.com", "a.b.medium.com", true},
		{"suffix excludes apex", "*.medium.com", "medium.com", false},
		{"suffix near miss", "*.medium.com", "amedium.com", false},
		{"suffix wrong host", "*.medium.com", "foo.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, matchHost(tc.pattern, tc.host))
		})
	}
}

func TestCanonicalHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		want string
	}{
		{"already canonical", "api.example.com", "api.example.com"},
		{"uppercase", "API.EXAMPLE.COM", "api.example.com"},
		{"trailing dot", "api.example.com.", "api.example.com"},
		{"port", "api.example.com:443", "api.example.com"},
		{"port and dot", "api.example.com.:443", "api.example.com"},
		{"empty port not stripped", "api.example.com:", "api.example.com:"},
		{"non-numeric port not stripped", "api.example.com:abc", "api.example.com:abc"},
		{"ipv6 literal untouched", "::1", "::1"},
		{"ipv4 with port", "127.0.0.1:8080", "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, canonicalHost(tc.host))
		})
	}
}

// TestHostDisposition exercises the Go CONNECT-stage decision directly (the corpus
// covers parity with Python; this pins the rule's branches).
func TestHostDisposition(t *testing.T) {
	rs := RuleSet{
		Allow: []Rule{
			{Host: "pass.example", Mode: ModePassthrough},
			{Host: "mixed.example", Mode: ModePassthrough},
			{Host: "mixed.example", Paths: []string{"/x"}, Mode: ModeIntercept},
			{Host: "icpt.example", Mode: ModeIntercept},
		},
		DenyAlways: []Rule{
			{Host: "pass.example", Mode: ModeIntercept, Reason: "blocked"},
		},
	}
	cases := []struct {
		name string
		host string
		want HostDisposition
	}{
		{"clean passthrough", "pass2.example", HostDisposition{Passthrough: false, DenyReason: ""}},
		{"passthrough with host deny", "pass.example", HostDisposition{Passthrough: true, DenyReason: "blocked"}},
		{"mixed mode resolves to intercept", "mixed.example", HostDisposition{Passthrough: false, DenyReason: ""}},
		{"intercept host", "icpt.example", HostDisposition{Passthrough: false, DenyReason: ""}},
		{"canonicalized host deny via trailing dot", "pass.example.", HostDisposition{Passthrough: true, DenyReason: "blocked"}},
		{"no match", "unknown.example", HostDisposition{Passthrough: false, DenyReason: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, rs.HostDisposition(tc.host))
		})
	}
}

func TestMatchPath(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"prefix covers subtree", "/repos/org/repo/", "/repos/org/repo/blob/main", true},
		{"prefix exact", "/repos/org/repo/", "/repos/org/repo", true},
		{"prefix does not match parent", "/repos/org/repo/", "/repos/org", false},
		{"prefix wrong branch", "/repos/org/repo/", "/repos/org/other", false},
		{"star within segment", "/@*", "/@scope", true},
		{"star covers subtree via prefix", "/@*", "/@scope/article", true},
		{"star does not cross slash", "/a*/b", "/a/c/b", false},
		{"star empty run", "/foo*", "/foo", true},
		{"doublestar at root", "**/.env", "/.env", true},
		{"doublestar at depth", "**/.env", "/a/b/.env", true},
		{"doublestar non match", "**/.env", "/a/b/c", false},
		{"doublestar git config", "**/.git/config", "/x/y/.git/config", true},
		{"doublestar middle zero", "/a/**/b", "/a/b", true},
		{"doublestar middle many", "/a/**/b", "/a/x/y/b", true},
		{"bare doublestar matches root", "**", "/", true},
		{"bare doublestar matches anything", "**", "/a/b/c", true},
		{"trailing slash normalized", "/v1", "/v1/", true},
		{"glued doublestar degrades to star", "/a**b", "/axyzb", true},
		{"glued doublestar does not cross slash", "/a**b", "/a/x/b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, matchPath(tc.pattern, tc.path))
		})
	}
}

func TestMatchMethod(t *testing.T) {
	cases := []struct {
		name    string
		methods []string
		method  string
		want    bool
	}{
		{"nil matches any", nil, "DELETE", true},
		{"membership hit", []string{"GET", "POST"}, "POST", true},
		{"membership miss", []string{"GET", "POST"}, "DELETE", false},
		{"case sensitive", []string{"GET"}, "get", false},
		{"empty list matches nothing", []string{}, "GET", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, matchMethod(tc.methods, tc.method))
		})
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name     string
		rs       RuleSet
		req      Request
		decision string
		mode     string
		matched  *MatchedRule
	}{
		{
			name: "allow filtered path and method",
			rs: RuleSet{Allow: []Rule{
				{Host: "api.github.com", Paths: []string{"/repos/org/repo/"}, Methods: []string{"GET", "POST"}, Mode: ModeIntercept},
			}},
			req:      Request{Host: "api.github.com", Path: "/repos/org/repo/issues", Method: "GET"},
			decision: DecisionAllow,
			mode:     ModeIntercept,
			matched:  &MatchedRule{List: "allow", Index: 0},
		},
		{
			name: "host-wide allow (nil paths)",
			rs: RuleSet{Allow: []Rule{
				{Host: "react.dev", Mode: ModeIntercept},
			}},
			req:      Request{Host: "react.dev", Path: "/learn/anything", Method: "GET"},
			decision: DecisionAllow,
			mode:     ModeIntercept,
			matched:  &MatchedRule{List: "allow", Index: 0},
		},
		{
			name:     "soft-deny default",
			rs:       RuleSet{Allow: []Rule{{Host: "react.dev", Mode: ModeIntercept}}},
			req:      Request{Host: "evil.example", Path: "/", Method: "GET"},
			decision: DecisionSoftDeny,
			mode:     "",
			matched:  nil,
		},
		{
			name: "host deny shadows allow",
			rs: RuleSet{
				Allow:      []Rule{{Host: "w3schools.com", Mode: ModeIntercept}},
				DenyAlways: []Rule{{Host: "w3schools.com", Mode: ModeIntercept, Reason: "low quality"}},
			},
			req:      Request{Host: "w3schools.com", Path: "/html", Method: "GET"},
			decision: DecisionHardDeny,
			mode:     ModeIntercept,
			matched:  &MatchedRule{List: "deny_always", Index: 0},
		},
		{
			name: "path-glob deny shadows host-wide allow",
			rs: RuleSet{
				Allow:      []Rule{{Host: "*", Paths: []string{"/**"}, Mode: ModeIntercept}},
				DenyAlways: []Rule{{Host: "*", Paths: []string{"**/.env"}, Mode: ModeIntercept, Reason: "secrets"}},
			},
			req:      Request{Host: "example.com", Path: "/config/.env", Method: "GET"},
			decision: DecisionHardDeny,
			mode:     ModeIntercept,
			matched:  &MatchedRule{List: "deny_always", Index: 0},
		},
		{
			name: "most-specific allow selects mode",
			rs: RuleSet{Allow: []Rule{
				{Host: "api.example.com", Mode: ModePassthrough},                        // host-wide
				{Host: "api.example.com", Paths: []string{"/v1/"}, Mode: ModeIntercept}, // path-scoped, more specific
			}},
			req:      Request{Host: "api.example.com", Path: "/v1/messages", Method: "POST"},
			decision: DecisionAllow,
			mode:     ModeIntercept,
			matched:  &MatchedRule{List: "allow", Index: 1},
		},
		{
			name: "passthrough suppresses path-scoped deny",
			rs: RuleSet{
				Allow:      []Rule{{Host: "api.anthropic.com", Mode: ModePassthrough}},
				DenyAlways: []Rule{{Host: "*", Paths: []string{"**/.env"}, Mode: ModeIntercept, Reason: "secrets"}},
			},
			req:      Request{Host: "api.anthropic.com", Path: "/v1/.env", Method: "POST"},
			decision: DecisionAllow,
			mode:     ModePassthrough,
			matched:  &MatchedRule{List: "allow", Index: 0},
		},
		{
			name: "host-level deny still enforced on passthrough host",
			rs: RuleSet{
				Allow:      []Rule{{Host: "api.anthropic.com", Mode: ModePassthrough}},
				DenyAlways: []Rule{{Host: "api.anthropic.com", Mode: ModeIntercept, Reason: "blocked"}},
			},
			req:      Request{Host: "api.anthropic.com", Path: "/v1/messages", Method: "POST"},
			decision: DecisionHardDeny,
			mode:     ModeIntercept,
			matched:  &MatchedRule{List: "deny_always", Index: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.rs.Decide(tc.req)
			require.Equal(t, tc.decision, got.Decision)
			require.Equal(t, tc.mode, got.Mode)
			require.Equal(t, tc.matched, got.Matched)
		})
	}
}

func TestFromConfig(t *testing.T) {
	cfg, err := config.Parse([]byte(`
network:
  egress:
    allow:
      - host: api.github.com
        paths: ["/repos/org/repo/"]
        methods: [GET, POST]
      - host: react.dev
        mode: intercept
    deny_always:
      - host: "*"
        paths: ["**/.env"]
        reason: "Secrets path."
`))
	require.NoError(t, err)

	rs := FromConfig(cfg.Network.Egress)

	require.Equal(t, RuleSet{
		Allow: []Rule{
			{Host: "api.github.com", Paths: []string{"/repos/org/repo/"}, Methods: []string{"GET", "POST"}, Mode: ModeIntercept},
			{Host: "react.dev", Mode: ModeIntercept},
		},
		DenyAlways: []Rule{
			{Host: "*", Paths: []string{"**/.env"}, Mode: ModeIntercept, Reason: "Secrets path."},
		},
	}, rs)

	// nil paths/methods on the host-wide rule must stay nil ("key omitted").
	require.Nil(t, rs.Allow[1].Paths)
	require.Nil(t, rs.Allow[1].Methods)
}
