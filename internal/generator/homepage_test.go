package generator

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobyS/agent-creance/internal/policy"
)

func TestHomepageRule(t *testing.T) {
	cases := []struct {
		name     string
		homepage string
		want     Rule
		ok       bool
	}{
		{
			"bare host -> host-wide",
			"https://react.dev/",
			Rule{Rule: policy.Rule{Host: "react.dev"}, Source: "src"},
			true,
		},
		{
			"no trailing slash -> host-wide",
			"https://laravel.com",
			Rule{Rule: policy.Rule{Host: "laravel.com"}, Source: "src"},
			true,
		},
		{
			"path-carrying -> path-scoped",
			"https://someuser.github.io/coollib/",
			Rule{Rule: policy.Rule{Host: "someuser.github.io", Paths: []string{"/coollib/"}}, Source: "src"},
			true,
		},
		{
			"uppercase host lowercased",
			"https://Example.COM/Docs",
			Rule{Rule: policy.Rule{Host: "example.com", Paths: []string{"/Docs/"}}, Source: "src"},
			true,
		},
		{
			// F12: a bare host on a known shared apex has no path to scope to and a
			// host-wide allow would cover every co-tenant, so no rule is emitted.
			"bare host on shared apex -> dropped",
			"https://sourceforge.net/",
			Rule{},
			false,
		},
		{
			// A path-carrying homepage on the same shared apex is still scoped (the
			// path branch wins), so it keeps its correct, narrow rule.
			"path-carrying on shared apex -> path-scoped",
			"https://sourceforge.net/projects/coollib/",
			Rule{Rule: policy.Rule{Host: "sourceforge.net", Paths: []string{"/projects/coollib/"}}, Source: "src"},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := homepageRule(tc.homepage, "src")
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestHomepageRule_NoHost(t *testing.T) {
	_, ok := homepageRule("not-a-url-without-host", "src")
	require.False(t, ok)
	_, ok = homepageRule("", "src")
	require.False(t, ok)
}
