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
// every other tenant's content. ok is false (no rule) when the URL has no host.
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
	}
	return r, true
}
