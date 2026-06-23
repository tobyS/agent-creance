package generator

import (
	"net/url"
	"strings"

	"github.com/tobyS/agent-creance/internal/policy"
)

// homepageRule returns the allow rule for a package's homepage URL, annotated with
// src. A bare-host homepage ("https://react.dev/") yields a host-wide rule; a
// homepage carrying a path ("https://someuser.github.io/coollib/") is scoped to that
// path prefix, so a package on a shared, path-multiplexed host does not allowlist
// every other tenant's content. ok is false (no rule) when the URL has no host, or
// when the homepage is a bare host on a known shared-apex host (F12): there is no
// path to scope to, and a host-wide allow would cover every co-tenant, so the rule
// is dropped (see sharedApexHosts).
func homepageRule(homepage, src string) (Rule, bool) {
	u, err := url.Parse(strings.TrimSpace(homepage))
	if err != nil {
		return Rule{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return Rule{}, false
	}
	r := Rule{Rule: policy.Rule{Host: host}, Source: src}
	if path := strings.Trim(u.Path, "/"); path != "" {
		r.Rule.Paths = []string{"/" + path + "/"}
		return r, true
	}
	// Bare host: a host-wide allow on a shared-apex host would over-allow, and
	// there is no path to scope to, so emit nothing.
	if isSharedApex(host) {
		return Rule{}, false
	}
	return r, true
}
