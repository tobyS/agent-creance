package generator

import (
	"net/url"
	"strings"

	"github.com/tobyS/agent-creance/internal/policy"
)

// forgeHost is one content host a forge serves, with how to scope it to a given
// <org>/<repo>. host and pathTmpl may contain the placeholders "<org>"/"<repo>";
// an empty pathTmpl means the rule is host-wide (the host's URLs are not org-scoped).
type forgeHost struct {
	host       string
	pathTmpl   string
	lowerTrust bool
}

// forges maps a repository's web host to the full set of content hosts that serving
// that repository touches. This is data, not per-generator code: extending it to a
// new forge (or expanding a manual `allow <repo-url>`) is a table edit. See
// docs/design.md, "Allowlist generators" → Forge content hosts.
var forges = map[string][]forgeHost{
	"github.com": {
		{host: "github.com", pathTmpl: "/<org>/<repo>/"},                // web view + .git ops
		{host: "raw.githubusercontent.com", pathTmpl: "/<org>/<repo>/"}, // raw file fetches
		{host: "codeload.github.com", pathTmpl: "/<org>/<repo>/"},       // tarball/zip + clone
		{host: "<org>.github.io", pathTmpl: "/<repo>/"},                 // project pages
		// Release-asset CDN: hash-addressed URLs are not org-scoped, so this is
		// necessarily host-wide and flagged lower-trust (a stricter model can drop it).
		{host: "objects.githubusercontent.com", pathTmpl: "", lowerTrust: true},
	},
	"gitlab.com": {
		{host: "gitlab.com", pathTmpl: "/<org>/<repo>/"}, // web view + .git ops + raw/archive paths
		{host: "<org>.gitlab.io", pathTmpl: "/<repo>/"},  // project pages
	},
}

// repositoryRules returns the allow rules for a package's repository URL, annotated
// with src. A repository on a known forge expands to that forge's companion content
// hosts; any other host yields a single rule scoped to <org>/<repo>. A URL with no
// host or fewer than two path segments yields no rules (nil).
func repositoryRules(repoURL, src string) []Rule {
	host, org, repo, ok := normalizeRepoURL(repoURL)
	if !ok {
		return nil
	}
	if hosts, found := forges[host]; found {
		out := make([]Rule, 0, len(hosts))
		for _, fh := range hosts {
			r := Rule{
				Rule:       policy.Rule{Host: strings.ToLower(substitutePlaceholders(fh.host, org, repo))},
				Source:     src,
				LowerTrust: fh.lowerTrust,
			}
			if fh.pathTmpl != "" {
				r.Rule.Paths = []string{substitutePlaceholders(fh.pathTmpl, org, repo)}
			}
			out = append(out, r)
		}
		return out
	}
	return []Rule{{
		Rule:   policy.Rule{Host: host, Paths: []string{"/" + org + "/" + repo + "/"}},
		Source: src,
	}}
}

// substitutePlaceholders replaces the <org>/<repo> placeholders in a forge host or
// path template.
func substitutePlaceholders(tmpl, org, repo string) string {
	return strings.NewReplacer("<org>", org, "<repo>", repo).Replace(tmpl)
}

// normalizeRepoURL extracts the host and first two path segments (org, repo) from a
// repository URL in any of the forms registries report: a leading "git+", an scp-like
// "git@host:org/repo.git", "git://"/"ssh://"/"http(s)://" URLs, and trailing ".git"
// or "/". ok is false when no host or fewer than two path segments can be extracted —
// the caller emits no repository rule in that case. host is lower-cased; org/repo are
// kept verbatim (the design trusts registry fields without rewriting).
func normalizeRepoURL(raw string) (host, org, repo string, ok bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "git+")
	if s == "" {
		return "", "", "", false
	}

	var hostStr, pathStr string
	if !strings.Contains(s, "://") && strings.Contains(s, "@") && strings.Contains(s, ":") {
		// scp-like "user@host:org/repo.git": split host from path on the first ":"
		// after the "@".
		rest := s[strings.Index(s, "@")+1:]
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return "", "", "", false
		}
		hostStr, pathStr = rest[:colon], rest[colon+1:]
	} else {
		u, err := url.Parse(s)
		if err != nil {
			return "", "", "", false
		}
		hostStr, pathStr = u.Hostname(), u.Path
	}

	hostStr = strings.ToLower(strings.TrimSpace(hostStr))
	if hostStr == "" {
		return "", "", "", false
	}

	pathStr = strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(pathStr, "/"), ".git"), "/")
	var segs []string
	for _, seg := range strings.Split(pathStr, "/") {
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	if len(segs) < 2 {
		return "", "", "", false
	}
	return hostStr, segs[0], segs[1], true
}
